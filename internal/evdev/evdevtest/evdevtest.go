// Package evdevtest builds fake sysfs and /dev trees for tests that need
// controllers to be plugged in.
package evdevtest

import (
	"os"
	"path/filepath"
	"testing"
)

// FakeSys makes an empty sysfs and /dev tree to add devices to.
func FakeSys(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"dev/input", "sys/class/input", "sys/class/hidraw"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// GamepadKeys is a capabilities/key bitmap with BTN_GAMEPAD (0x130) and
// BTN_MODE (0x13c): bits in the fifth 64-bit word.
const GamepadKeys = "1001000000000000 0 0 0 0"

func AddInput(t *testing.T, root, ev, name, vendor, product, keys string) {
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

func AddHidraw(t *testing.T, root, dev, name, id string) {
	t.Helper()
	dir := filepath.Join(root, "sys/class/hidraw", dev, "device")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "uevent"), []byte("HID_ID="+id+"\nHID_NAME="+name+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "dev", dev), nil, 0o644)
}
