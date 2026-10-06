package core

import (
	"context"
	"sort"
	"sync"
)

// keyLocks gives each resource key a FIFO queue: actions on the same key
// run one at a time in the order they reached it, and actions on different
// keys run in parallel. Queues are created on first use and removed when
// empty.
type keyLocks struct {
	mu   sync.Mutex
	keys map[string]*keyQueue
}

type keyQueue struct {
	held    bool
	waiters []chan struct{} // closed to hand the key to the first waiter
}

func newKeyLocks() *keyLocks { return &keyLocks{keys: map[string]*keyQueue{}} }

// acquire waits for key. If ctx ends first it gives up its place and
// returns ctx's error.
func (l *keyLocks) acquire(ctx context.Context, key string) error {
	l.mu.Lock()
	q := l.keys[key]
	if q == nil {
		q = &keyQueue{}
		l.keys[key] = q
	}
	if !q.held {
		q.held = true
		l.mu.Unlock()
		return nil
	}
	ch := make(chan struct{})
	q.waiters = append(q.waiters, ch)
	l.mu.Unlock()

	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		l.mu.Lock()
		for i, w := range q.waiters {
			if w == ch {
				q.waiters = append(q.waiters[:i], q.waiters[i+1:]...)
				l.mu.Unlock()
				return ctx.Err()
			}
		}
		l.mu.Unlock()
		// The key was handed over just as ctx ended: pass it on.
		l.release(key)
		return ctx.Err()
	}
}

// release hands key to the next waiter, or frees it.
func (l *keyLocks) release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	q := l.keys[key]
	if q == nil || !q.held {
		panic("core: release of a resource key that is not held: " + key)
	}
	if len(q.waiters) > 0 {
		next := q.waiters[0]
		q.waiters = q.waiters[1:]
		close(next) // ownership passes; held stays true
		return
	}
	delete(l.keys, key)
}

// acquireAll takes every key in sorted order, which rules out deadlocks
// between actions needing overlapping sets. On error it releases what it
// took. The returned function releases all keys, in reverse order.
func (l *keyLocks) acquireAll(ctx context.Context, keys []string) (func(), error) {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	for i, k := range sorted {
		if err := l.acquire(ctx, k); err != nil {
			for j := i - 1; j >= 0; j-- {
				l.release(sorted[j])
			}
			return nil, err
		}
	}
	return func() {
		for i := len(sorted) - 1; i >= 0; i-- {
			l.release(sorted[i])
		}
	}, nil
}

// waiting reports how many actions wait for key (for tests).
func (l *keyLocks) waiting(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if q := l.keys[key]; q != nil {
		return len(q.waiters)
	}
	return 0
}

// size reports how many keys are held (for tests).
func (l *keyLocks) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.keys)
}
