package evdev

import (
	"bytes"
	"math"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// report37 builds an input report 0x37 as a remote sends it: core
// buttons, accelerometer, 10 bytes of IR (basic mode: two dots per five
// bytes) and 6 of extension.
func report37(core uint16, dots [4][2]int, ext []byte) []byte {
	b := make([]byte, 22)
	b[0], b[1], b[2] = wiiInIRExt, byte(core>>8), byte(core)
	for i := 0; i < 2; i++ {
		x1, y1, x2, y2 := dots[i*2][0], dots[i*2][1], dots[i*2+1][0], dots[i*2+1][1]
		p := b[6+i*5:]
		p[0], p[1] = byte(x1), byte(y1)
		p[2] = byte(y1>>8&3)<<6 | byte(x1>>8&3)<<4 | byte(y2>>8&3)<<2 | byte(x2>>8&3)
		p[3], p[4] = byte(x2), byte(y2)
	}
	copy(b[16:], ext)
	return b
}

var noDots = [4][2]int{{1023, 1023}, {1023, 1023}, {1023, 1023}, {1023, 1023}}

func TestParseCoreButtonsAndIR(t *testing.T) {
	// The bar's two ends at x 300 and 700, y 400 (10-bit values cross the byte).
	b := report37(wiiA|wiiUp, [4][2]int{{300, 400}, {700, 400}, {1023, 1023}, {1023, 1023}}, nil)
	r, ok := parseWiiReport(b)
	if !ok || r.Core != wiiA|wiiUp || !r.HasIR {
		t.Fatalf("report: %+v %v", r, ok)
	}
	want := [4]irDot{{300, 400, true}, {700, 400, true}, {1023, 1023, false}, {1023, 1023, false}}
	if r.Dots != want {
		t.Fatalf("dots %+v, want %+v", r.Dots, want)
	}
	if _, ok := parseWiiReport([]byte{wiiInIRExt, 0, 0}); ok {
		t.Error("a short report parsed")
	}
	if _, ok := parseWiiReport([]byte{0x99, 0, 0, 0, 0, 0, 0}); ok {
		t.Error("an unknown report parsed")
	}
	if r, ok := parseWiiReport([]byte{wiiInButtons, 0x08, 0x08}); !ok || r.Core != wiiUp|wiiA {
		t.Errorf("plain buttons: %+v", r)
	}
}

func TestParseStatusAndRead(t *testing.T) {
	r, ok := parseWiiReport([]byte{wiiInStatus, 0, 0, 0x02, 0, 0, 0x60})
	if !ok || !r.Extension {
		t.Fatalf("status %+v", r)
	}
	if r, _ := parseWiiReport([]byte{wiiInStatus, 0, 0, 0x00, 0, 0, 0x60}); r.Extension {
		t.Error("no extension reads as one")
	}
	id := []byte{0x00, 0x00, 0xa4, 0x20, 0x00, 0x00}
	read := append([]byte{wiiInRead, 0, 0, 0x50, 0xa4, 0xfa}, append(id, make([]byte, 10)...)...)
	r, ok = parseWiiReport(read)
	if !ok || !bytes.Equal(r.Data, id) || !isNunchuk(r.Data) {
		t.Fatalf("read %+v", r)
	}
	if isNunchuk([]byte{0, 0, 0xa4, 0x20, 0x01, 0x01}) { // a Classic Controller
		t.Error("a Classic is no Nunchuk")
	}
}

func TestOutputReports(t *testing.T) {
	if got := wiiLEDReport(2); !bytes.Equal(got, []byte{0x11, 0x20}) {
		t.Errorf("led %x", got)
	}
	if got := wiiLEDReport(0); !bytes.Equal(got, []byte{0x11, 0x00}) {
		t.Errorf("no slot %x", got)
	}
	if got := wiiReporting(); !bytes.Equal(got, []byte{0x12, 0x04, 0x37}) {
		t.Errorf("reporting %x", got)
	}
	w := wiiWriteRegister(0xb00030, []byte{0x08})
	if len(w) != 22 || !bytes.Equal(w[:7], []byte{0x16, 0x04, 0xb0, 0x00, 0x30, 0x01, 0x08}) {
		t.Errorf("write %x", w)
	}
	if got := wiiReadRegister(0xa400fa, 6); !bytes.Equal(got, []byte{0x17, 0x04, 0xa4, 0x00, 0xfa, 0x00, 0x06}) {
		t.Errorf("read %x", got)
	}
	init := wiiIRInit()
	if len(init) != 7 || init[0][0] != wiiOutIR || init[6][5] != 1 || init[6][6] != 0x08 {
		t.Errorf("ir init: %x", init)
	}
}

func TestNunchukState(t *testing.T) {
	x, y, c, z := nunchukState([]byte{255, 128, 0, 0, 0, 0x03}) // right, centred, nothing held
	if x != 99 || y != 0 || c || z {
		t.Errorf("right: %d %d %v %v", x, y, c, z)
	}
	x, y, c, z = nunchukState([]byte{0, 255, 0, 0, 0, 0x00}) // left, up, both held
	if x != -100 || y != -99 || !c || !z {
		t.Errorf("left up: %d %d %v %v", x, y, c, z)
	}
	if x, y, c, z := nunchukState(nil); x != 0 || y != 0 || c || z {
		t.Error("nothing")
	}
}

func TestPointerTracker(t *testing.T) {
	var tr pointerTracker
	t0 := time.Unix(1000, 0)
	bar := func(x int) [4]irDot {
		return [4]irDot{{x - 100, 384, true}, {x + 100, 384, true}, {}, {}}
	}
	// The bar in the middle of the camera: the pointer in the middle.
	x, y, shown := tr.Update(bar(512), t0)
	if !shown || math.Abs(x-0.5) > 0.01 || math.Abs(y-0.5) > 0.01 {
		t.Fatalf("centre: %v %v %v", x, y, shown)
	}
	// The bar seen further left (the remote turned right): the pointer moves right, smoothly.
	x2, _, _ := tr.Update(bar(300), t0.Add(10*time.Millisecond))
	want := 0.5 + (212.0/200)/defaultScreenW
	if x2 <= x || x2 >= want {
		t.Errorf("mirrored and smoothed: %v then %v (target %v)", x, x2, want)
	}
	for i := 0; i < 400; i++ {
		x2, _, _ = tr.Update(bar(300), t0.Add(time.Duration(20+i*10)*time.Millisecond))
	}
	if math.Abs(x2-want) > 0.01 {
		t.Errorf("it settles on the target: %v, want %v", x2, want)
	}
	// One dot: the position is held. None for a while: it is gone.
	one := [4]irDot{{100, 100, true}}
	at := t0.Add(4100 * time.Millisecond)
	if x3, _, shown := tr.Update(one, at); !shown || x3 != x2 {
		t.Errorf("one dot holds: %v %v", x3, shown)
	}
	if _, _, shown := tr.Update([4]irDot{}, at.Add(time.Second)); shown {
		t.Error("lost after a while")
	}
	// Back again: it starts where it points, not from where it was.
	x4, _, shown := tr.Update(bar(700), at.Add(2*time.Second))
	if !shown || math.Abs(x4-(0.5-(188.5/200)/defaultScreenW)) > 0.01 {
		t.Errorf("back: %v %v", x4, shown)
	}
}

// Stray light does not move the pointer, a tilted remote does not either,
// and the pointer is the same however far the bar is.
func TestPointerTrackerIgnoresStrayLightRollAndDistance(t *testing.T) {
	t0 := time.Unix(1000, 0)
	settle := func(dots [4]irDot) (float64, float64) {
		var tr pointerTracker
		var x, y float64
		for i := 0; i < 300; i++ {
			x, y, _ = tr.Update(dots, t0.Add(time.Duration(i*10)*time.Millisecond))
		}
		return x, y
	}
	bx, by := settle([4]irDot{{400, 384, true}, {600, 384, true}})
	// A stray dot beside the bar, once the bar is being followed.
	var tr pointerTracker
	var x, y float64
	for i := 0; i < 300; i++ {
		dots := [4]irDot{{400, 384, true}, {600, 384, true}}
		if i > 5 {
			dots[2] = irDot{880, 300, true}
		}
		x, y, _ = tr.Update(dots, t0.Add(time.Duration(i*10)*time.Millisecond))
	}
	if math.Abs(x-bx) > 0.005 || math.Abs(y-by) > 0.005 {
		t.Errorf("stray light moved it: %v,%v from %v,%v", x, y, bx, by)
	}
	// Far away: the bar half as wide and its middle half as far from the centre.
	ox, oy := 56.0, 0.0 // bar at (512+ox... ) offset of the middle from the camera centre
	_ = oy
	near, _ := settle([4]irDot{{int(512 + ox - 100), 384, true}, {int(512 + ox + 100), 384, true}})
	far, _ := settle([4]irDot{{int(512 + ox/2 - 50), 384, true}, {int(512 + ox/2 + 50), 384, true}})
	if math.Abs(near-far) > 0.01 {
		t.Errorf("distance moved it: %v vs %v", near, far)
	}
	// Rolled 20 degrees about the camera's centre: the same place.
	rolled := func(a float64) [4]irDot {
		c, s := math.Cos(a), math.Sin(a)
		rot := func(px, py float64) irDot {
			return irDot{int(math.Round(511.5 + px*c - py*s)), int(math.Round(383.5 + px*s + py*c)), true}
		}
		return [4]irDot{rot(-100+56, 0), rot(100+56, 0)}
	}
	flat, _ := settle(rolled(0))
	tilted, _ := settle(rolled(20 * math.Pi / 180))
	if math.Abs(flat-tilted) > 0.01 {
		t.Errorf("roll moved it: %v vs %v", flat, tilted)
	}
}

// The filter takes the shake out of a held pointer and still follows a
// real move without lag.
func TestPointerFilterSteadyAndQuick(t *testing.T) {
	t0 := time.Unix(1000, 0)
	rng := uint32(1)
	noise := func(n float64) int { // a repeatable +-n
		rng = rng*1664525 + 1013904223
		return int(float64(rng>>16&0xffff)/65535*2*n - n)
	}
	var tr pointerTracker
	var raw, out []float64
	for i := 0; i < 500; i++ {
		jx, jy := noise(3), noise(3)
		d := [4]irDot{{412 + jx, 384 + jy, true}, {612 + jx, 384 - jy, true}}
		x, _, _ := tr.Update(d, t0.Add(time.Duration(i*10)*time.Millisecond))
		if i > 100 {
			raw = append(raw, 0.5-(float64(512+jx)-irWidth/2)/200/defaultScreenW)
			out = append(out, x)
		}
	}
	sd := func(v []float64) float64 {
		m := 0.0
		for _, a := range v {
			m += a
		}
		m /= float64(len(v))
		s := 0.0
		for _, a := range v {
			s += (a - m) * (a - m)
		}
		return math.Sqrt(s / float64(len(v)))
	}
	if sd(out)*4 > sd(raw) {
		t.Errorf("shake %v is not at least 4x smaller than %v", sd(out), sd(raw))
	}
	// A step across the screen is mostly there within 100 ms.
	for i := 0; i < 10; i++ {
		tr.Update([4]irDot{{412, 384, true}, {612, 384, true}}, t0.Add(time.Duration(10000+i*10)*time.Millisecond))
	}
	var x float64
	before, _, _ := tr.Update([4]irDot{{412, 384, true}, {612, 384, true}}, t0.Add(10100*time.Millisecond))
	for i := 1; i <= 10; i++ {
		x, _, _ = tr.Update([4]irDot{{312, 384, true}, {512, 384, true}}, t0.Add(time.Duration(10100+i*10)*time.Millisecond))
	}
	target := before + 100.0/200/defaultScreenW
	if x < before+(target-before)*0.7 {
		t.Errorf("lags: %v of the way to %v from %v", x, target, before)
	}
}

func TestFitCalibration(t *testing.T) {
	// A screen 4 bar widths across and 2.5 down, centred at (0.3, -0.2).
	c, ok := FitCalibration([2]float64{2.3, -1.45}, [2]float64{-1.7, -1.45}, [2]float64{2.3, 1.05}, [2]float64{-1.7, 1.05})
	if !ok || math.Abs(c.W-4) > 1e-9 || math.Abs(c.H-2.5) > 1e-9 || math.Abs(c.CX-0.3) > 1e-9 || math.Abs(c.CY+0.2) > 1e-9 {
		t.Errorf("fit: %+v %v", c, ok)
	}
	if _, ok := FitCalibration([2]float64{0, 0}, [2]float64{0, 0}, [2]float64{0, 0}, [2]float64{0, 0}); ok {
		t.Error("four equal points are not a screen")
	}
	if _, ok := FitCalibration([2]float64{-2, 0}, [2]float64{2, 0}, [2]float64{-2, 1}, [2]float64{2, 1}); ok {
		t.Error("turned about is not a screen")
	}
	if got := (Calibration{}).Or(); got.W != defaultScreenW || got.H != defaultScreenH {
		t.Errorf("defaults: %+v", got)
	}
}

// --- a remote, on a fake channel ---

type fakeChannel struct {
	mu     sync.Mutex
	writes [][]byte
}

func (f *fakeChannel) Write(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, append([]byte(nil), b...))
	return len(b), nil
}

