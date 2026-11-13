package evdev

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// One canonical gamepad. Every controller family reports its buttons and
// sticks with its own codes (Xbox's X is on the code PlayStation's Triangle
// is on, a Wii Remote has a 1 and a 2); a profile's [pad] table says which
// code is which canonical button, and everything above this file (the
// menu's Nav, hostd's button bindings) speaks only the canonical names.
//
// The face buttons are named by position, not label: south is where an
// Xbox's A and a PlayStation's cross are, east its B and circle. The
// family's labels are the shell's business (it shows what is printed on
// the controller in the person's hand).
const (
	BtnSouth  = "south"
	BtnEast   = "east"
	BtnWest   = "west"
	BtnNorth  = "north"
	BtnLB     = "lb" // left shoulder
	BtnRB     = "rb"
	BtnLT     = "lt" // left trigger (analog or digital)
	BtnRT     = "rt"
	BtnLS     = "ls" // left stick, pressed
	BtnRS     = "rs"
	BtnStart  = "start"
	BtnSelect = "select"
	BtnGuide  = "guide"
	BtnUp     = "up" // the d-pad
	BtnDown   = "down"
	BtnLeft   = "left"
	BtnRight  = "right"
)

// ButtonNames are the buttons hostd can bind (see Bindings) and a profile
// can place: the canonical ones, without the d-pad.
var ButtonNames = []string{BtnGuide, BtnStart, BtnSelect, BtnSouth, BtnEast, BtnWest, BtnNorth,
	BtnLB, BtnRB, BtnLT, BtnRT, BtnLS, BtnRS}

// padButtons are all the canonical buttons: ButtonNames and the d-pad.
var padButtons = append(slices.Clone(ButtonNames), BtnUp, BtnDown, BtnLeft, BtnRight)

// Canonical axes: two sticks, two triggers, and the d-pad reported as a
// pair of hat axes.
const (
	AxisLX, AxisLY, AxisRX, AxisRY = "lx", "ly", "rx", "ry"
	AxisLT, AxisRT                 = "lt", "rt"
	AxisHatX, AxisHatY             = "hatx", "haty"
)

var padAxes = []string{AxisLX, AxisLY, AxisRX, AxisRY, AxisLT, AxisRT, AxisHatX, AxisHatY}

// Families of controllers: what the shell shows on its hints, and which
// face button confirms.
const (
	FamilyXbox        = "xbox"
	FamilyPlayStation = "playstation"
	FamilyNintendo    = "nintendo"
	FamilyWii         = "wii"
	FamilyGeneric     = "generic"
)

var families = []string{FamilyXbox, FamilyPlayStation, FamilyNintendo, FamilyWii, FamilyGeneric}

// PadMap is a profile's [pad] table.
type PadMap struct {
	// Family: xbox, playstation, nintendo, wii or generic (the default).
	Family string `toml:"family" json:"family,omitempty"`
	// Confirm and Back are the face positions that confirm and go back:
	// south and east, except where the controller's own A button is on the
	// right (Nintendo's, a Wii Remote's): east and south.
	Confirm string `toml:"confirm" json:"confirm,omitempty"`
	Back    string `toml:"back" json:"back,omitempty"`
	// Keys: an evdev key (BTN_SOUTH, KEY_UP, a number) -> a canonical button.
	Keys map[string]string `toml:"keys" json:"keys,omitempty"`
	// Axes: an evdev axis (ABS_X, ABS_HAT0X, a number) -> a canonical axis,
	// with its range for where the kernel cannot be asked (min and max
	// are what EVIOCGABS says on a real device).
	Axes map[string]AxisDef `toml:"axes" json:"axes,omitempty"`
}

// AxisDef places one evdev axis.
type AxisDef struct {
	As  string `toml:"as" json:"as"`
	Min int32  `toml:"min" json:"min"`
	Max int32  `toml:"max" json:"max"`
}

// padRuntime is a PadMap with its names resolved to codes.
type padRuntime struct {
	family, confirm, back string
	keys                  map[uint16]string
	axes                  map[uint16]axisRT
}

type axisRT struct {
	as       string
	min, max int32
}

// More evdev names a profile may use, beside keyCodes' (controllers.go).
var moreKeyCodes = map[string]uint16{
	"BTN_A": 0x130, "BTN_B": 0x131, "BTN_C": 0x132, "BTN_X": 0x133, "BTN_Y": 0x134, "BTN_Z": 0x135,
	"BTN_0": 0x100, "BTN_1": 0x101, "BTN_2": 0x102, "BTN_3": 0x103, "BTN_4": 0x104,
	"BTN_DPAD_UP": 0x220, "BTN_DPAD_DOWN": 0x221, "BTN_DPAD_LEFT": 0x222, "BTN_DPAD_RIGHT": 0x223,
	"KEY_UP": 103, "KEY_LEFT": 105, "KEY_RIGHT": 106, "KEY_DOWN": 108,
}

var absCodes = map[string]uint16{
	"ABS_X": 0, "ABS_Y": 1, "ABS_Z": 2, "ABS_RX": 3, "ABS_RY": 4, "ABS_RZ": 5,
	"ABS_GAS": 9, "ABS_BRAKE": 10,
	"ABS_HAT0X": 0x10, "ABS_HAT0Y": 0x11, "ABS_HAT1X": 0x12, "ABS_HAT1Y": 0x13,
	"ABS_HAT2X": 0x14, "ABS_HAT2Y": 0x15, "ABS_HAT3X": 0x16, "ABS_HAT3Y": 0x17,
}

