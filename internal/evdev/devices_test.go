package evdev

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/davitizhgenti/hostd/internal/evdev/evdevtest"
)

// capabilities writes a bitmap into a fake device's capabilities folder.
func capabilities(t *testing.T, root, ev, name, bitmap string) {
	t.Helper()
	p := filepath.Join(root, "sys/class/input", ev, "device/capabilities", name)
	if err := os.WriteFile(p, []byte(bitmap+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanDevicesTellsKindsApart(t *testing.T) {
	root := evdevtest.FakeSys(t)
	// Bitmaps as sysfs prints them: hex words, the highest first. KEY_A (30) and
	// KEY_SPACE (57) are bits of the first word; BTN_MOUSE (0x110 = 272) is bit 16
	// of the fifth.
	const keyboard = "200000040000000"
	const mouse = "10000 0 0 0 0"
	evdevtest.AddInput(t, root, "event0", "Logitech Keyboard", "046d", "c31c", keyboard)
	evdevtest.AddInput(t, root, "event1", "Other Keyboard", "046d", "c31c", keyboard)
	evdevtest.AddInput(t, root, "event2", "Mouse", "046d", "c077", mouse)
	capabilities(t, root, "event2", "rel", "3") // REL_X and REL_Y
	evdevtest.AddInput(t, root, "event3", "Power Button", "0000", "0001", "0")
	evdevtest.AddInput(t, root, "event4", "Xbox Pad", "045e", "028e", evdevtest.GamepadKeys)
	evdevtest.AddInput(t, root, "event5", "Xbox Pad", "045e", "028e", evdevtest.GamepadKeys)

	var players Players
	players.Connect(DeviceID(deviceInfo{Vendor: "045e", Product: "028e"}, "/dev/input/event4"))
	got := ScanDevices(root, nil, &players)
	kinds := map[string]string{}
	ids := map[string]bool{}
	for _, d := range got {
		kinds[d.Device] = d.Kind
		ids[d.ID] = true
	}
	want := map[string]string{
		"/dev/input/event0": KindKeyboard, "/dev/input/event1": KindKeyboard, "/dev/input/event2": KindPointer,
		"/dev/input/event4": KindGamepad, "/dev/input/event5": KindGamepad, // the power button is no device
	}
	if len(kinds) != len(want) {
		t.Fatalf("devices = %v", kinds)
	}
	for dev, kind := range want {
		if kinds[dev] != kind {
			t.Errorf("%s is %q, want %q", dev, kinds[dev], kind)
		}
	}
	// Two of one model are two devices (their nodes differ).
	if len(ids) != 5 {
		t.Errorf("ids = %v: devices of one model share an ID", ids)
	}
	for _, d := range got {
		if d.Kind == KindGamepad && d.Device == "/dev/input/event4" && d.Player != 1 {
			t.Errorf("player = %d", d.Player)
		}
		if d.Kind != KindGamepad && d.Player != 0 {
			t.Errorf("%s has a player", d.Name)
		}
	}
}