func (f *fakeChannel) sent() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.writes...)
}

type wiiHarness struct {
	ch     *fakeChannel
	remote *wiiRemote
	events []InputEvent
	now    time.Time
	slots  int
}

func newWiiHarness(t *testing.T) *wiiHarness {
	t.Helper()
	ps, err := LoadProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	var prof *Profile
	for _, p := range ps.list {
		if p.ID == "dolphinbar" {
			prof = p
		}
	}
	if prof == nil || prof.Reader != ReaderWiimote {
		t.Fatalf("no dolphinbar profile with a reader: %+v", prof)
	}
	h := &wiiHarness{ch: &fakeChannel{}, now: time.Unix(5000, 0)}
	nav := NewNavigator(prof, nil, func([]string) {})
	h.remote = &wiiRemote{rw: h.ch, nav: nav, codes: prof.codes, slot: func() int { h.slots++; return 2 },
		emit: func(ev InputEvent) { h.events = append(h.events, ev) },
		now:  func() time.Time { return h.now }, pause: func(time.Duration) {}}
	return h
}

func (h *wiiHarness) feed(b []byte) { h.remote.Handle(b); h.now = h.now.Add(10 * time.Millisecond) }

// take is what the remote did, without its coming and going (takeAll).
func (h *wiiHarness) take() []InputEvent {
	var out []InputEvent
	for _, e := range h.takeAll() {
		if e.Presence == "" {
			out = append(out, e)
		}
	}
	return out
}

