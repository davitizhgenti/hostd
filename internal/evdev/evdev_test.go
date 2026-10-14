package evdev

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/internal/evdev/evdevtest"
)

func TestBuiltinProfiles(t *testing.T) {
	ps, err := LoadProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range ps.list {
		ids = append(ids, p.ID)
	}
	for _, want := range []string{"xbox", "playstation", "switch", "8bitdo", "wii-remote", "dolphinbar"} {
		if !strings.Contains(strings.Join(ids, ","), want) {
			t.Errorf("no built-in profile %s (have %v)", want, ids)
		}
	}
}

func TestUserProfiles(t *testing.T) {
	dir := t.TempDir()
	// Replace a built-in, and add one: Guide on Start for a pad without one.
	_ = os.WriteFile(filepath.Join(dir, "xbox.toml"), []byte(`name = "My Xbox pad"
match = { vendor = "045e" }
buttons = { guide = "BTN_MODE" }
`), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "cheap.toml"), []byte(`name = "Cheap pad"
match = { vendor = "0079", products = ["0006"] }
buttons = { guide = "BTN_START" }
`), 0o644)
	ps, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := ps.forInput(deviceInfo{Vendor: "0079", Product: "0006", Gamepad: true})
	if p == nil || p.ID != "cheap" || p.codes[0x13b] != "guide" {
		t.Fatalf("cheap pad: %+v", p)
	}
	if p := ps.forInput(deviceInfo{Vendor: "045e", Gamepad: true}); p.Name != "My Xbox pad" {
		t.Fatalf("override: %+v", p)
	}
	for body, want := range map[string]string{
		"name = \"x\"\nmatch = { vendor = \"1\" }\nbuttons = { turbo = \"BTN_MODE\" }": `button "turbo"`,
		"name = \"x\"\nmatch = { vendor = \"1\" }\nbuttons = { guide = \"BTN_NOPE\" }": `unknown key "BTN_NOPE"`,
		"name = \"x\"": "match needs a vendor or a name",
		"name = \"x\"\nmatch = { vendor = \"1\" }\ncolor = \"red\"": "unknown key color",
	} {
		bad := t.TempDir()
		_ = os.WriteFile(filepath.Join(bad, "bad.toml"), []byte(body), 0o644)
		if _, err := LoadProfiles(bad); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", body, err, want)
		}
	}
}

func TestScanControllers(t *testing.T) {
	root := evdevtest.FakeSys(t)
	evdevtest.AddInput(t, root, "event3", "Microsoft X-Box 360 pad", "045e", "028e", evdevtest.GamepadKeys)
	evdevtest.AddInput(t, root, "event4", "Sony Interactive Entertainment Wireless Controller", "054c", "09cc", evdevtest.GamepadKeys)
	evdevtest.AddInput(t, root, "event5", "Sony Interactive Entertainment Wireless Controller Touchpad", "054c", "09cc", "0")
	evdevtest.AddInput(t, root, "event6", "Generic USB Joystick", "0079", "0006", evdevtest.GamepadKeys)
	evdevtest.AddInput(t, root, "event7", "SEMICO USB Keyboard", "1a2c", "6d5b", "fffffffffffffffe")
	evdevtest.AddInput(t, root, "event8", "Nintendo Wii Remote", "057e", "0306", evdevtest.GamepadKeys)
	evdevtest.AddHidraw(t, root, "hidraw5", "HJZ Mayflash Wiimote PC Adapter", "0003:0000057E:00000306")
	evdevtest.AddHidraw(t, root, "hidraw0", "SEMICO USB Keyboard", "0003:00001A2C:00006D5B")

	ps, _ := LoadProfiles("")
	got := map[string]string{}
	for _, c := range Scan(root, ps) {
		got[c.Device] = c.Profile
	}
	want := map[string]string{
		"/dev/input/event3": "xbox",
		"/dev/input/event4": "playstation", // not its touchpad, not the keyboard
		"/dev/input/event6": "gamepad",     // unknown: the generic profile
		"/dev/input/event8": "wii-remote",  // over Bluetooth: an input device
		"/dev/hidraw5":      "dolphinbar",  // raw, for Dolphin
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("controllers\n%v\nwant\n%v", got, want)
	}
}

func TestProfileButtonsReachBindings(t *testing.T) {
	// A pad whose profile puts Guide on Start: pressing Start is "guide".
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "cheap.toml"), []byte("name = \"Cheap\"\nmatch = { vendor = \"0079\" }\nbuttons = { guide = \"BTN_START\" }\n"), 0o644)
	ps, _ := LoadProfiles(dir)
	p := ps.forInput(deviceInfo{Vendor: "0079", Gamepad: true})
	var got []InputEvent
	readEvents(&chunked{data: append(inputEvent(evKey, 0x13b, 1), inputEvent(0, 0, 0)...), n: 1000}, p.codes,
		func(e InputEvent) { got = append(got, e) })
	if len(got) != 1 || !reflect.DeepEqual(got[0].Buttons, []string{"guide"}) {
		t.Fatalf("events %+v", got)
	}
}

func inputEvent(typ, code uint16, value int32) []byte {
	b := make([]byte, eventSize)
	binary.LittleEndian.PutUint16(b[16:], typ)
	binary.LittleEndian.PutUint16(b[18:], code)
	binary.LittleEndian.PutUint32(b[20:], uint32(value))
	return b
}

