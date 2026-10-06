package core

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/sdk"
)

func ev(typ string) sdk.Event { return sdk.Event{Type: typ} }

// drain reads everything currently buffered without blocking.
func drain(ch <-chan sdk.Event) []string {
	var got []string
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return append(got, "<closed>")
			}
			got = append(got, e.Type)
		default:
			return got
		}
	}
}

func TestBusFiltersAndFansOut(t *testing.T) {
	b := NewBus(16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	all := b.Subscribe(ctx, "*")
	inst := b.Subscribe(ctx, "instance.*")
	vol := b.Subscribe(ctx, "audio.volume.changed")

	for _, typ := range []string{"instance.started", "audio.volume.changed", "window.focused", "instance.exited"} {
		b.Publish(ev(typ))
	}
	check := func(name string, ch <-chan sdk.Event, want ...string) {
		t.Helper()
		got := drain(ch)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s got %v, want %v", name, got, want)
		}
	}
	check("*", all, "instance.started", "audio.volume.changed", "window.focused", "instance.exited")
	check("instance.*", inst, "instance.started", "instance.exited")
	check("exact", vol, "audio.volume.changed")
}

func TestBusSlowSubscriberDoesNotBlockOthers(t *testing.T) {
	const buffer = 4
	b := NewBus(buffer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	slow := b.Subscribe(ctx, "*") // never read until the end
	fast := b.Subscribe(ctx, "*")

	// In lockstep: each Publish must return, and fast must get the event,
	// even after slow's buffer is full and it has been dropped.
	for i := 0; i < 100; i++ {
		want := fmt.Sprintf("test.e%d", i)
		b.Publish(ev(want))
		select {
		case e := <-fast:
			if e.Type != want {
				t.Fatalf("fast got %s, want %s", e.Type, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("fast did not get %s", want)
		}
	}

	// slow got its buffer's worth, then the lagged notice, then closed.
	got := drain(slow)
	want := []string{"test.e0", "test.e1", "test.e2", "test.e3", EventLagged, "<closed>"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("slow subscriber got %v, want %v", got, want)
	}
	if n := b.Subscribers(); n != 1 {
		t.Fatalf("Subscribers = %d after dropping the slow one", n)
	}
}

func TestBusUnsubscribeOnCancel(t *testing.T) {
	b := NewBus(4)
	ctx, cancel := context.WithCancel(context.Background())
	ch := b.Subscribe(ctx, "*")
	cancel()
	// The channel closes once the bus notices the cancellation.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("received an event after cancel")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("channel not closed after cancel")
	}
	if n := b.Subscribers(); n != 0 {
		t.Fatalf("Subscribers = %d after cancel", n)
	}
	b.Publish(ev("test.after")) // must not panic on the closed channel
}

func TestBusCancelAfterLagIsSafe(t *testing.T) {
	b := NewBus(1)
	ctx, cancel := context.WithCancel(context.Background())
	ch := b.Subscribe(ctx, "*")
	b.Publish(ev("test.a"))
	b.Publish(ev("test.b")) // lags: lagged notice, closed
	cancel()                // must not close the channel a second time
	got := drain(ch)
	if fmt.Sprint(got) != fmt.Sprint([]string{"test.a", EventLagged, "<closed>"}) {
		t.Fatalf("got %v", got)
	}
	if n := b.Subscribers(); n != 0 {
		t.Fatalf("Subscribers = %d", n)
	}
}

func TestBusConcurrentPublishSubscribe(t *testing.T) {
	b := NewBus(1024)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			sub, subCancel := context.WithCancel(ctx)
			ch := b.Subscribe(sub, "test.*")
			for j := 0; j < 50; j++ {
				select {
				case <-ch:
				default:
				}
			}
			subCancel()
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				b.Publish(ev("test.x"))
			}
		}()
	}
	wg.Wait()
	cancel() // goleak (TestMain) checks the cleanup goroutines exit
}