func (h *wiiHarness) takeAll() []InputEvent { ev := h.events; h.events = nil; return ev }

func TestARemoteIsSetUpWhenHeardFrom(t *testing.T) {
	h := newWiiHarness(t)
	h.feed([]byte{wiiInButtons, 0, 0}) // any report: it is there
	if got := h.ch.sent(); len(got) != 1 || !bytes.Equal(got[0], wiiStatusRequest()) {
		t.Fatalf("first it is asked its status: %x", got)
	}
	h.feed([]byte{wiiInStatus, 0, 0, 0x00, 0, 0, 0x60}) // its reply: no extension
	sent := h.ch.sent()[1:]
	if !bytes.Equal(sent[0], []byte{0x11, 0x20}) { // player 2's LED
		t.Errorf("led: %x", sent[0])
	}
	if len(sent) != 1+7+1 || !bytes.Equal(sent[len(sent)-1], wiiReporting()) {
		t.Errorf("then the camera on and the reporting mode: %x", sent)
	}
	// Plugging in an extension resets its reporting: set again, extension initialised, the camera left on.
	before := len(h.ch.sent())
	h.feed([]byte{wiiInStatus, 0, 0, 0x02, 0, 0, 0x60})
	sent = h.ch.sent()[before:]
	if len(sent) != 1+3+1 || sent[1][1] != 0x04 || sent[1][2] != 0xa4 || !bytes.Equal(sent[len(sent)-1], wiiReporting()) {
		t.Errorf("extension: %x", sent)
	}
}

