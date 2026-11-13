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
	if x2 <= x || x2 >= 1-300/irWidth {
		t.Errorf("mirrored and smoothed: %v then %v", x, x2)
	}
	for i := 0; i < 40; i++ {
		x2, _, _ = tr.Update(bar(300), t0.Add(time.Duration(20+i)*time.Millisecond))
	}
	if math.Abs(x2-(1-300/irWidth)) > 0.01 {
		t.Errorf("it settles on the target: %v", x2)
	}
	// One dot: the position is held. None for a while: it is gone.
	one := [4]irDot{{100, 100, true}}
	if x3, _, shown := tr.Update(one, t0.Add(100*time.Millisecond)); !shown || x3 != x2 {
		t.Errorf("one dot holds: %v %v", x3, shown)
	}
	if _, _, shown := tr.Update([4]irDot{}, t0.Add(time.Second)); shown {
		t.Error("lost after a while")
	}
	// Back again: it starts where it points, not from where it was.
	x4, _, shown := tr.Update(bar(900), t0.Add(2*time.Second))
	if !shown || math.Abs(x4-(1-900/irWidth)) > 0.01 {
		t.Errorf("back: %v %v", x4, shown)
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

func (h *wiiHarness) take() []InputEvent { ev := h.events; h.events = nil; return ev }

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
