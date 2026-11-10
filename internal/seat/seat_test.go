package seat

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/internal/evdev"
)

type fakeInput struct {
	mu      sync.Mutex
	running int32
	starts  int
	grabs   []string
}

func (f *fakeInput) Watch(ctx context.Context, fn func(evdev.InputEvent)) error {
	f.mu.Lock()
	f.starts++
	f.mu.Unlock()
	atomic.StoreInt32(&f.running, 1)
	defer atomic.StoreInt32(&f.running, 0)
	<-ctx.Done()
	return nil
}
func (f *fakeInput) Grab(d string, on bool) error {
	f.grabs = append(f.grabs, d)
	return nil
}
func (f *fakeInput) Devices() map[string]int { return map[string]int{"pad": 1} }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", what)
}

func TestGateReadsOnlyWhileTheSeatIsHeld(t *testing.T) {
	in := &fakeInput{}
	var active atomic.Bool
	active.Store(true)
	g := &Gate{Inner: in, Every: 10 * time.Millisecond, Active: func(context.Context) bool { return active.Load() }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = g.Watch(ctx, func(evdev.InputEvent) {}) }()
	running := func() bool { return atomic.LoadInt32(&in.running) == 1 }
	waitFor(t, "reading while held", running)
	active.Store(false)
	waitFor(t, "stops when the seat goes", func() bool { return !running() })
	active.Store(true)
	waitFor(t, "reads again when it is back", running)
	if in.starts != 2 {
		t.Errorf("starts = %d", in.starts)
	}
	cancel()
	<-done
	if running() {
		t.Error("still reading after the context ended")
	}
	if err := g.Grab("pad", true); err != nil || len(in.grabs) != 1 {
		t.Errorf("grab: %v %v", err, in.grabs)
	}
	if g.Devices()["pad"] != 1 {
		t.Error("devices do not pass through")
	}
	if (&Gate{Inner: nilInput{}}).Devices() != nil || (&Gate{Inner: nilInput{}}).Grab("", true) != nil {
		t.Error("an Input without them")
	}
}

type nilInput struct{}

func (nilInput) Watch(context.Context, func(evdev.InputEvent)) error { return nil }

func TestActiveAndHolderWithoutLogind(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no loginctl
	if !Active(context.Background(), "nobody") {
		t.Error("unknown must be active")
	}
	if Holder(context.Background()) != "" {
		t.Error("no holder without logind")
	}
}