func TestButtonsBecomeNavAndBindings(t *testing.T) {
	h := newWiiHarness(t)
	h.feed([]byte{wiiInButtons, 0, 0})
	h.take()
	h.feed([]byte{wiiInButtons, byte(wiiDown >> 8), byte(wiiA)}) // d-pad down and A
	ev := h.take()
	if len(ev) != 1 || !reflect.DeepEqual(ev[0].Nav, []string{VerbDown, VerbChoose}) && !reflect.DeepEqual(ev[0].Nav, []string{VerbChoose, VerbDown}) {
		t.Fatalf("down and A: %+v", ev)
	}
	h.feed([]byte{wiiInButtons, 0, 0})
	h.take()
	h.feed([]byte{wiiInButtons, 0, byte(wiiHome)}) // Home is the Guide button: it opens the menu
	ev = h.take()
	if len(ev) != 1 || !reflect.DeepEqual(ev[0].Buttons, []string{"guide"}) {
		t.Fatalf("home: %+v", ev)
	}
	h.feed([]byte{wiiInButtons, 0, 0})
	if ev = h.take(); len(ev) != 1 || !reflect.DeepEqual(ev[0].Released, []string{"guide"}) {
		t.Fatalf("home released: %+v", ev)
	}
}

func TestThePointerFollowsTheCameraAndClicks(t *testing.T) {
	h := newWiiHarness(t)
	h.feed([]byte{wiiInButtons, 0, 0})
	h.take()
	bar := func(core uint16, x int) []byte {
		return report37(core, [4][2]int{{x - 100, 384}, {x + 100, 384}, {1023, 1023}, {1023, 1023}}, nil)
	}
	h.feed(bar(0, 512))
	ev := h.take()
	if len(ev) != 1 || ev[0].Pointer == nil || !ev[0].Pointer.Visible || math.Abs(ev[0].Pointer.X-0.5) > 0.01 {
		t.Fatalf("it appears: %+v", ev)
	}
	// A steady pointer says nothing, however many reports come.
	for i := 0; i < 20; i++ {
		h.feed(bar(0, 512))
	}
	if ev = h.take(); len(ev) != 0 {
		t.Fatalf("a steady pointer sent %d events", len(ev))
	}
	h.feed(bar(wiiA, 512)) // A is a click
	ev = h.take()
	if len(ev) != 1 || ev[0].Pointer == nil || !ev[0].Pointer.Down {
		t.Fatalf("click: %+v", ev)
	}
	// Moving fast sends at most one event per wiiPointerEvery.
	for i := 0; i < 20; i++ {
		h.feed(bar(wiiA, 512-i*10)) // 10 ms apart
	}
	n := 0
	for _, e := range h.take() {
		if e.Pointer != nil {
			n++
		}
	}
	if n == 0 || n > 20*10/int(wiiPointerEvery/time.Millisecond)+1 {
		t.Errorf("%d pointer events in 200 ms", n)
	}
	// Pointing away: it goes (after a moment).
	h.now = h.now.Add(time.Second)
	h.feed(report37(0, noDots, nil))
	ev = h.take()
	if len(ev) == 0 || ev[len(ev)-1].Pointer == nil || ev[len(ev)-1].Pointer.Visible {
		t.Fatalf("it goes: %+v", ev)
	}
}

