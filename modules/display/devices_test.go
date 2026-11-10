package display

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/internal/evdev"
	"github.com/davitizhgenti/hostd/internal/evdev/evdevtest"
	"github.com/davitizhgenti/hostd/sdk"
)

const keyboardKeys = "200000040000000" // KEY_A and KEY_SPACE

// Two keyboards and two controllers are four devices with IDs of their own;
// each is announced when it comes and goes, and shows when it was last used.
func TestDevicesAreToldApart(t *testing.T) {
	r := newDisplayRig(t, true)
	players := &evdev.Players{}
	r.m.opts.Players = players
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := r.e.Subscribe(ctx, "device.*")
	time.Sleep(50 * time.Millisecond)

	evdevtest.AddInput(t, r.sys, "event0", "Keyboard A", "046d", "c31c", keyboardKeys)
	evdevtest.AddInput(t, r.sys, "event1", "Keyboard B", "046d", "c31c", keyboardKeys)
	evdevtest.AddInput(t, r.sys, "event8", "Xbox pad", "045e", "028e", evdevtest.GamepadKeys)
	evdevtest.AddInput(t, r.sys, "event9", "Xbox pad", "045e", "028e", evdevtest.GamepadKeys)
	connected := map[string]bool{}
	for i := 0; i < 400 && len(connected) < 4; i++ {
		select {
		case ev := <-events:
			var d evdev.Device
			_ = json.Unmarshal(ev.Data, &d)
			if ev.Type == EventDeviceConnected {
				connected[d.Name+" "+d.Device] = true
			}
		case <-time.After(10 * time.Millisecond):
			r.clock.Advance(300 * time.Millisecond)
		}
	}
	if len(connected) != 4 {
		t.Fatalf("announced %v", connected)
	}

	// Input from one keyboard is that keyboard's: the other has no time.
	kbA := evdev.DeviceID(evdev.DeviceInfoFor("046d", "c31c"), "/dev/input/event0")
	r.input.send(t, evdev.InputEvent{Device: kbA})
	devs := r.m.readDevices()
	if len(devs) != 4 {
		t.Fatalf("devices = %+v", devs)
	}
	used := 0
	for _, d := range devs {
		if d.LastInput != nil {
			used++
			if d.ID != kbA || d.Kind != evdev.KindKeyboard {
				t.Errorf("%s (%s) shows input it did not make", d.Name, d.ID)
			}
		}
	}
	if used != 1 {
		t.Fatalf("%d devices show input, want 1", used)
	}

	// The second controller becomes player 1; the first, which had it, player 2.
	var pad1, pad2 string
	for _, d := range devs {
		switch d.Device.Device {
		case "/dev/input/event8":
			pad1 = d.ID
		case "/dev/input/event9":
			pad2 = d.ID
		}
	}
	players.Connect(pad1)
	players.Connect(pad2)
	do := func(id string, player int) (sdk.Result, error) {
		return r.m.Handle(context.Background(), sdk.Action{ID: "act_1", Type: "device.assign",
			Args: sdk.MustJSON(map[string]any{"id": id, "player": player})})
	}
	if _, err := do(pad2, 1); err != nil {
		t.Fatal(err)
	}
	if players.Slot(pad2) != 1 || players.Slot(pad1) != 2 {
		t.Fatalf("slots: pad1 %d pad2 %d", players.Slot(pad1), players.Slot(pad2))
	}
	for _, d := range r.m.readDevices() {
		if d.ID == pad2 && d.Player != 1 {
			t.Errorf("the read shows player %d", d.Player)
		}
	}
	if _, err := do(kbA, 1); sdk.CodeOf(err) != sdk.CodeInvalidArgs || !strings.Contains(err.Error(), "only controllers") {
		t.Fatalf("a keyboard has no slot: %v", err)
	}
	if _, err := do("nothing", 1); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("unknown device: %v", err)
	}

	_ = os.Remove(filepath.Join(r.sys, "dev/input/event1"))
	for range 400 {
		select {
		case ev := <-events:
			if ev.Type == EventDeviceDisconnected {
				return
			}
		case <-time.After(10 * time.Millisecond):
			r.clock.Advance(300 * time.Millisecond)
		}
	}
	t.Fatal("no device.disconnected")
}
