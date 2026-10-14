// Package presence tracks whether someone is using the screen.
package presence

import (
	"context"
	"sync"
	"time"

	"github.com/davitizhgenti/hostd/internal/clock"
)

// Tracker tracks whether someone is using the screen: any keyboard, mouse
// or controller input in the last idleAfter makes them present.
type Tracker struct {
	clock     clock.Clock
	idleAfter time.Duration
	changed   func(present bool) // called on every change, from run's goroutine

	mu     sync.Mutex
	last   time.Time
	active bool
	wake   chan struct{} // signalled by touch while idle
}

// New returns a Tracker: changed is called on every change.
func New(c clock.Clock, idleAfter time.Duration, changed func(bool)) *Tracker {
	return &Tracker{clock: c, idleAfter: idleAfter, changed: changed, wake: make(chan struct{}, 1)}
}

// Touch records input. It is called for every read from every device, so
// it stays cheap: the run loop only hears about the first one after idle.
func (p *Tracker) Touch() {
	p.mu.Lock()
	p.last = p.clock.Now()
	idle := !p.active
	p.mu.Unlock()
	if idle {
		select {
		case p.wake <- struct{}{}:
		default:
		}
	}
}

// Present reports whether someone used the screen recently.
func (p *Tracker) Present() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.active
}

// LastInput returns when the last input was (zero if never).
func (p *Tracker) LastInput() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

func (p *Tracker) Run(ctx context.Context) {
	var timer clock.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		var expired <-chan time.Time
		if timer != nil {
			expired = timer.C()
		}
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
			p.mu.Lock()
			was := p.active
			p.active = true
			p.mu.Unlock()
			if !was {
				timer = p.clock.NewTimer(p.idleAfter)
				p.changed(true)
			}
		case <-expired:
			p.mu.Lock()
			left := p.idleAfter - p.clock.Since(p.last)
			if left <= 0 {
				p.active = false
			}
			p.mu.Unlock()
			if left > 0 {
				timer = p.clock.NewTimer(left)
				continue
			}
			timer = nil
			p.changed(false)
		}
	}
}