func TestANunchukMovesTheMenu(t *testing.T) {
	h := newWiiHarness(t)
	h.feed([]byte{wiiInButtons, 0, 0})
	h.feed([]byte{wiiInStatus, 0, 0, 0x02, 0, 0, 0x60})
	id := []byte{0x00, 0x00, 0xa4, 0x20, 0x00, 0x00}
	h.feed(append([]byte{wiiInRead, 0, 0, 0x50, 0xa4, 0xfa}, append(id, make([]byte, 10)...)...))
	h.take()
	centred := []byte{128, 128, 0, 0, 0, 0x03}
	h.feed(report37(0, noDots, centred))
	h.take()
	h.feed(report37(0, noDots, []byte{128, 128, 0, 0, 0, 0x02})) // Z held (0 = pressed)
	ev := h.take()
	if len(ev) != 1 || !reflect.DeepEqual(ev[0].Buttons, []string{"rb"}) {
		t.Fatalf("Z: %+v", ev)
	}
	h.feed(report37(0, noDots, []byte{128, 255, 0, 0, 0, 0x03})) // Z let go, the stick pushed up (255): the menu goes up
	ev = h.take()
	if len(ev) != 1 || !reflect.DeepEqual(ev[0].Nav, []string{VerbUp}) || !reflect.DeepEqual(ev[0].Released, []string{"rb"}) {
		t.Fatalf("stick up: %+v", ev)
	}
	// Pulled out: its bytes mean nothing any more.
	h.feed([]byte{wiiInStatus, 0, 0, 0x00, 0, 0, 0x60})
	h.take()
	h.feed(report37(0, noDots, []byte{255, 255, 0, 0, 0, 0x00}))
	if ev = h.take(); len(ev) != 0 {
		t.Fatalf("junk from an unplugged extension: %+v", ev)
	}
}

