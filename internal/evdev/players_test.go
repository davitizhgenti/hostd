package evdev

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPlayersTakeTheLowestFreeSlot(t *testing.T) {
	var p Players
	if a, b, c := p.Connect("a"), p.Connect("b"), p.Connect("c"); a != 1 || b != 2 || c != 3 {
		t.Fatalf("slots %d %d %d", a, b, c)
	}
	// Switched off and on again: the same slot.
	p.Disconnect("b")
	if got := p.Connect("b"); got != 2 {
		t.Fatalf("b came back as %d", got)
	}
	// While b is away a newcomer takes the lowest slot that is free of
	// connected controllers: b's.
	p.Disconnect("b")
	if got := p.Connect("d"); got != 2 {
		t.Fatalf("d got %d, want b's slot 2", got)
	}
	if got := p.Slot("b"); got != 0 {
		t.Fatalf("b still has %d after losing its slot", got)
	}
	if got := p.Slot("nobody"); got != 0 {
		t.Fatalf("a stranger has %d", got)
	}
}

func TestPlayersAssignSwaps(t *testing.T) {
	var p Players
	p.Connect("a")
	p.Connect("b")
	p.Assign("b", 1) // b takes a's slot; a gets b's
	if p.Slot("b") != 1 || p.Slot("a") != 2 {
		t.Fatalf("a %d b %d", p.Slot("a"), p.Slot("b"))
	}
	p.Assign("c", 2) // a stranger takes 2; a, with no old slot to move to, loses it? it keeps c's old (none)
	if p.Slot("c") != 2 || p.Slot("a") != 0 {
		t.Fatalf("a %d c %d", p.Slot("a"), p.Slot("c"))
	}
}

func TestDeviceIDIsStable(t *testing.T) {
	for _, c := range []struct {
		d    deviceInfo
		dev  string
		want string
	}{
		{deviceInfo{Vendor: "045e", Product: "028e", Uniq: "AA:BB"}, "/dev/input/event7", "045e:028e-aa:bb"},
		{deviceInfo{Vendor: "045e", Product: "028e", Phys: "usb-0000:00:14.0-2/input0"}, "/dev/input/event7", "045e:028e-usb-0000:00:14.0-2/input0"},
		{deviceInfo{Vendor: "045e", Product: "028e"}, "/dev/input/event9", "045e:028e-event9"},
	} {
		if got := DeviceID(c.d, c.dev); got != c.want {
			t.Errorf("DeviceID = %q, want %q", got, c.want)
		}
	}
}

func TestEvdevGrabAndDevicesWithNoControllers(t *testing.T) {
	e := &Evdev{}
	if err := e.Grab("", true); err != nil {
		t.Fatal(err)
	}
	if err := e.Grab("045e:028e-x", true); err != nil {
		t.Fatal(err)
	}
	if !e.grabbed[""] || !e.grabbed["045e:028e-x"] {
		t.Fatalf("grabbed = %v", e.grabbed)
	}
	// A controller that appears later is taken when it was asked for.
	if err := e.Grab("", false); err != nil {
		t.Fatal(err)
	}
	if e.grabbed[""] || !e.grabbed["045e:028e-x"] {
		t.Fatalf("grabbed after giving back all = %v", e.grabbed)
	}
	if got := e.Devices(); len(got) != 0 {
		t.Fatalf("devices = %v", got)
	}
	f, err := os.CreateTemp(t.TempDir(), "pad")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	e.pad("/dev/input/event1", "045e:028e-x", f) // a regular file cannot be grabbed: ignored
	e.players().Connect("045e:028e-x")
	if got := e.Devices(); got["045e:028e-x"] != 1 {
		t.Fatalf("devices = %v", got)
	}
	e.pad("/dev/input/event1", "045e:028e-x", nil)
	if len(e.Devices()) != 0 {
		t.Fatal("an unplugged pad is still listed")
	}
}

func TestPlayersSurviveARestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "players.json")
	a := &Players{File: file}
	if err := a.Load(); err != nil { // nothing yet: not an error
		t.Fatal(err)
	}
	a.Connect("pad-1")
	a.Connect("pad-2")
	a.Assign("pad-2", 1)
	b := &Players{File: file} // hostd starts again
	if err := b.Load(); err != nil {
		t.Fatal(err)
	}
	if b.Slot("pad-2") != 1 || b.Slot("pad-1") != 2 {
		t.Fatalf("slots after a restart: pad-1 %d, pad-2 %d", b.Slot("pad-1"), b.Slot("pad-2"))
	}
	b.Connect("pad-1") // the controllers are found again as hostd starts
	b.Connect("pad-2")
	if got := b.Connect("pad-3"); got != 3 {
		t.Fatalf("a newcomer after a restart gets %d", got)
	}
	if err := os.WriteFile(file, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (&Players{File: file}).Load(); err == nil {
		t.Fatal("a broken file was read")
	}
}
