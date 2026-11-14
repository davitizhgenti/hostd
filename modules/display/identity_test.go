package display

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/internal/evdev"
	"github.com/davitizhgenti/hostd/sdk"
)

// A Wii Remote has no input node: it is a device when it is heard from,
// has a name and a colour of its own, and is announced when it comes and
// goes, without counting as someone using the screen.
func TestAWiiRemoteIsADeviceWithAName(t *testing.T) {
	r := newDisplayRig(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := r.e.Subscribe(ctx, "device.*")
	time.Sleep(50 * time.Millisecond)

	const a, b = "057e:0306-hidraw3", "057e:0306-hidraw4"
	r.input.send(t, evdev.InputEvent{Device: a, Player: 1, Family: "wii", Presence: "here"})
	r.input.send(t, evdev.InputEvent{Device: b, Player: 2, Family: "wii", Presence: "here"})
	for _, want := range []string{a, b} {
		select {
		case ev := <-events:
			var d DeviceView
			_ = json.Unmarshal(ev.Data, &d)
			if ev.Type != EventDeviceConnected || d.ID != want || !d.Pointer {
				t.Fatalf("event %s %s", ev.Type, ev.Data)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no device.connected")
		}
	}
	if r.m.presence.Present() {
		t.Error("a remote coming is not someone at the screen")
	}
	got := map[string]DeviceView{}
	for _, d := range r.m.readDevices() {
		got[d.ID] = d
	}
	if got[a].Label != "Wii Remote 1" || got[b].Label != "Wii Remote 2" || got[a].Colour == got[b].Colour || got[a].Colour == "" {
		t.Fatalf("labels and colours: %+v %+v", got[a], got[b])
	}

	do := func(typ string, args map[string]any) (sdk.Result, error) {
		return r.m.Handle(context.Background(), sdk.Action{ID: "act_1", Type: typ, Args: sdk.MustJSON(args)})
	}
	if _, err := do("device.rename", map[string]any{"id": b, "name": "Mia"}); err != nil {
		t.Fatal(err)
	}
	if _, err := do("device.colour", map[string]any{"id": b, "colour": "violet"}); err != nil {
		t.Fatal(err)
	}
	if _, err := do("device.colour", map[string]any{"id": b, "colour": "mauve"}); sdk.CodeOf(err) != sdk.CodeInvalidArgs {
		t.Fatalf("not a colour: %v", err)
	}
	if _, err := do("device.rename", map[string]any{"id": "nothing", "name": "x"}); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("unknown device: %v", err)
	}
	if d := r.m.deviceByID(b); d.Label != "Mia" || d.Colour != "violet" {
		t.Fatalf("renamed: %+v", d)
	}

	r.input.send(t, evdev.InputEvent{Device: a, Player: 1, Family: "wii", Presence: "gone"})
	for range 20 {
		select {
		case ev := <-events:
			if ev.Type == EventDeviceDisconnected {
				if r.m.deviceByID(a) != nil || r.m.deviceByID(b) == nil {
					t.Fatalf("after it went: %+v", r.m.readDevices())
				}
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no device.disconnected")
		}
	}
}

// Every pointer reaches the menu, with its device, its name and colour: two
// remotes at once do not share one.
func TestEveryPointerReachesTheMenu(t *testing.T) {
	r := newDisplayRig(t, true)
	pointers := r.e.Subscribe(t.Context(), EventPointer)
	r.m.mu.Lock()
	r.m.menuFront = true
	r.m.mu.Unlock()
	const a, b = "057e:0306-hidraw3", "057e:0306-hidraw4"
	r.input.send(t, evdev.InputEvent{Device: a, Player: 1, Family: "wii", Pointer: &evdev.Pointer{X: .2, Y: .3, Visible: true}})
	r.input.send(t, evdev.InputEvent{Device: b, Player: 2, Family: "wii", Pointer: &evdev.Pointer{X: .8, Y: .6, Visible: true, Down: true}})
	var first, second map[string]any
	for _, into := range []*map[string]any{&first, &second} {
		select {
		case ev := <-pointers:
			_ = json.Unmarshal(ev.Data, into)
		case <-time.After(5 * time.Second):
			t.Fatal("a pointer did not reach the menu")
		}
	}
	if first["device"] != a || second["device"] != b || first["colour"] == second["colour"] || first["name"] != "Wii Remote 1" || second["down"] != true {
		t.Fatalf("pointers: %v %v", first, second)
	}
}