func TestARemoteThatGoesQuietIsForgotten(t *testing.T) {
	h := newWiiHarness(t)
	h.feed([]byte{wiiInButtons, 0, byte(wiiA)}) // held when it vanishes
	h.take()
	h.remote.Gone()
	// Heard again, it is set up again from the start.
	n := len(h.ch.sent())
	h.feed([]byte{wiiInButtons, 0, 0})
	if got := h.ch.sent()[n:]; len(got) != 1 || !bytes.Equal(got[0], wiiStatusRequest()) {
		t.Errorf("after gone: %x", got)
	}
}

func TestProcBusyFindsARunningProcessByName(t *testing.T) {
	b, err := os.ReadFile("/proc/self/comm")
	if err != nil {
		t.Skip("no /proc")
	}
	if !procBusy(strings.TrimSpace(string(b))) {
		t.Error("this process is not found")
	}
	if procBusy("no-such-process-hostd") {
		t.Error("a process that does not exist is found")
	}
}

// classicBytes builds a Classic Controller's 6 bytes: sticks and triggers as
// raw values, and the buttons held as a list of evdev codes.
func classicBytes(lx, ly, rx, ry, lt, rt int, held ...uint16) []byte {
	b := []byte{byte(lx&0x3f) | byte(rx>>3&3)<<6, byte(ly&0x3f) | byte(rx>>1&3)<<6,
		byte(rx&1)<<7 | byte(lt>>3&3)<<5 | byte(ry&0x1f), byte(lt&7)<<5 | byte(rt&0x1f), 0xff, 0xff}
	for _, c := range classicButtons {
		for _, h := range held {
			if h == c.code {
				b[c.byteN] &^= 1 << c.bit
			}
		}
	}
	return b
}

func TestClassicState(t *testing.T) {
	if !isClassic([]byte{0, 0, 0xa4, 0x20, 0x01, 0x01}) || isClassic([]byte{0, 0, 0xa4, 0x20, 0x00, 0x00}) {
		t.Error("ids")
	}
	held, axes := classicState(classicBytes(63, 0, 31, 31, 31, 0, 0x130, 0x13b))
	if !held[0x130] || !held[0x13b] || held[0x131] {
		t.Errorf("held: %v", held)
	}
	if axes[classicLX] != 31 || axes[classicLY] != 32 || axes[classicRX] != 15 || axes[classicRY] != -15 ||
		axes[classicLT] != 31 || axes[classicRT] != 0 {
		t.Errorf("axes: %v", axes)
	}
	if held, axes := classicState(nil); held != nil || axes != nil {
		t.Error("nothing")
	}
}

func TestAClassicControllerMovesTheMenu(t *testing.T) {
	h := newWiiHarness(t)
	h.feed([]byte{wiiInButtons, 0, 0})
	h.feed([]byte{wiiInStatus, 0, 0, 0x02, 0, 0, 0x60})
	id := []byte{0x00, 0x00, 0xa4, 0x20, 0x01, 0x01}
	h.feed(append([]byte{wiiInRead, 0, 0, 0x50, 0xa4, 0xfa}, append(id, make([]byte, 10)...)...))
	h.take()
	idle := classicBytes(32, 32, 16, 16, 0, 0)
	h.feed(report37(0, noDots, idle))
	h.take()
	h.feed(report37(0, noDots, classicBytes(32, 32, 16, 16, 0, 0, 0x130))) // A: choose
	ev := h.take()
	if len(ev) != 1 || !reflect.DeepEqual(ev[0].Nav, []string{VerbChoose}) || !reflect.DeepEqual(ev[0].Buttons, []string{"east"}) {
		t.Fatalf("A: %+v", ev)
	}
	h.feed(report37(0, noDots, classicBytes(32, 32, 16, 16, 0, 0, 0x221))) // d-pad down
	if ev = h.take(); len(ev) != 1 || !reflect.DeepEqual(ev[0].Nav, []string{VerbDown}) || !reflect.DeepEqual(ev[0].Released, []string{"east"}) {
		t.Fatalf("down: %+v", ev)
	}
	h.feed(report37(0, noDots, classicBytes(32, 63, 16, 16, 0, 0))) // the left stick pushed up (63 is up, as 255 is a Nunchuk's)
	if ev = h.take(); len(ev) != 1 || !reflect.DeepEqual(ev[0].Nav, []string{VerbUp}) {
		t.Fatalf("stick: %+v", ev)
	}
}

