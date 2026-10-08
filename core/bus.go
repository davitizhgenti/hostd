package core

import (
	"context"
	"sync"

	"github.com/davitizhgenti/hostd/sdk"
)

// EventLagged is the last event a subscriber receives before the bus drops
// it for falling too far behind.
const EventLagged = sdk.EventLagged

// Bus delivers events to subscribers. Publishing never blocks: each
// subscriber has a bounded buffer, and one that falls behind is dropped
// (its channel gets a final bus.lagged event and is closed) rather than
// slowing down everyone else.
type Bus struct {
	mu     sync.Mutex
	subs   map[*subscription]struct{}
	buffer int
}

type subscription struct {
	filter string
	ch     chan sdk.Event
}

// NewBus returns a bus whose subscribers may fall up to buffer events
// behind.
func NewBus(buffer int) *Bus {
	if buffer < 1 {
		buffer = 1
	}
	return &Bus{subs: map[*subscription]struct{}{}, buffer: buffer}
}

// Subscribe returns a channel of events whose type matches filter (see
// sdk.MatchType). It is closed when ctx is done or the subscriber lags.
func (b *Bus) Subscribe(ctx context.Context, filter string) <-chan sdk.Event {
	// One slot beyond the buffer is kept free for the bus.lagged notice.
	s := &subscription{filter: filter, ch: make(chan sdk.Event, b.buffer+1)}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()

	go func() {
		<-ctx.Done()
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, ok := b.subs[s]; ok {
			delete(b.subs, s)
			close(s.ch)
		}
	}()
	return s.ch
}

// Publish delivers e to every matching subscriber without blocking.
func (b *Bus) Publish(e sdk.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for s := range b.subs {
		if !sdk.MatchType(s.filter, e.Type) {
			continue
		}
		// Only Publish sends, and only under b.mu, so this length check
		// cannot race with another send; readers only make more room.
		if len(s.ch) >= b.buffer {
			s.ch <- sdk.Event{Type: EventLagged, Time: e.Time}
			delete(b.subs, s)
			close(s.ch)
			continue
		}
		s.ch <- e
	}
}

// Subscribers reports how many subscriptions are active.
func (b *Bus) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
