package clock

import (
	"sync"
	"testing"
	"time"
)

var epoch = time.Date(2026, 10, 4, 19, 0, 0, 0, time.UTC)

func received(ch <-chan time.Time) (time.Time, bool) {
	select {
	case t := <-ch:
		return t, true
	default:
		return time.Time{}, false
	}
}

func TestFakeNowAndAdvance(t *testing.T) {
	f := NewFake(epoch)
	f.Advance(90 * time.Second)
	if got, want := f.Now(), epoch.Add(90*time.Second); !got.Equal(want) {
		t.Fatalf("Now = %v, want %v", got, want)
	}
	if got := f.Since(epoch); got != 90*time.Second {
		t.Fatalf("Since = %v", got)
	}
}

func TestFakeTimerFiresAtDeadline(t *testing.T) {
	f := NewFake(epoch)
	tm := f.NewTimer(time.Minute)

	f.Advance(59 * time.Second)
	if _, ok := received(tm.C()); ok {
		t.Fatal("timer fired early")
	}
	f.Advance(time.Second)
	got, ok := received(tm.C())
	if !ok {
		t.Fatal("timer did not fire")
	}
	if want := epoch.Add(time.Minute); !got.Equal(want) {
		t.Fatalf("fired with %v, want %v", got, want)
	}
	if f.Waiters() != 0 {
		t.Fatalf("Waiters = %d after firing", f.Waiters())
	}
}

func TestFakeTimerStopAndReset(t *testing.T) {
	f := NewFake(epoch)
	tm := f.NewTimer(time.Minute)
	if !tm.Stop() {
		t.Fatal("Stop on active timer returned false")
	}
	if tm.Stop() {
		t.Fatal("second Stop returned true")
	}
	f.Advance(2 * time.Minute)
	if _, ok := received(tm.C()); ok {
		t.Fatal("stopped timer fired")
	}

	if tm.Reset(time.Second) {
		t.Fatal("Reset of stopped timer returned true")
	}
	f.Advance(time.Second)
	if _, ok := received(tm.C()); !ok {
		t.Fatal("reset timer did not fire")
	}
}

func TestFakeFiresInDeadlineOrder(t *testing.T) {
	f := NewFake(epoch)
	var order []int
	var seen []time.Time
	for _, tc := range []struct {
		id int
		d  time.Duration
	}{{3, 3 * time.Second}, {1, time.Second}, {2, 2 * time.Second}, {4, 2 * time.Second}} {
		f.AfterFunc(tc.d, func() {
			order = append(order, tc.id)
			seen = append(seen, f.Now())
		})
	}
	f.Advance(10 * time.Second)

	want := []int{1, 2, 4, 3} // equal deadlines keep insertion order
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
	if !seen[0].Equal(epoch.Add(time.Second)) || !seen[3].Equal(epoch.Add(3*time.Second)) {
		t.Fatalf("Now inside callbacks = %v", seen)
	}
	if !f.Now().Equal(epoch.Add(10 * time.Second)) {
		t.Fatalf("final Now = %v", f.Now())
	}
}

func TestFakeAfterFuncCanScheduleMore(t *testing.T) {
	f := NewFake(epoch)
	var fired []time.Time
	var tick func()
	tick = func() {
		fired = append(fired, f.Now())
		if len(fired) < 3 {
			f.AfterFunc(time.Second, tick)
		}
	}
	f.AfterFunc(time.Second, tick)
	f.Advance(5 * time.Second)
	if len(fired) != 3 || !fired[2].Equal(epoch.Add(3*time.Second)) {
		t.Fatalf("fired = %v", fired)
	}
}

func TestFakeTicker(t *testing.T) {
	f := NewFake(epoch)
	tk := f.NewTicker(10 * time.Second)

	for i := 1; i <= 3; i++ {
		f.Advance(10 * time.Second)
		got, ok := received(tk.C())
		if !ok || !got.Equal(epoch.Add(time.Duration(i)*10*time.Second)) {
			t.Fatalf("tick %d = %v, %v", i, got, ok)
		}
	}

	// Unread ticks are dropped, not queued.
	f.Advance(time.Minute)
	if _, ok := received(tk.C()); !ok {
		t.Fatal("no tick after long advance")
	}
	if _, ok := received(tk.C()); ok {
		t.Fatal("ticks queued up instead of being dropped")
	}

	tk.Reset(time.Second)
	f.Advance(time.Second)
	if _, ok := received(tk.C()); !ok {
		t.Fatal("no tick after Reset")
	}

	tk.Stop()
	f.Advance(time.Minute)
	if _, ok := received(tk.C()); ok {
		t.Fatal("stopped ticker ticked")
	}
}

func TestFakeBlockUntil(t *testing.T) {
	f := NewFake(epoch)
	var wg sync.WaitGroup
	wg.Add(1)
	got := make(chan time.Time, 1)
	go func() {
		defer wg.Done()
		got <- <-f.After(time.Minute)
	}()

	f.BlockUntil(1)
	f.Advance(time.Minute)
	wg.Wait()
	if v := <-got; !v.Equal(epoch.Add(time.Minute)) {
		t.Fatalf("goroutine woke with %v", v)
	}
}

func TestFakeRejectsBackwards(t *testing.T) {
	f := NewFake(epoch)
	defer func() {
		if recover() == nil {
			t.Fatal("Set into the past did not panic")
		}
	}()
	f.Set(epoch.Add(-time.Second))
}

func TestRealClockSatisfiesInterface(t *testing.T) {
	c := Real()
	if c.Since(c.Now()) < 0 {
		t.Fatal("real clock went backwards")
	}
	tm := c.NewTimer(time.Millisecond)
	<-tm.C()
	done := make(chan struct{})
	c.AfterFunc(time.Millisecond, func() { close(done) })
	<-done
}
