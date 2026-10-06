package core

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Microsecond)
	}
}

func TestKeyLocksFIFO(t *testing.T) {
	l := newKeyLocks()
	ctx := context.Background()
	if err := l.acquire(ctx, "k"); err != nil {
		t.Fatal(err)
	}

	const n = 1000
	var mu sync.Mutex
	var order []int
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.acquire(ctx, "k"); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
			l.release("k")
		}()
		// Enqueue strictly one after another, so arrival order is i.
		waitFor(t, fmt.Sprintf("waiter %d to queue", i), func() bool { return l.waiting("k") == i+1 })
	}
	l.release("k")
	wg.Wait()
	for i, got := range order {
		if got != i {
			t.Fatalf("position %d got waiter %d; order not FIFO", i, got)
		}
	}
	if l.size() != 0 {
		t.Fatalf("%d keys left after all released", l.size())
	}
}

func TestKeyLocksIndependentKeysRunInParallel(t *testing.T) {
	l := newKeyLocks()
	ctx := context.Background()
	if err := l.acquire(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	// "b" is free even though "a" is held.
	done := make(chan error, 1)
	go func() { done <- l.acquire(ctx, "b") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("key b blocked by key a")
	}
	l.release("a")
	l.release("b")
}

func TestKeyLocksCancelWhileWaiting(t *testing.T) {
	l := newKeyLocks()
	if err := l.acquire(context.Background(), "k"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- l.acquire(ctx, "k") }()
	waitFor(t, "waiter", func() bool { return l.waiting("k") == 1 })
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if l.waiting("k") != 0 {
		t.Fatal("cancelled waiter still queued")
	}
	// The key still works for the next one in line.
	next := make(chan error, 1)
	go func() { next <- l.acquire(context.Background(), "k") }()
	waitFor(t, "next waiter", func() bool { return l.waiting("k") == 1 })
	l.release("k")
	if err := <-next; err != nil {
		t.Fatal(err)
	}
	l.release("k")
	if l.size() != 0 {
		t.Fatal("key leaked")
	}
}

func TestKeyLocksAcquireAllReleasesOnCancel(t *testing.T) {
	l := newKeyLocks()
	if err := l.acquire(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := l.acquireAll(ctx, []string{"b", "a"}) // takes a, then waits for b
		errc <- err
	}()
	waitFor(t, "waiter on b", func() bool { return l.waiting("b") == 1 })
	cancel()
	if err := <-errc; err == nil {
		t.Fatal("acquireAll succeeded after cancel")
	}
	// "a" must have been released again.
	rel, err := l.acquireAll(context.Background(), []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	rel()
	l.release("b")
	if l.size() != 0 {
		t.Fatal("keys leaked")
	}
}

func TestKeyLocksNoDeadlockRandomSets(t *testing.T) {
	l := newKeyLocks()
	keys := []string{"k1", "k2", "k3", "k4", "k5"}
	const workers, iterations = 16, 10000 / 16
	counters := make(map[string]int)
	var cmu sync.Mutex
	var wg sync.WaitGroup
	done := make(chan struct{})
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			r := rand.New(rand.NewSource(seed))
			for i := 0; i < iterations; i++ {
				var set []string
				for _, k := range keys {
					if r.Intn(2) == 0 {
						set = append(set, k)
					}
				}
				r.Shuffle(len(set), func(i, j int) { set[i], set[j] = set[j], set[i] })
				release, err := l.acquireAll(context.Background(), set)
				if err != nil {
					t.Error(err)
					return
				}
				cmu.Lock()
				for _, k := range set {
					counters[k]++
				}
				cmu.Unlock()
				release()
			}
		}(int64(w))
	}
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("deadlock: random multi-key acquisition did not finish")
	}
	if l.size() != 0 {
		t.Fatalf("%d keys still held", l.size())
	}
}

func TestKeyLocksReleaseUnheldPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("release of an unheld key did not panic")
		}
	}()
	newKeyLocks().release("nope")
}
