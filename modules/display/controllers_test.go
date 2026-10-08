package display

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakeSys makes an empty sysfs and /dev tree to add devices to.
func fakeSys(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"dev/input", "sys/class/input", "sys/class/hidraw"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// gamepadKeys is a capabilities/key bitmap with BTN_GAMEPAD (0x130) and
// BTN_MODE (0x13c): bits in the fifth 64-bit word.
const gamepadKeys = "1001000000000000 0 0 0 0"

func addInput(t *testing.T, root, ev, name, vendor, product, keys string) {
	t.Helper()
	dir := filepath.Join(root, "sys/class/input", ev, "device")
	_ = os.MkdirAll(filepath.Join(dir, "id"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "capabilities"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "name"), []byte(name+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "id/vendor"), []byte(vendor+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "id/product"), []byte(product+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "capabilities/key"), []byte(keys+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "dev/input", ev), nil, 0o644)
}

func addHidraw(t *testing.T, root, dev, name, id string) {
	t.Helper()
	dir := filepath.Join(root, "sys/class/hidraw", dev, "device")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "uevent"), []byte("HID_ID="+id+"\nHID_NAME="+name+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "dev", dev), nil, 0o644)
}

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
	root := fakeSys(t)
	addInput(t, root, "event3", "Microsoft X-Box 360 pad", "045e", "028e", gamepadKeys)
	addInput(t, root, "event4", "Sony Interactive Entertainment Wireless Controller", "054c", "09cc", gamepadKeys)
	addInput(t, root, "event5", "Sony Interactive Entertainment Wireless Controller Touchpad", "054c", "09cc", "0")
	addInput(t, root, "event6", "Generic USB Joystick", "0079", "0006", gamepadKeys)
	addInput(t, root, "event7", "SEMICO USB Keyboard", "1a2c", "6d5b", "fffffffffffffffe")
	addInput(t, root, "event8", "Nintendo Wii Remote", "057e", "0306", gamepadKeys)
	addHidraw(t, root, "hidraw5", "HJZ Mayflash Wiimote PC Adapter", "0003:0000057E:00000306")
	addHidraw(t, root, "hidraw0", "SEMICO USB Keyboard", "0003:00001A2C:00006D5B")

	ps, _ := LoadProfiles("")
	got := map[string]string{}
	for _, c := range scanControllers(sysfs{root: root}, ps) {
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

func TestControllerEvents(t *testing.T) {
	r := newDisplayRig(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := r.e.Subscribe(ctx, "controller.*")
	time.Sleep(50 * time.Millisecond) // the watcher is in place

	addInput(t, r.sys, "event9", "Microsoft X-Box 360 pad", "045e", "028e", gamepadKeys)
	next := func() (string, Controller) {
		t.Helper()
		for range 200 {
			select {
			case ev := <-events:
				var c Controller
				_ = json.Unmarshal(ev.Data, &c)
				return ev.Type, c
			case <-time.After(10 * time.Millisecond):
				r.clock.Advance(300 * time.Millisecond)
			}
		}
		t.Fatal("no controller event")
		return "", Controller{}
	}
	if typ, c := next(); typ != EventControllerConnected || c.Profile != "xbox" || c.Device != "/dev/input/event9" {
		t.Fatalf("%s %+v", typ, c)
	}
	list, _ := r.m.Read(context.Background(), "controllers", nil)
	if cs := list.([]Controller); len(cs) != 1 || !reflect.DeepEqual(cs[0].Buttons, []string{"guide", "start", "select"}) {
		t.Fatalf("read %+v", list)
	}
	_ = os.Remove(filepath.Join(r.sys, "dev/input/event9"))
	if typ, _ := next(); typ != EventControllerDisconnected {
		t.Fatalf("got %s", typ)
	}
}