func TestAResetRemoteIsSetUpAgain(t *testing.T) {
	h := newWiiHarness(t)
	h.feed([]byte{wiiInButtons, 0, 0})
	h.feed([]byte{wiiInStatus, 0, 0, 0x00, 0, 0, 0x60})
	h.feed(report37(0, noDots, nil)) // set up: it reports in the mode asked for
	n := len(h.ch.sent())
	h.feed(report37(0, noDots, nil))
	if len(h.ch.sent()) != n {
		t.Fatal("a remote in the right mode was written to")
	}
	// It is switched off and on: it reports buttons only again.
	h.now = h.now.Add(2 * time.Second)
	h.feed([]byte{wiiInButtons, 0, 0})
	got := h.ch.sent()[n:]
	if len(got) != 1 || !bytes.Equal(got[0], wiiStatusRequest()) {
		t.Fatalf("not asked again: %x", got)
	}
	// Not again in the same second, however many reports come.
	for i := 0; i < 5; i++ {
		h.feed([]byte{wiiInButtons, 0, 0})
	}
	if len(h.ch.sent()) != n+1 {
		t.Errorf("asked %d times in a second", len(h.ch.sent())-n)
	}
	// Its answer sets it up, the camera included.
	h.feed([]byte{wiiInStatus, 0, 0, 0x00, 0, 0, 0x60})
	if got := h.ch.sent()[n+1:]; len(got) != 1+7+1 {
		t.Errorf("setup after the reset: %x", got)
	}
}

// A calibration: aim at the four corners and press A at each; the remote's
// buttons are the calibration's alone meanwhile, and the result is kept and
// used at once.
func TestCalibratingARemote(t *testing.T) {
	h := newWiiHarness(t)
	var saved *Calibration
	h.remote.saved = func(c *Calibration) { saved = c }
	h.feed([]byte{wiiInButtons, 0, 0})
	h.take()
	// Where the bar is on the camera when the remote points at a corner (its
	// middle, 200 wide): the screen is 4 bar widths across and 2.5 down, so
	// the corners are +-400 across and +-250 down from the camera's centre,
	// mirrored across.
	at := func(core uint16, cornerX, cornerY int) []byte {
		cx, cy := 512-cornerX, 384+cornerY
		return report37(core, [4][2]int{{cx - 100, cy}, {cx + 100, cy}, {1023, 1023}, {1023, 1023}}, nil)
	}
	h.remote.StartCalibration()
	if ev := h.take(); len(ev) != 1 || ev[0].Calibrate == nil || ev[0].Calibrate.State != "aim" || ev[0].Calibrate.Step != 0 {
		t.Fatalf("started: %+v", ev)
	}
	corners := [4][2]int{{-400, -250}, {400, -250}, {-400, 250}, {400, 250}}
	for i, c := range corners {
		for j := 0; j < 30; j++ { // aiming settles
			h.feed(at(0, c[0], c[1]))
		}
		h.take()
		h.feed(at(wiiA|wiiUp, c[0], c[1])) // A (and up: it is not navigation now)
		ev := h.take()
		var got *CalibrateEvent
		for _, e := range ev {
			if len(e.Nav)+len(e.Buttons) > 0 || (e.Pointer != nil && e.Pointer.Down) {
				t.Fatalf("corner %d: the buttons act while calibrating: %+v", i, e)
			}
			if e.Calibrate != nil {
				got = e.Calibrate
			}
		}
		if i < 3 && (got == nil || got.State != "aim" || got.Step != i+1) {
			t.Fatalf("corner %d: %+v", i, got)
		}
		if i == 3 && (got == nil || got.State != "done" || got.Fit == nil) {
			t.Fatalf("last corner: %+v", got)
		}
		h.feed(at(0, c[0], c[1])) // let go
		h.take()
	}
	fit := saved
	if fit == nil || math.Abs(fit.W-4) > 0.1 || math.Abs(fit.H-2.5) > 0.1 || math.Abs(fit.CX) > 0.05 || math.Abs(fit.CY) > 0.05 {
		t.Fatalf("kept: %+v", fit)
	}
	if h.remote.tracker.Cal != *fit {
		t.Error("the new calibration is not in use")
	}
	// Pointing at a corner now is that corner of the screen.
	var x, y float64
	for j := 0; j < 60; j++ {
		x, y, _ = h.remote.tracker.Update([4]irDot{{512 + 400 - 100, 384 + 250, true}, {512 + 400 + 100, 384 + 250, true}}, h.now.Add(time.Duration(j)*10*time.Millisecond))
	}
	if x > 0.03 || y < 0.97 {
		t.Errorf("bottom left corner reads %v, %v", x, y)
	}
	// Buttons are buttons again.
	h.feed(report37(wiiUp, noDots, nil))
	if ev := h.take(); len(ev) == 0 || len(ev[0].Nav) == 0 {
		t.Errorf("after: %+v", ev)
	}
}

