package display

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/internal/evdev"
	"github.com/davitizhgenti/hostd/internal/evdev/evdevtest"
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

// Calibrating asks the input, and its progress reaches the menu with the
// corner to aim at, the remote's name and its colour.
func TestCalibratingARemote(t *testing.T) {
	r := newDisplayRig(t, true)
	events := r.e.Subscribe(t.Context(), EventCalibrate)
	const wii = "057e:0306-hidraw3"
	r.input.send(t, evdev.InputEvent{Device: wii, Player: 1, Family: "wii", Presence: "here"})
	do := func(args map[string]any) error {
		_, err := r.m.Handle(context.Background(), sdk.Action{ID: "act_1", Type: "device.calibrate", Args: sdk.MustJSON(args)})
		return err
	}
	if err := do(map[string]any{"id": wii}); err != nil {
		t.Fatal(err)
	}
	if err := do(map[string]any{"id": wii, "reset": true}); err != nil {
		t.Fatal(err)
	}
	r.input.mu.Lock()
	asked := append([]string(nil), r.input.calibrated...)
	r.input.mu.Unlock()
	if len(asked) != 2 || asked[0] != wii+" reset=false" || asked[1] != wii+" reset=true" {
		t.Fatalf("asked: %v", asked)
	}
	if err := do(map[string]any{"id": "nothing"}); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("unknown: %v", err)
	}
	evdevtest.AddInput(t, r.sys, "event0", "Keyboard A", "046d", "c31c", keyboardKeys)
	for i := 0; i < 400 && r.m.deviceByID(evdev.DeviceID(evdev.DeviceInfoFor("046d", "c31c"), "/dev/input/event0")) == nil; i++ {
		r.clock.Advance(300 * time.Millisecond)
		time.Sleep(10 * time.Millisecond)
	}
	if err := do(map[string]any{"id": evdev.DeviceID(evdev.DeviceInfoFor("046d", "c31c"), "/dev/input/event0")}); sdk.CodeOf(err) != sdk.CodeInvalidArgs {
		t.Fatalf("a keyboard has no pointer: %v", err)
	}

	r.input.send(t, evdev.InputEvent{Device: wii, Player: 1, Family: "wii", Calibrate: &evdev.CalibrateEvent{State: "aim", Step: 1}})
	r.input.send(t, evdev.InputEvent{Device: wii, Player: 1, Family: "wii",
		Calibrate: &evdev.CalibrateEvent{State: "done", Fit: &evdev.Calibration{W: 4, H: 2.5}}})
	var got [2]map[string]any
	for i := range got {
		select {
		case ev := <-events:
			_ = json.Unmarshal(ev.Data, &got[i])
		case <-time.After(5 * time.Second):
			t.Fatal("no display.calibrate")
		}
	}
	if got[0]["corner"] != "top right" || got[0]["name"] != "Wii Remote 1" || got[0]["colour"] == "" || got[1]["state"] != "done" || got[1]["calibration"] == nil {
		t.Fatalf("events: %v", got)
	}
}

// Through the engine, as the menu and hostctl send them (validated first,
// which looks for a window when an action is not known to name none): the
// device actions are not about windows.
func TestDeviceActionsGoThroughTheEngine(t *testing.T) {
	r := newDisplayRig(t, true)
	const wii = "057e:0306-hidraw3"
	r.input.send(t, evdev.InputEvent{Device: wii, Player: 1, Family: "wii", Presence: "here"})
	for typ, args := range map[string]string{
		"device.colour":    `{"id":"` + wii + `","colour":"green"}`,
		"device.rename":    `{"id":"` + wii + `","name":"Mia"}`,
		"device.calibrate": `{"id":"` + wii + `"}`,
	} {
		if _, err := r.submit(t, typ, args, sdk.SourceLocal, "display"); err != nil {
			t.Errorf("%s: %v", typ, err)
		}
	}
	if d := r.m.deviceByID(wii); d.Label != "Mia" || d.Colour != "green" {
		t.Fatalf("after: %+v", d)
	}
	if _, err := r.submit(t, "device.colour", `{"id":"nothing","colour":"green"}`, sdk.SourceLocal, "display"); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Errorf("unknown device: %v", err)
	}
}
