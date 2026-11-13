package evdev

import (
	"math"
	"time"
)

// A Wii Remote's HID protocol, as far as the menu needs it: the buttons,
// the IR camera (for the pointer) and a Nunchuk. A Mayflash DolphinBar in
// its Dolphin mode hands each paired remote to the host as a raw HID
// device (/dev/hidraw*) speaking this protocol; the kernel driver
// (hid-wiimote) does not bind to it, so there is no input device. See
// wiibrew.org "Wiimote" for the reports.

// Output report IDs (host to remote).
const (
	wiiOutLED       = 0x11
	wiiOutReporting = 0x12
	wiiOutIR        = 0x13
	wiiOutStatus    = 0x15
	wiiOutWrite     = 0x16
	wiiOutRead      = 0x17
	wiiOutIR2       = 0x1a
)

// Input report IDs (remote to host).
const (
	wiiInStatus   = 0x20
	wiiInRead     = 0x21
	wiiInAck      = 0x22
	wiiInButtons  = 0x30
	wiiInIRExt    = 0x37 // buttons, accelerometer, 10 bytes of IR, 6 of extension
	wiiRegister   = 0x04 // memory space flag of a write or read: registers
	wiiContinuous = 0x04 // reporting flag: send reports even when nothing changed
)

// Core buttons as one 16-bit mask: the first data byte high, the second low.
const (
	wiiLeft  = 0x0100
	wiiRight = 0x0200
	wiiDown  = 0x0400
	wiiUp    = 0x0800
	wiiPlus  = 0x1000
	wiiTwo   = 0x0001
	wiiOne   = 0x0002
	wiiB     = 0x0004
	wiiA     = 0x0008
	wiiMinus = 0x0010
	wiiHome  = 0x0080
)

// wiiButtons maps core buttons to the evdev key codes the Wii Remote
// profile (controllers/wii-remote.toml) names, which is how the remote
// reaches the same Navigator as any controller.
var wiiButtons = []struct {
	mask uint16
	code uint16
}{
	{wiiA, 0x130}, {wiiB, 0x131}, {wiiOne, 0x101}, {wiiTwo, 0x102},
	{wiiPlus, 0x13b}, {wiiMinus, 0x13a}, {wiiHome, 0x13c},
	{wiiUp, 103}, {wiiDown, 108}, {wiiLeft, 105}, {wiiRight, 106},
}

// Nunchuk: its buttons as evdev codes, and its stick's axes.
const (
	wiiNunchukC = 0x132 // BTN_C
	wiiNunchukZ = 0x135 // BTN_Z
	wiiAbsX     = 0
	wiiAbsY     = 1
)

// The IR camera sees 1024 x 768 points.
const (
	irWidth  = 1023.0
	irHeight = 767.0
)

// wiiReport is one input report, decoded as far as is known.
type wiiReport struct {
	ID    byte
	Core  uint16 // core buttons mask
	Dots  [4]irDot
	HasIR bool
	Ext   []byte // the extension's 6 bytes, for report 0x37
	// Status (report 0x20): whether an extension is plugged in.
	Extension bool
	// Data (report 0x21): the bytes a read returned.
	Data []byte
}

// irDot is a point the camera sees; Valid is false for an empty slot.
type irDot struct {
	X, Y  int
	Valid bool
}

// parseWiiReport decodes a report as read from hidraw (the ID first).
// ok is false for one that is too short or is not one that is used.
func parseWiiReport(b []byte) (r wiiReport, ok bool) {
	if len(b) < 1 {
		return r, false
	}
	r.ID = b[0]
	switch {
	case r.ID == wiiInStatus:
		if len(b) < 7 {
			return r, false
		}
		r.Core = uint16(b[1])<<8 | uint16(b[2])
		r.Extension = b[3]&0x02 != 0
		return r, true
	case r.ID == wiiInRead:
		if len(b) < 22 {
			return r, false
		}
		r.Core = uint16(b[1])<<8 | uint16(b[2])
		if b[3]&0x0f != 0 { // an error: nothing was read
			return r, true
		}
		n := int(b[3]>>4) + 1
		r.Data = append([]byte(nil), b[6:6+min(n, 16)]...)
		return r, true
	case r.ID == wiiInIRExt:
		if len(b) < 22 {
			return r, false
		}
		r.Core = uint16(b[1])<<8 | uint16(b[2])
		r.Dots, r.HasIR = decodeIRBasic(b[6:16]), true
		r.Ext = b[16:22]
		return r, true
	case r.ID >= wiiInButtons && r.ID <= 0x3f:
		if len(b) < 3 {
			return r, false
		}
		r.Core = uint16(b[1])<<8 | uint16(b[2])
		return r, true
	}
	return r, false
}

// decodeIRBasic reads the IR camera's basic mode: 10 bytes, two points per
// 5 bytes, 10 bits for each coordinate. An empty slot reads 1023, 1023.
func decodeIRBasic(b []byte) [4]irDot {
	dot := func(x, y int) irDot { return irDot{X: x, Y: y, Valid: x != 1023 || y != 1023} }
	var d [4]irDot
	for i := 0; i < 2; i++ {
		p := b[i*5 : i*5+5]
		d[i*2] = dot(int(p[0])|int(p[2]>>4&3)<<8, int(p[1])|int(p[2]>>6&3)<<8)
		d[i*2+1] = dot(int(p[3])|int(p[2]&3)<<8, int(p[4])|int(p[2]>>2&3)<<8)
	}
	return d
}