func TestCalibrationCanBeCancelledAndNeedsTheBar(t *testing.T) {
	h := newWiiHarness(t)
	h.feed([]byte{wiiInButtons, 0, 0})
	h.take()
	h.remote.StartCalibration()
	h.take()
	h.feed(report37(wiiA, noDots, nil)) // A with no bar in sight
	ev := h.take()
	if len(ev) != 1 || ev[0].Calibrate == nil || ev[0].Calibrate.Step != 0 || ev[0].Calibrate.Reason == "" {
		t.Fatalf("no bar: %+v", ev)
	}
	h.feed(report37(0, noDots, nil))
	h.take()
	h.feed(report37(wiiB, noDots, nil))
	if ev = h.take(); len(ev) != 1 || ev[0].Calibrate == nil || ev[0].Calibrate.State != "cancelled" {
		t.Fatalf("B gives up: %+v", ev)
	}
	// Four points that are no screen fail, and nothing is kept.
	var saved *Calibration
	h.remote.saved = func(c *Calibration) { saved = c }
	h.remote.StartCalibration()
	h.take()
	for i := 0; i < 4; i++ {
		for j := 0; j < 5; j++ {
			h.feed(report37(0, [4][2]int{{412, 384}, {612, 384}, {1023, 1023}, {1023, 1023}}, nil))
		}
		h.feed(report37(wiiA, [4][2]int{{412, 384}, {612, 384}, {1023, 1023}, {1023, 1023}}, nil))
		h.feed(report37(0, [4][2]int{{412, 384}, {612, 384}, {1023, 1023}, {1023, 1023}}, nil))
	}
	var last *CalibrateEvent
	for _, e := range h.take() {
		if e.Calibrate != nil {
			last = e.Calibrate
		}
	}
	if last == nil || last.State != "failed" || saved != nil {
		t.Fatalf("four equal points: %+v saved %+v", last, saved)
	}
	// The remote going away while calibrating cancels it.
	h.remote.StartCalibration()
	h.take()
	h.remote.Gone()
	if ev = h.take(); len(ev) == 0 || ev[0].Calibrate == nil || ev[0].Calibrate.State != "cancelled" {
		t.Fatalf("gone: %+v", ev)
	}
}

// The DolphinBar sends an idle report for a slot with no remote: that is not
// a remote. One that sends anything else is there, once; it is gone when it
// has said so and falls silent.
func TestAnEmptySlotIsNotARemote(t *testing.T) {
	h := newWiiHarness(t)
	for i := 0; i < 5; i++ {
		if alive := h.remote.Handle([]byte{wiiInButtons, 0, 0}); alive {
			t.Fatal("an idle report shows a remote")
		}
	}
	if ev := h.takeAll(); len(ev) != 0 || h.slots != 0 {
		t.Fatalf("an empty slot connected: %+v, %d slots", ev, h.slots)
	}
	h.remote.Gone() // its timer never ran: nothing to say
	if ev := h.takeAll(); len(ev) != 0 {
		t.Fatalf("gone with nobody there: %+v", ev)
	}
	// A remote: it comes once, and goes once.
	for range 2 {
		if !h.remote.Handle(report37(0, noDots, nil)) {
			t.Fatal("a camera report shows a remote")
		}
	}
	here := 0
	for _, e := range h.takeAll() {
		if e.Presence == "here" {
			here++
		}
	}
	if here != 1 || h.slots != 1 {
		t.Fatalf("arrived %d times, %d slots", here, h.slots)
	}
	h.remote.Gone()
	ev := h.takeAll()
	if len(ev) == 0 || ev[len(ev)-1].Presence != "gone" {
		t.Fatalf("it should say it went: %+v", ev)
	}
	// A button press is a remote too, even before it is set up.
	h2 := newWiiHarness(t)
	if !h2.remote.Handle([]byte{wiiInButtons, 0, byte(wiiA)}) {
		t.Fatal("a pressed button shows a remote")
	}
}
