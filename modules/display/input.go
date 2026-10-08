package display

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// Input reports that someone touched an input device. The display module
// uses it to tell whether a person is at the screen, and to open the
// menu with a controller's Guide button.
type Input interface {
	// Watch calls fn on input activity until ctx ends. Devices plugged in
	// later (a controller switched on) are picked up.
	Watch(ctx context.Context, fn func(InputEvent)) error
}

// InputEvent is a batch of input from one device.
type InputEvent struct {
	// Buttons pressed, by name (see Buttons), for the binding table.
	Buttons []string
}

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
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { mu.Lock(); delete(open, path); mu.Unlock() }()
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
// the device's bindable buttons.
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
				if binary.LittleEndian.Uint16(e[16:18]) == evKey && int32(binary.LittleEndian.Uint32(e[20:24])) == 1 { // pressed, not released or repeated
					if name, ok := codes[binary.LittleEndian.Uint16(e[18:20])]; ok {
						ev.Buttons = append(ev.Buttons, name)
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
