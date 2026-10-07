package api

import (
	"sync"

	"github.com/davitizhgenti/hostd/sdk"
)

// ring keeps the most recent events so a client that reconnects to the
// event stream with ?since=<id> misses nothing in that window.
type ring struct {
	mu     sync.Mutex
	events []sdk.Event
	max    int
}

func newRing(max int) *ring { return &ring{max: max} }

func (r *ring) add(e sdk.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	if over := len(r.events) - r.max; over > 0 {
		r.events = append(r.events[:0], r.events[over:]...)
	}
}

// since returns the events after the one with ID id. found is false when
// id is no longer in the window (or never was); then every kept event is
// returned and the client may have missed some.
func (r *ring) since(id string) (events []sdk.Event, found bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, e := range r.events {
		if e.ID == id {
			return append([]sdk.Event(nil), r.events[i+1:]...), true
		}
	}
	return append([]sdk.Event(nil), r.events...), false
}