func keyCode(name string) (uint16, bool) {
	if c, ok := keyCodes[name]; ok {
		return c, true
	}
	if c, ok := moreKeyCodes[name]; ok {
		return c, true
	}
	n, err := strconv.ParseUint(name, 0, 16)
	return uint16(n), err == nil
}

func absCode(name string) (uint16, bool) {
	if c, ok := absCodes[name]; ok {
		return c, true
	}
	n, err := strconv.ParseUint(name, 0, 16)
	return uint16(n), err == nil
}

// defaultPad is the kernel's standard gamepad (Documentation/input/gamepad):
// what a controller no profile knows is assumed to be. Xbox-style codes:
// 0x133 (BTN_X) is the west button and 0x134 (BTN_Y) the north one.
func defaultPad() PadMap {
	return PadMap{
		Family: FamilyGeneric, Confirm: BtnSouth, Back: BtnEast,
		Keys: map[string]string{
			"BTN_SOUTH": BtnSouth, "BTN_EAST": BtnEast, "BTN_X": BtnWest, "BTN_Y": BtnNorth,
			"BTN_TL": BtnLB, "BTN_TR": BtnRB, "BTN_TL2": BtnLT, "BTN_TR2": BtnRT,
			"BTN_THUMBL": BtnLS, "BTN_THUMBR": BtnRS,
			"BTN_START": BtnStart, "BTN_SELECT": BtnSelect, "BTN_MODE": BtnGuide,
			"BTN_DPAD_UP": BtnUp, "BTN_DPAD_DOWN": BtnDown, "BTN_DPAD_LEFT": BtnLeft, "BTN_DPAD_RIGHT": BtnRight,
		},
		Axes: map[string]AxisDef{
			"ABS_X": {AxisLX, -32768, 32767}, "ABS_Y": {AxisLY, -32768, 32767},
			"ABS_RX": {AxisRX, -32768, 32767}, "ABS_RY": {AxisRY, -32768, 32767},
			"ABS_Z": {AxisLT, 0, 255}, "ABS_RZ": {AxisRT, 0, 255},
			"ABS_HAT0X": {AxisHatX, -1, 1}, "ABS_HAT0Y": {AxisHatY, -1, 1},
		},
	}
}

// padRuntime checks a profile's [pad] table and resolves it. A profile
// without one gets the standard gamepad. A table with keys or axes of its
// own replaces the standard ones of that kind.
func resolvePad(m *PadMap) (*padRuntime, error) {
	pm := defaultPad()
	if m != nil {
		if m.Family != "" {
			pm.Family = m.Family
		}
		if m.Confirm != "" || m.Back != "" {
			pm.Confirm, pm.Back = m.Confirm, m.Back
		}
		if len(m.Keys) > 0 {
			pm.Keys = m.Keys
		}
		if len(m.Axes) > 0 {
			pm.Axes = m.Axes
		}
	}
	if !slices.Contains(families, pm.Family) {
		return nil, fmt.Errorf("pad.family %q: use one of %s", pm.Family, strings.Join(families, ", "))
	}
	faces := []string{BtnSouth, BtnEast, BtnWest, BtnNorth}
	if !slices.Contains(faces, pm.Confirm) || !slices.Contains(faces, pm.Back) || pm.Confirm == pm.Back {
		return nil, fmt.Errorf("pad.confirm %q and pad.back %q: two different face buttons (%s)", pm.Confirm, pm.Back, strings.Join(faces, ", "))
	}
	rt := &padRuntime{family: pm.Family, confirm: pm.Confirm, back: pm.Back, keys: map[uint16]string{}, axes: map[uint16]axisRT{}}
	for key, button := range pm.Keys {
		code, ok := keyCode(key)
		if !ok {
			return nil, fmt.Errorf("pad.keys: unknown key %q (an evdev name such as BTN_SOUTH, or a number)", key)
		}
		if !slices.Contains(padButtons, button) {
			return nil, fmt.Errorf("pad.keys %s: %q is not a button (use one of %s)", key, button, strings.Join(padButtons, ", "))
		}
		rt.keys[code] = button
	}
	for axis, def := range pm.Axes {
		code, ok := absCode(axis)
		if !ok {
			return nil, fmt.Errorf("pad.axes: unknown axis %q (an evdev name such as ABS_X, or a number)", axis)
		}
		if !slices.Contains(padAxes, def.As) {
			return nil, fmt.Errorf("pad.axes %s: %q is not an axis (use one of %s)", axis, def.As, strings.Join(padAxes, ", "))
		}
		if def.Max <= def.Min {
			return nil, fmt.Errorf("pad.axes %s: max must be above min", axis)
		}
		rt.axes[code] = axisRT{def.As, def.Min, def.Max}
	}
	return rt, nil
}

// withRange is a copy of the pad with one axis's range set (a Navigator's
// own: profiles are shared by every device of a kind).
func (p *padRuntime) withRange(code uint16, as string, min, max int32) *padRuntime {
	c := *p
	c.axes = maps.Clone(p.axes)
	c.axes[code] = axisRT{as, min, max}
	return &c
}