// --- output reports ---

func wiiLEDReport(slot int) []byte {
	var leds byte
	if slot >= 1 && slot <= 4 {
		leds = 0x10 << (slot - 1)
	}
	return []byte{wiiOutLED, leds}
}

// wiiReporting asks for report 0x37, sent steadily (so a remote that goes
// quiet is known to be gone).
func wiiReporting() []byte { return []byte{wiiOutReporting, wiiContinuous, wiiInIRExt} }

func wiiStatusRequest() []byte { return []byte{wiiOutStatus, 0x00} }

// wiiWriteRegister writes up to 16 bytes at a register address.
func wiiWriteRegister(addr uint32, data []byte) []byte {
	r := make([]byte, 22)
	r[0], r[1] = wiiOutWrite, wiiRegister
	r[2], r[3], r[4] = byte(addr>>16), byte(addr>>8), byte(addr)
	r[5] = byte(len(data))
	copy(r[6:], data)
	return r
}

// wiiReadRegister asks for size bytes at a register address.
func wiiReadRegister(addr uint32, size int) []byte {
	return []byte{wiiOutRead, wiiRegister, byte(addr >> 16), byte(addr >> 8), byte(addr), byte(size >> 8), byte(size)}
}

// wiiIRInit is what turns the IR camera on, in basic mode (10 bytes per
// report), at a middling sensitivity: Dolphin's and wiibrew's sequence.
func wiiIRInit() [][]byte {
	return [][]byte{
		{wiiOutIR, 0x04},
		{wiiOutIR2, 0x04},
		wiiWriteRegister(0xb00030, []byte{0x01}),
		wiiWriteRegister(0xb00000, []byte{0x02, 0x00, 0x00, 0x71, 0x01, 0x00, 0x72, 0x00, 0x20}),
		wiiWriteRegister(0xb0001a, []byte{0x1f, 0x03}),
		wiiWriteRegister(0xb00033, []byte{0x01}),
		wiiWriteRegister(0xb00030, []byte{0x08}),
	}
}

// wiiExtensionInit makes a plugged-in extension report unencrypted, then
// asks for its id.
func wiiExtensionInit() [][]byte {
	return [][]byte{
		wiiWriteRegister(0xa400f0, []byte{0x55}),
		wiiWriteRegister(0xa400fb, []byte{0x00}),
		wiiReadRegister(0xa400fa, 6),
	}
}

// isNunchuk reports whether an extension id (6 bytes) is a Nunchuk's.
func isNunchuk(id []byte) bool {
	return len(id) >= 6 && id[2] == 0xa4 && id[3] == 0x20 && id[4] == 0x00 && id[5] == 0x00
}

// nunchukState decodes a Nunchuk's 6 bytes: the stick as -100..100 (up and
// right positive on the device; the axes are screen-like, so Y is flipped)
// and whether C and Z are held (0 means pressed on the wire).
func nunchukState(b []byte) (x, y int32, c, z bool) {
	if len(b) < 6 {
		return 0, 0, false, false
	}
	axis := func(v byte) int32 {
		n := (int32(v) - 128) * 100 / 128
		return max(-100, min(100, n))
	}
	return axis(b[0]), -axis(b[1]), b[5]&0x02 == 0, b[5]&0x01 == 0
}

// --- the pointer ---

// A pointerTracker turns the IR dots into a smoothed position on the
// screen as shares (0..1), mirrored: the camera sees the bar move left
// as the remote turns right.
type pointerTracker struct {
	x, y float64
	seen time.Time // the last time two dots were seen
	have bool
}

const (
	pointerSmooth = 0.4                    // share of the way to the new position per report
	pointerDead   = 0.002                  // moves smaller than this are noise
	pointerLost   = 300 * time.Millisecond // without two dots this long, the remote points away
)

// Update takes a report's dots at time now and returns where the pointer
// is and whether it is on the screen. With one dot the position is held
// (one dot of the bar would be off by half its width); with none, after
// pointerLost, it is not shown.
func (t *pointerTracker) Update(dots [4]irDot, now time.Time) (x, y float64, shown bool) {
	var vx, vy float64
	n := 0
	for _, d := range dots {
		if d.Valid {
			vx += float64(d.X)
			vy += float64(d.Y)
			n++
		}
	}
	if n >= 2 {
		// Two points are the bar's ends (more are stray light: the two
		// outermost of them would do, but the mean of all is steadier).
		tx := 1 - vx/float64(n)/irWidth
		ty := vy / float64(n) / irHeight
		tx, ty = math.Min(1, math.Max(0, tx)), math.Min(1, math.Max(0, ty))
		if !t.have {
			t.x, t.y, t.have = tx, ty, true
		} else if math.Abs(tx-t.x) > pointerDead || math.Abs(ty-t.y) > pointerDead {
			t.x += (tx - t.x) * pointerSmooth
			t.y += (ty - t.y) * pointerSmooth
		}
		t.seen = now
	}
	shown = t.have && now.Sub(t.seen) < pointerLost
	if !shown {
		t.have = false
	}
	return t.x, t.y, shown
}
