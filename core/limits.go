package core

import (
	"sync"
	"time"

	"github.com/davitizhgenti/hostd/internal/clock"
)

// RuleLimiter pauses rules that fire too often: more than Max times within
// Window pauses the rule for Pause. The automation module asks it before
// running a rule; the core owns it so the limits are the same everywhere.
type RuleLimiter struct {
	clock  clock.Clock
	Max    int
	Window time.Duration
	Pause  time.Duration

	mu     sync.Mutex
	fires  map[string][]time.Time
	paused map[string]time.Time // rule -> paused until
}

// NewRuleLimiter returns a limiter with the design's defaults: more than 10
// fires in one minute pauses a rule for 10 minutes.
func NewRuleLimiter(c clock.Clock) *RuleLimiter {
	return &RuleLimiter{
		clock: c, Max: 10, Window: time.Minute, Pause: 10 * time.Minute,
		fires: map[string][]time.Time{}, paused: map[string]time.Time{},
	}
}

// Allow records a fire of rule and reports whether it may run. When this
// fire trips the limit, or the rule is already paused, it returns false and
// the time the pause ends; tripped is true only for the fire that caused
// the pause, so the caller emits one loop_detected event, not one per fire.
func (l *RuleLimiter) Allow(rule string) (ok bool, until time.Time, tripped bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	if u, p := l.paused[rule]; p {
		if now.Before(u) {
			return false, u, false
		}
		delete(l.paused, rule)
	}
	cutoff := now.Add(-l.Window)
	recent := l.fires[rule][:0]
	for _, t := range l.fires[rule] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	recent = append(recent, now)
	if len(recent) > l.Max {
		u := now.Add(l.Pause)
		l.paused[rule] = u
		delete(l.fires, rule)
		return false, u, true
	}
	l.fires[rule] = recent
	return true, time.Time{}, false
}

// Paused returns the rules currently paused and until when.
func (l *RuleLimiter) Paused() map[string]time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	out := map[string]time.Time{}
	for r, u := range l.paused {
		if now.Before(u) {
			out[r] = u
		}
	}
	return out
}
