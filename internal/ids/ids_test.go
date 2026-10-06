package ids

import (
	"sync"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/internal/clock"
)

func TestNewHasPrefixAndTime(t *testing.T) {
	at := time.Date(2026, 10, 4, 19, 2, 11, 0, time.UTC)
	g := NewGenerator(clock.NewFake(at))

	id := g.New(Action)
	prefix, ts, err := Parse(id)
	if err != nil {
		t.Fatal(err)
	}
	if prefix != Action {
		t.Fatalf("prefix = %q", prefix)
	}
	if !ts.Equal(at) {
		t.Fatalf("time = %v, want %v", ts, at)
	}
	if !HasPrefix(id, Action) || HasPrefix(id, Event) {
		t.Fatalf("HasPrefix wrong for %q", id)
	}
}

func TestMonotonicWithinOneMillisecond(t *testing.T) {
	g := NewGenerator(clock.NewFake(time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)))
	var prev string
	for i := 0; i < 10000; i++ {
		id := g.New(Event)
		if id <= prev {
			t.Fatalf("id %d %q not after %q", i, id, prev)
		}
		prev = id
	}
}

func TestSortsByTime(t *testing.T) {
	f := clock.NewFake(time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC))
	g := NewGenerator(f)
	a := g.New(Action)
	f.Advance(time.Millisecond)
	b := g.New(Action)
	if a >= b {
		t.Fatalf("%q should sort before %q", a, b)
	}
}

func TestConcurrentUnique(t *testing.T) {
	const workers, each = 8, 2000
	var mu sync.Mutex
	seen := make(map[string]bool, workers*each)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := make([]string, 0, each)
			for i := 0; i < each; i++ {
				local = append(local, New(Token))
			}
			mu.Lock()
			defer mu.Unlock()
			for _, id := range local {
				if seen[id] {
					t.Errorf("duplicate id %q", id)
				}
				seen[id] = true
			}
		}()
	}
	wg.Wait()
	if len(seen) != workers*each {
		t.Fatalf("got %d unique ids, want %d", len(seen), workers*each)
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	for _, id := range []string{
		"",
		"act",
		"_01J9ZQ3XK2V5N8M7P6R4T3W2Y1",
		"act_",
		"act_not-a-ulid",
		"act_01J9ZQ3XK2V5N8M7P6R4T3W2Y", // one character short
	} {
		if _, _, err := Parse(id); err == nil {
			t.Errorf("Parse(%q) succeeded", id)
		}
	}
}
