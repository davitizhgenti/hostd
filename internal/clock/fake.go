package clock

import (
	"sort"
	"sync"
	"time"
)

// Fake is a Clock that only moves when Advance or Set is called.
//
// Timers and tickers fire in deadline order, and Now reports each deadline
// while its waiter fires, so code observing the clock from a callback sees the
// time it was scheduled for. Channel sends never block: like real timers, a
// tick is dropped if the previous one was not received.
type Fake struct {
	mu      sync.Mutex
	cond    *sync.Cond
	now     time.Time
	seq     uint64
	waiters []*waiter
}

type waiter struct {
	fake     *Fake
	seq      uint64 // insertion order, to keep equal deadlines stable
	deadline time.Time
	period   time.Duration // > 0 for tickers
	ch       chan time.Time
	fn       func()
	active   bool
}

// NewFake returns a fake clock starting at start.
func NewFake(start time.Time) *Fake {
	f := &Fake{now: start}
	f.cond = sync.NewCond(&f.mu)
	return f
}

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *Fake) Since(t time.Time) time.Duration { return f.Now().Sub(t) }

func (f *Fake) After(d time.Duration) <-chan time.Time { return f.NewTimer(d).C() }

func (f *Fake) NewTimer(d time.Duration) Timer {
	return f.add(d, 0, make(chan time.Time, 1), nil)
}

func (f *Fake) AfterFunc(d time.Duration, fn func()) Timer {
	return f.add(d, 0, nil, fn)
}

func (f *Fake) NewTicker(d time.Duration) Ticker {
	if d <= 0 {
		panic("clock: non-positive interval for NewTicker")
	}
	return &fakeTicker{f.add(d, d, make(chan time.Time, 1), nil)}
}

func (f *Fake) add(d, period time.Duration, ch chan time.Time, fn func()) *waiter {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := &waiter{fake: f, period: period, ch: ch, fn: fn}
	f.schedule(w, d)
	return w
}

// schedule (re)inserts w. Caller holds f.mu.
func (f *Fake) schedule(w *waiter, d time.Duration) {
	f.seq++
	w.seq = f.seq
	w.deadline = f.now.Add(d)
	w.active = true
	f.waiters = append(f.waiters, w)
	f.cond.Broadcast()
}

// remove unschedules w and reports whether it was active. Caller holds f.mu.
func (f *Fake) remove(w *waiter) bool {
	if !w.active {
		return false
	}
	w.active = false
	for i, x := range f.waiters {
		if x == w {
			f.waiters = append(f.waiters[:i], f.waiters[i+1:]...)
			break
		}
	}
	return true
}

// Advance moves the clock forward by d, firing every waiter whose deadline
// is reached, in order.
func (f *Fake) Advance(d time.Duration) {
	if d < 0 {
		panic("clock: negative Advance")
	}
	f.Set(f.Now().Add(d))
}

// Set moves the clock to t, which must not be before Now.
func (f *Fake) Set(t time.Time) {
	for {
		f.mu.Lock()
		if t.Before(f.now) {
			f.mu.Unlock()
			panic("clock: Set moves time backwards")
		}
		w := f.next(t)
		if w == nil {
			f.now = t
			f.mu.Unlock()
			return
		}
		f.now = w.deadline
		f.remove(w)
		if w.period > 0 {
			f.schedule(w, w.period)
		}
		now, ch, fn := f.now, w.ch, w.fn
		f.mu.Unlock()

		if ch != nil {
			select {
			case ch <- now:
			default:
			}
		}
		if fn != nil {
			fn()
		}
	}
}

// next returns the earliest waiter due at or before t. Caller holds f.mu.
func (f *Fake) next(t time.Time) *waiter {
	if len(f.waiters) == 0 {
		return nil
	}
	sort.Slice(f.waiters, func(i, j int) bool {
		a, b := f.waiters[i], f.waiters[j]
		if !a.deadline.Equal(b.deadline) {
			return a.deadline.Before(b.deadline)
		}
		return a.seq < b.seq
	})
	if w := f.waiters[0]; !w.deadline.After(t) {
		return w
	}
	return nil
}

// Waiters reports how many timers and tickers are scheduled.
func (f *Fake) Waiters() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.waiters)
}

// BlockUntil waits until at least n timers or tickers are scheduled. Tests
// use it to know a goroutine has reached its wait before calling Advance.
func (f *Fake) BlockUntil(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for len(f.waiters) < n {
		f.cond.Wait()
	}
}

func (w *waiter) C() <-chan time.Time { return w.ch }

func (w *waiter) Stop() bool {
	w.fake.mu.Lock()
	defer w.fake.mu.Unlock()
	return w.fake.remove(w)
}

func (w *waiter) Reset(d time.Duration) bool {
	w.fake.mu.Lock()
	defer w.fake.mu.Unlock()
	was := w.fake.remove(w)
	w.fake.schedule(w, d)
	return was
}

type fakeTicker struct{ w *waiter }

func (t *fakeTicker) C() <-chan time.Time { return t.w.ch }
func (t *fakeTicker) Stop()               { t.w.Stop() }

func (t *fakeTicker) Reset(d time.Duration) {
	if d <= 0 {
		panic("clock: non-positive interval for Ticker.Reset")
	}
	t.w.fake.mu.Lock()
	defer t.w.fake.mu.Unlock()
	t.w.fake.remove(t.w)
	t.w.period = d
	t.w.fake.schedule(t.w, d)
}