func TestReadEvents(t *testing.T) {
	const evSyn, evMsc, evLed = 0, 4, 0x11
	var stream bytes.Buffer
	// A key press with its scan code and sync, then an LED change (the
	// system, not a person), then a gamepad stick move.
	stream.Write(inputEvent(evMsc, 4, 30))
	stream.Write(inputEvent(evKey, 30, 1))
	stream.Write(inputEvent(evSyn, 0, 0))
	stream.Write(inputEvent(evLed, 0, 1))
	stream.Write(inputEvent(evAbs, 0, -12000))
	stream.Write(inputEvent(evSyn, 0, 0))

	for _, chunk := range []int{1, 7, eventSize, 1000} { // however the reads split it
		calls := 0
		readEvents(&chunked{data: stream.Bytes(), n: chunk}, nil, func(InputEvent) { calls++ })
		if calls == 0 {
			t.Fatalf("chunk %d: no activity seen", chunk)
		}
	}
	calls := 0
	readEvents(bytes.NewReader(append(inputEvent(evLed, 0, 1), inputEvent(evSyn, 0, 0)...)), nil, func(InputEvent) { calls++ })
	if calls != 0 {
		t.Fatalf("LED and sync events counted as a person: %d", calls)
	}
}

type chunked struct {
	data []byte
	n    int
}

func TestEvdevHotplug(t *testing.T) {
	// Devices are FIFOs here: a plugged-in "controller" is a new
	// event* node that the watcher must pick up.
	dir := t.TempDir()
	mk := func(name string) string {
		p := filepath.Join(dir, name)
		if err := mkfifo(p); err != nil {
			t.Skipf("mkfifo: %v", err)
		}
		return p
	}
	kbd := mk("event0")
	_ = os.WriteFile(filepath.Join(dir, "mice"), nil, 0o644) // not an event device

	touched := make(chan struct{}, 16)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (&Evdev{Dir: dir, SysRoot: t.TempDir()}).Watch(ctx, func(InputEvent) { touched <- struct{}{} })
	}()

	write := func(path string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write(inputEvent(evKey, 304, 1)) // BTN_SOUTH
		f.Close()
	}
	wait := func(what string) {
		t.Helper()
		select {
		case <-touched:
		case <-time.After(5 * time.Second):
			t.Fatalf("no activity from %s", what)
		}
	}
	write(kbd)
	wait("the first device")
	pad := mk("event7")
	write(pad)
	wait("a device plugged in later")

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// --- presence --------------------------------------------------------------------

func (c *chunked) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, fmt.Errorf("EOF")
	}
	n := min(c.n, len(p), len(c.data))
	copy(p, c.data[:n])
	c.data = c.data[n:]
	return n, nil
}

func TestGrabKeepsTheFileCloseable(t *testing.T) {
	// A grab must leave the file in the poller's non-blocking mode, so that
	// closing it ends a Read in progress (hostd's shutdown waits for that).
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	_ = grab(r, true) // not an input device: the ioctl fails, the mode must not change
	if err := r.SetReadDeadline(time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("the file left the poller after a grab: %v", err)
	}
	done := make(chan struct{})
	go func() { _, _ = r.Read(make([]byte, 1)); close(done) }()
	time.Sleep(20 * time.Millisecond)
	_ = r.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("closing the file did not end the Read")
	}
}

func mkfifo(path string) error { return syscall.Mkfifo(path, 0o600) }

func TestReadEventsGuide(t *testing.T) {
	var got []InputEvent
	var stream []byte
	stream = append(stream, inputEvent(evKey, btnMode, 1)...) // press
	stream = append(stream, inputEvent(0, 0, 0)...)
	readEvents(&chunked{data: stream, n: 1000}, map[uint16]string{btnMode: "guide"}, func(e InputEvent) { got = append(got, e) })
	stream = append(inputEvent(evKey, btnMode, 0), inputEvent(0, 0, 0)...) // release
	readEvents(&chunked{data: stream, n: 1000}, map[uint16]string{btnMode: "guide"}, func(e InputEvent) { got = append(got, e) })
	if len(got) != 2 || !reflect.DeepEqual(got[0].Buttons, []string{"guide"}) || len(got[1].Buttons) != 0 ||
		!reflect.DeepEqual(got[1].Released, []string{"guide"}) {
		t.Fatalf("events %+v", got)
	}
}

func TestReadEventsNav(t *testing.T) {
	var stream []byte
	for _, e := range [][3]int{{evKey, btnSouth, 1}, {evKey, btnSouth, 0}, {evAbs, absHat0Y, -1}, {evAbs, absHat0Y, 0},
		{evKey, btnEast, 1}, {evKey, btnWest, 1}, {evKey, 0x223, 1}} {
		stream = append(stream, inputEvent(uint16(e[0]), uint16(e[1]), int32(e[2]))...)
	}
	stream = append(stream, inputEvent(0, 0, 0)...)
	var got []string
	readEvents(&chunked{data: stream, n: 1000}, map[uint16]string{btnMode: "guide"}, func(e InputEvent) { got = append(got, e.Nav...) })
	if want := []string{"choose", "up", "back", "actions", "right"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("nav %q, want %q", got, want)
	}
	// A keyboard (no controller profile) never navigates.
	got = nil
	readEvents(&chunked{data: stream, n: 1000}, nil, func(e InputEvent) { got = append(got, e.Nav...) })
	if len(got) != 0 {
		t.Fatalf("keyboard nav %q", got)
	}
}
