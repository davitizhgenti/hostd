package evdev

import (
	"path/filepath"
	"sort"
)

// The kinds of input device hostd tells apart.
const (
	KindKeyboard = "keyboard"
	KindPointer  = "pointer" // a mouse, trackpad or trackball
	KindTouch    = "touch"
	KindGamepad  = "gamepad"
)

// Device is an input device on the machine: a keyboard, a mouse, a
// controller. Each has an ID of its own (see DeviceID), so two keyboards or two
// controllers of one model are two devices, and what they do can be told apart.
type Device struct {
	ID      string `json:"id"`
	Device  string `json:"device"` // /dev/input/event7
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Vendor  string `json:"vendor"`
	Product string `json:"product"`
	// Player is a controller's slot (1, 2...); 0 for the rest.
	Player int `json:"player,omitempty"`
}

// kindOf says what an input device is, from what sysfs says it can do; "" for
// what is none of them (a power button, a lid switch, a speaker's jack).
func kindOf(s sysfs, dev string, d deviceInfo, controller bool) string {
	base := filepath.Join("sys/class/input", filepath.Base(dev), "device/capabilities")
	keys, rel := s.read(filepath.Join(base, "key")), s.read(filepath.Join(base, "rel"))
	switch {
	case controller || d.Gamepad:
		return KindGamepad
	case hasKeyBit(rel, 0) && hasKeyBit(rel, 1) && hasKeyBit(keys, 0x110): // REL_X, REL_Y, BTN_MOUSE
		return KindPointer
	case hasKeyBit(keys, 0x14a) && s.read(filepath.Join(base, "abs")) != "": // BTN_TOUCH with axes
		return KindTouch
	case hasKeyBit(keys, 30) && hasKeyBit(keys, 57): // KEY_A and KEY_SPACE
		return KindKeyboard
	}
	return ""
}

// ScanDevices lists the input devices under root ("/" outside tests): every
// keyboard, pointer, touch device and controller. Controllers (matched by ps,
// nil for the built-in profiles) get their player slot from players, if given.
func ScanDevices(root string, ps *Profiles, players *Players) []Device {
	s := sysfs{root: root}
	if ps == nil {
		ps, _ = LoadProfiles("")
	}
	var out []Device
	events, _ := filepath.Glob(filepath.Join(root, "dev/input/event*"))
	for _, ev := range events {
		dev := "/dev/input/" + filepath.Base(ev)
		d := s.input(dev)
		p := ps.forInput(d)
		controller := p != nil && (d.Gamepad || p.Match.Name != "")
		kind := kindOf(s, dev, d, controller)
		if kind == "" {
			continue
		}
		x := Device{ID: DeviceID(d, dev), Device: dev, Name: d.Name, Kind: kind, Vendor: d.Vendor, Product: d.Product}
		if kind == KindGamepad && players != nil {
			x.Player = players.Slot(x.ID)
		}
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Device < out[j].Device })
	return out
}

// DeviceInfoFor builds the device description DeviceID reads, for tests of
// other packages that need the ID of a fake device.
func DeviceInfoFor(vendor, product string) deviceInfo {
	return deviceInfo{Vendor: vendor, Product: product}
}
