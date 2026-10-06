package core

import (
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/internal/clock"
)

func TestRuleLimiter(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 10, 7, 20, 0, 0, 0, time.UTC))
	l := NewRuleLimiter(c)

	// 10 fires within a minute are fine.
	for i := 0; i < 10; i++ {
		if ok, _, _ := l.Allow("gaming audio"); !ok {
			t.Fatalf("fire %d refused", i+1)
		}
		c.Advance(5 * time.Second)
	}
	// The 11th within the minute trips the limit, once.
	ok, until, tripped := l.Allow("gaming audio")
	if ok || !tripped {
		t.Fatalf("11th fire: ok=%v tripped=%v", ok, tripped)
	}
	if want := c.Now().Add(10 * time.Minute); !until.Equal(want) {
		t.Fatalf("paused until %v, want %v", until, want)
	}
	// Further fires while paused are refused without tripping again.
	c.Advance(time.Minute)
	if ok, u, tripped := l.Allow("gaming audio"); ok || tripped || !u.Equal(until) {
		t.Fatalf("while paused: ok=%v tripped=%v until=%v", ok, tripped, u)
	}
	if p := l.Paused(); !p["gaming audio"].Equal(until) {
		t.Fatalf("Paused() = %v", p)
	}
	// Other rules are unaffected.
	if ok, _, _ := l.Allow("screen off"); !ok {
		t.Fatal("unrelated rule refused")
	}
	// After the pause it runs again, with a fresh count.
	c.Set(until)
	if ok, _, _ := l.Allow("gaming audio"); !ok {
		t.Fatal("refused after the pause ended")
	}
	if len(l.Paused()) != 0 {
		t.Fatalf("still paused: %v", l.Paused())
	}
}

func TestRuleLimiterSlidingWindow(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 10, 7, 20, 0, 0, 0, time.UTC))
	l := NewRuleLimiter(c)
	// One fire every 6.1s never has more than 10 in any minute.
	for i := 0; i < 100; i++ {
		if ok, _, _ := l.Allow("steady"); !ok {
			t.Fatalf("fire %d refused although the rate is under the limit", i+1)
		}
		c.Advance(6100 * time.Millisecond)
	}
}
