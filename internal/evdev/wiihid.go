package evdev

import (
	"context"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Pointer is where a pointing device (a Wii Remote's IR camera) points on
// the screen, as shares of its width and height.
type Pointer struct {
	X, Y    float64
	Visible bool // false: it points away from the screen
	Down    bool // its button is held
}

const (
	wiiSilent       = 5 * time.Second       // a remote this long without a report is gone
	wiiPointerEvery = 30 * time.Millisecond // pointer events are sent at most this often
	wiiPointerStep  = 0.004                 // ...and only when it moved this much
	wiiRescan       = 2 * time.Second       // how often hidraw is looked at again
	wiiPause        = 15 * time.Millisecond // between the writes that set a remote up
)

// wiiRemote drives one Wii Remote over its raw HID channel: it sets the
// remote up when it is heard from, and turns its reports into what any
// controller gives: buttons, menu verbs and, for the IR camera, a pointer.
type wiiRemote struct {
	rw    io.Writer
	nav   *Navigator
	codes map[uint16]string // evdev code -> bindable button, for the binding table
	slot  func() int        // the remote's player slot; also connects it
	emit  func(InputEvent)
	now   func() time.Time
	pause func(time.Duration)

	mu         sync.Mutex
	configured bool
	core       uint16
	irReady    bool
	extPlugged bool
	nunchuk    bool // an extension is plugged in and is a Nunchuk
	c, z       bool
	sx, sy     int32
	tracker    pointerTracker
	ptr        Pointer
	ptrAt      time.Time
}

// send writes output reports one after another.
func (w *wiiRemote) send(reports ...[]byte) {
	for _, r := range reports {
		if _, err := w.rw.Write(r); err != nil {
			return
		}
		w.pause(wiiPause)
	}
}

// Gone forgets everything about the remote: it went quiet (switched off,
// or out of range of the DolphinBar), and will need setting up again.
func (w *wiiRemote) Gone() {
	w.mu.Lock()
	was := w.configured
	w.configured, w.irReady, w.extPlugged, w.nunchuk = false, false, false, false
	w.core, w.c, w.z, w.sx, w.sy = 0, false, false, 0, 0
	w.tracker = pointerTracker{}
	hadPointer := w.ptr.Visible
	w.ptr = Pointer{}
	w.mu.Unlock()
	if !was {
		return
	}
	w.nav.Reset()
	if hadPointer {
		w.emit(InputEvent{Pointer: &Pointer{}})
	}
}

// Handle takes one report as read from hidraw.
func (w *wiiRemote) Handle(b []byte) {
	r, ok := parseWiiReport(b)
	if !ok {
		return
	}
	w.mu.Lock()
	first := !w.configured
	w.configured = true
	w.mu.Unlock()
	if first {
		w.slot() // connected: it has a player slot now
		w.send(wiiStatusRequest())
	}
	var ev InputEvent
	switch r.ID {
	case wiiInStatus:
		w.configure(r.Extension)
	case wiiInRead:
		w.mu.Lock()
		w.nunchuk = w.extPlugged && isNunchuk(r.Data)
		w.mu.Unlock()
	}
	w.buttons(r.Core, &ev)
	if r.ID == wiiInIRExt {
		w.extension(r.Ext, &ev)
		w.pointer(r, &ev)
	}
	if len(ev.Buttons)+len(ev.Released)+len(ev.Nav) > 0 || ev.Pointer != nil {
		w.emit(ev)
	}
}

// configure is the reply to a status request, and what a remote sends when
// an extension is plugged in or out (which also resets its reporting).
func (w *wiiRemote) configure(extension bool) {
	w.mu.Lock()
	irReady, plugged := w.irReady, w.extPlugged
	w.irReady, w.extPlugged = true, extension
	if !extension {
		w.nunchuk = false
	}
	w.mu.Unlock()
	w.send(wiiLEDReport(w.slot()))
	if !irReady {
		w.send(wiiIRInit()...)
	}
	if extension && !plugged {
		w.send(wiiExtensionInit()...)
	}
	w.send(wiiReporting())
}

// buttons turns changes in the core buttons into key events for the
// Navigator and the binding table.
func (w *wiiRemote) buttons(core uint16, ev *InputEvent) {
	w.mu.Lock()
	prev := w.core
	w.core = core
	w.mu.Unlock()
	for _, b := range wiiButtons {
		was, is := prev&b.mask != 0, core&b.mask != 0
		if was != is {
			w.key(b.code, is, ev)
		}
	}
}

func (w *wiiRemote) key(code uint16, down bool, ev *InputEvent) {
	v := int32(0)
	if down {
		v = 1
	}
	ev.Nav = append(ev.Nav, w.nav.Key(code, v)...)
	if name, ok := w.codes[code]; ok {
		if down {
			ev.Buttons = append(ev.Buttons, name)
		} else {
			ev.Released = append(ev.Released, name)
		}
	}
}

// extension reads a Nunchuk's stick and buttons, if one is plugged in.
func (w *wiiRemote) extension(b []byte, ev *InputEvent) {
	w.mu.Lock()
	on := w.nunchuk
	prevC, prevZ, prevX, prevY := w.c, w.z, w.sx, w.sy
	w.mu.Unlock()
	if !on {
		return
	}
	x, y, c, z := nunchukState(b)
	if c != prevC {
		w.key(wiiNunchukC, c, ev)
	}
	if z != prevZ {
		w.key(wiiNunchukZ, z, ev)
	}
	if x != prevX {
		ev.Nav = append(ev.Nav, w.nav.Abs(wiiAbsX, x)...)
	}
	if y != prevY {
		ev.Nav = append(ev.Nav, w.nav.Abs(wiiAbsY, y)...)
	}
	w.mu.Lock()
	w.c, w.z, w.sx, w.sy = c, z, x, y
	w.mu.Unlock()
}

// pointer follows the IR camera. A pointer event is sent when it appears,
// goes, is clicked or let go, and as it moves (a little, and not too often).
func (w *wiiRemote) pointer(r wiiReport, ev *InputEvent) {
	now := w.now()
	w.mu.Lock()
	defer w.mu.Unlock()
	x, y, shown := w.tracker.Update(r.Dots, now)
	down := r.Core&wiiA != 0
	next := Pointer{X: x, Y: y, Visible: shown, Down: down}
	if !shown {
		next = Pointer{Down: down}
	}
	prev := w.ptr
	changed := next.Visible != prev.Visible || next.Down != prev.Down
	moved := next.Visible && (math.Abs(next.X-prev.X) > wiiPointerStep || math.Abs(next.Y-prev.Y) > wiiPointerStep)
	if !changed && (!moved || now.Sub(w.ptrAt) < wiiPointerEvery) {
		return
	}
	if !next.Visible && !prev.Visible {
		w.ptr = next // a click with no pointer: nothing to tell the screen
		return
	}
	w.ptr, w.ptrAt = next, now
	ev.Pointer = &next
}

// --- finding remotes ---

// Busy tells whether an app that reads a raw device itself runs.
func procBusy(app string) bool {
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		if e.Name() == "" || e.Name()[0] < '0' || e.Name()[0] > '9' {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "comm"))
		if err == nil && strings.TrimSpace(string(b)) == app {
			return true
		}
	}
	return false
}

