package display

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/internal/evdev"
	"github.com/davitizhgenti/hostd/internal/evdev/evdevtest"
)

func TestControllerEvents(t *testing.T) {
	r := newDisplayRig(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := r.e.Subscribe(ctx, "controller.*")
	time.Sleep(50 * time.Millisecond) // the watcher is in place

	evdevtest.AddInput(t, r.sys, "event9", "Microsoft X-Box 360 pad", "045e", "028e", evdevtest.GamepadKeys)
	next := func() (string, evdev.Controller) {
		t.Helper()
		for range 200 {
			select {
			case ev := <-events:
				var c evdev.Controller
				_ = json.Unmarshal(ev.Data, &c)
				return ev.Type, c
			case <-time.After(10 * time.Millisecond):
				r.clock.Advance(300 * time.Millisecond)
			}
		}
		t.Fatal("no controller event")
		return "", evdev.Controller{}
	}
	if typ, c := next(); typ != EventControllerConnected || c.Profile != "xbox" || c.Device != "/dev/input/event9" {
		t.Fatalf("%s %+v", typ, c)
	}
	list, _ := r.m.Read(context.Background(), "controllers", nil)
	if cs := list.([]evdev.Controller); len(cs) != 1 || !reflect.DeepEqual(cs[0].Buttons, []string{"guide", "start", "select"}) {
		t.Fatalf("read %+v", list)
	}
	_ = os.Remove(filepath.Join(r.sys, "dev/input/event9"))
	if typ, _ := next(); typ != EventControllerDisconnected {
		t.Fatalf("got %s", typ)
	}
}
