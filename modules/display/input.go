package display

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/sys/unix"
)

// Input reports that someone touched an input device. The display module
// uses it to tell whether a person is at the screen, and to open the
// menu with a controller's Guide button.
type Input interface {
	// Watch calls fn on input activity until ctx ends. Devices plugged in
	// later (a controller switched on) are picked up.
	Watch(ctx context.Context, fn func(InputEvent)) error
}

// A Grabber can take the game controllers from everyone else and give
// them back. While hostd's menu is in front, apps that read controllers
// themselves (Steam does) must not act on its navigation: hostd takes
// them and sends the menu Nav events.
type Grabber interface {
	Grab(on bool) error
}

// InputEvent is a batch of input from one device.
type InputEvent struct {
	// Buttons pressed and let go, by name (see Buttons), for the
	// binding table.
	Buttons, Released []string
	// Nav is a controller's navigation, for the menu: up, down, left,
	// right (D-pad), choose (A), back (B) and actions (X or Y).
	Nav []string
}

// Controller buttons and axes for Nav. X and Y are numbered differently
// by different drivers; either opens the actions.
const (
	btnSouth, btnEast, btnNorth, btnWest = 0x130, 0x131, 0x133, 0x134
	absHat0X, absHat0Y                   = 0x10, 0x11
)

var navButtons = map[uint16]string{
	btnSouth: "choose", btnEast: "back", btnNorth: "actions", btnWest: "actions",
	0x220: "up", 0x221: "down", 0x222: "left", 0x223: "right", // BTN_DPAD_*
}

// evioCGrab is EVIOCGRAB: while held, only the grabbing file gets the
// device's events.
const evioCGrab = 0x40044590

// btnMode is BTN_MODE, the Guide button of Xbox, PlayStation and most
// other controllers under Linux.
const btnMode = 0x13c

// Linux input event types that mean a person did something. EV_SYN and
// EV_MSC accompany them; EV_LED and friends are the system talking.
const (
	evKey = 0x01 // keys and buttons, including gamepad buttons
	evRel = 0x02 // mouse movement, wheels
	evAbs = 0x03 // touchpads, gamepad sticks and triggers
)

// eventSize is sizeof(struct input_event) on 64-bit Linux: a 16-byte
// timeval, then type, code (uint16) and value (int32).
const eventSize = 24

// isActivity reads one input_event and reports whether it is a person's
// doing. Small analog stick wobble on gamepads (|value| within a few units
// of centre arrives as EV_ABS too) still counts: hostd only needs "someone
// is here", and a resting controller sends nothing.
func isActivity(ev []byte) bool {
	if len(ev) < eventSize {
		return false
	}
	switch binary.LittleEndian.Uint16(ev[16:18]) {
	case evKey, evRel, evAbs:
		return true
	}
	return false
}

// Evdev reads /dev/input/event*: keyboards, mice and game controllers
// alike (the compositor never sees controllers). The screen user needs to
// be in the input group, which the installer sets up.
type Evdev struct {
	Dir string // default /dev/input
	// Profiles say which buttons each controller has (see controllers.go);
	// nil: the built-in ones.
	Profiles *Profiles
	SysRoot  string // where sysfs is mounted, "/" outside tests

	mu      sync.Mutex
	pads    map[string]*os.File // open controllers, by path
	grabbed bool
}

// Grab takes the open controllers (and ones plugged in later) from every
// other reader, or gives them back. Keyboards and mice are never taken.
func (e *Evdev) Grab(on bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.grabbed = on
	var errs []error
	for path, f := range e.pads {
		if err := grab(f, on); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
		}
	}
	return errors.Join(errs...)
}

// grab sets EVIOCGRAB on f. Through SyscallConn, not f.Fd(): Fd puts the
// file in blocking mode, and then closing it no longer ends the reader's
// Read, so hostd hung on shutdown after a grab.
func grab(f *os.File, on bool) error {
	v := 0
	if on {
		v = 1
	}
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var ioErr error
	if err := rc.Control(func(fd uintptr) { ioErr = unix.IoctlSetInt(int(fd), evioCGrab, v) }); err != nil {
		return err
	}
	return ioErr
}