// watchWii reads the Wii Remotes of raw devices whose profile has a
// reader of its own (the DolphinBar's four channels), while the app that
// reads them otherwise (Dolphin) is not running: then it is Dolphin's, and
// hostd neither reads nor writes them. It sees devices come and go by
// looking at /dev every few seconds.
func (e *Evdev) watchWii(ctx context.Context, fn func(InputEvent), profiles *Profiles, sys sysfs) {
	busy := e.Busy
	if busy == nil {
		busy = procBusy
	}
	type running struct{ stop func() }
	open := map[string]running{}
	var wg sync.WaitGroup
	defer wg.Wait()
	defer func() {
		for _, r := range open {
			r.stop()
		}
	}()
	scan := func() {
		paths, _ := filepath.Glob(filepath.Join(sys.root, "dev/hidraw*"))
		seen := map[string]bool{}
		for _, p := range paths {
			dev := "/dev/" + filepath.Base(p)
			info := sys.hidraw(dev)
			prof := profiles.forRaw(info)
			if prof == nil || prof.Reader != ReaderWiimote {
				continue
			}
			seen[p] = true
			if busy(prof.App) {
				if r, ok := open[p]; ok {
					r.stop()
					delete(open, p)
				}
				continue
			}
			if _, ok := open[p]; ok {
				continue
			}
			f, err := os.OpenFile(p, os.O_RDWR, 0)
			if err != nil {
				continue // not ours to read (permissions), or gone
			}
			rctx, cancel := context.WithCancel(ctx)
			stop := func() { cancel(); f.Close() }
			open[p] = running{stop}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer stop()
				e.readWii(rctx, f, DeviceID(info, dev), prof, fn)
			}()
		}
		for p, r := range open { // unplugged
			if !seen[p] {
				r.stop()
				delete(open, p)
			}
		}
	}
	scan()
	t := time.NewTicker(wiiRescan)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			scan()
		}
	}
}

// readWii reads one remote's channel until it fails or ctx ends.
func (e *Evdev) readWii(ctx context.Context, f *os.File, id string, prof *Profile, fn func(InputEvent)) {
	slot := func() int {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.players().Connect(id)
	}
	emit := func(ev InputEvent) {
		ev.Device, ev.Family = id, prof.Family()
		e.mu.Lock()
		ev.Player = e.players().Slot(id)
		e.mu.Unlock()
		fn(ev)
	}
	var remote *wiiRemote
	nav := NewNavigator(prof, e.Nav, func(verbs []string) { emit(InputEvent{Nav: verbs}) })
	remote = &wiiRemote{rw: f, nav: nav, codes: prof.codes, slot: slot, emit: emit, now: time.Now, pause: time.Sleep}
	var lost *time.Timer
	defer func() {
		if lost != nil {
			lost.Stop()
		}
		remote.Gone()
		e.mu.Lock()
		e.players().Disconnect(id)
		e.mu.Unlock()
	}()
	stop := context.AfterFunc(ctx, func() { f.Close() })
	defer stop()
	buf := make([]byte, 64)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if lost == nil {
				lost = time.AfterFunc(wiiSilent, remote.Gone)
			}
			lost.Reset(wiiSilent)
			remote.Handle(buf[:n])
		}
		if err != nil {
			return
		}
	}
}