// pad notes an open controller (f) or its end (nil), and grabs it if
// controllers are taken.
func (e *Evdev) pad(path string, f *os.File) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if f == nil {
		delete(e.pads, path)
		return
	}
	if e.pads == nil {
		e.pads = map[string]*os.File{}
	}
	e.pads[path] = f
	if e.grabbed {
		_ = grab(f, true)
	}
}

func (e *Evdev) Watch(ctx context.Context, fn func(InputEvent)) error {
	dir := e.Dir
	if dir == "" {
		dir = "/dev/input"
	}
	profiles := e.Profiles
	if profiles == nil {
		var err error
		if profiles, err = LoadProfiles(""); err != nil {
			return err
		}
	}
	sys := sysfs{root: e.SysRoot}
	if sys.root == "" {
		sys.root = "/"
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	if err := w.Add(dir); err != nil {
		return err
	}

	var mu sync.Mutex
	open := map[string]bool{}
	var wg sync.WaitGroup
	defer wg.Wait()
	readDevice := func(path string) {
		mu.Lock()
		if open[path] || !strings.HasPrefix(filepath.Base(path), "event") {
			mu.Unlock()
			return
		}
		f, err := os.Open(path)
		if err != nil {
			mu.Unlock()
			return // not ours to read (permissions), or gone already
		}
		open[path] = true
		mu.Unlock()
		// The device's buttons, by its profile: a known pad's Guide,
		// nothing for a keyboard.
		var codes map[uint16]string
		if p := profiles.forInput(sys.input(path)); p != nil {
			codes = p.codes
		}
		if codes != nil {
			e.pad(path, f)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { mu.Lock(); delete(open, path); mu.Unlock() }()
			if codes != nil {
				defer e.pad(path, nil)
			}
			stop := context.AfterFunc(ctx, func() { f.Close() })
			defer stop()
			defer f.Close()
			readEvents(f, codes, fn)
		}()
	}

	matches, _ := filepath.Glob(filepath.Join(dir, "event*"))
	for _, m := range matches {
		readDevice(m)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return errors.New("input watcher closed")
			}
			if ev.Op&(fsnotify.Create|fsnotify.Chmod) != 0 {
				// udev creates the node first and sets its group a moment
				// later; Chmod catches the device once it is readable.
				readDevice(ev.Name)
			}
		case err := <-w.Errors:
			if err != nil {
				return err
			}
		}
	}
}

// readEvents reads input events until the device goes away. codes names
// the device's bindable buttons; a device with codes is a controller,
// whose navigation is reported too.
func readEvents(r io.Reader, codes map[uint16]string, fn func(InputEvent)) {
	buf := make([]byte, eventSize*64)
	var pending []byte
	for {
		n, err := r.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)
			active := false
			var ev InputEvent
			for len(pending) >= eventSize {
				e := pending[:eventSize]
				active = active || isActivity(e)
				typ, code := binary.LittleEndian.Uint16(e[16:18]), binary.LittleEndian.Uint16(e[18:20])
				value := int32(binary.LittleEndian.Uint32(e[20:24]))
				if typ == evKey {
					if name, ok := codes[code]; ok {
						switch value {
						case 1: // pressed (2 is a repeat)
							ev.Buttons = append(ev.Buttons, name)
						case 0:
							ev.Released = append(ev.Released, name)
						}
					}
				}
				if codes != nil {
					if nav := navOf(typ, code, value); nav != "" {
						ev.Nav = append(ev.Nav, nav)
					}
				}
				pending = pending[eventSize:]
			}
			if active {
				fn(ev) // one call per read, however many events it held
			}
		}
		if err != nil {
			return
		}
	}
}

// navOf reads one controller event as navigation, or "".
func navOf(typ, code uint16, value int32) string {
	switch {
	case typ == evKey && value == 1:
		return navButtons[code]
	case typ == evAbs && code == absHat0X && value != 0:
		if value < 0 {
			return "left"
		}
		return "right"
	case typ == evAbs && code == absHat0Y && value != 0:
		if value < 0 {
			return "up"
		}
		return "down"
	}
	return ""
}
