package evdev

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Evdev codes, for the tests' event tables.
const (
	btnSouth, btnEast, btnNorth, btnWest  = 0x130, 0x131, 0x133, 0x134
	btnX, btnY                            = 0x133, 0x134 // xpad's names for north and west
	btnTL, btnTR, btnTL2, btnTR2          = 0x136, 0x137, 0x138, 0x139
	btnSelect, btnStart                   = 0x13a, 0x13b
	btnThumbL                             = 0x13d
	btnDpadUp                             = 0x220
	absX, absY, absZ, absRX, absRY, absRZ = 0, 1, 2, 3, 4, 5
	absHat0Y                              = 0x11
	keyUp, keyDown                        = 103, 108
)

// fakeClock is a clock and a timer queue the tests drive by hand.
type fakeClock struct {
	mu      sync.Mutex
	t       time.Time
	pending []*fakeTimer
}

type fakeTimer struct {
	at   time.Time
	f    func()
	dead bool
}

func newClock() *fakeClock { return &fakeClock{t: time.Unix(1000, 0)} }

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *fakeClock) After(d time.Duration, f func()) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.t.Add(d), f: f}
	c.pending = append(c.pending, t)
	return func() { c.mu.Lock(); t.dead = true; c.mu.Unlock() }
}

// Advance moves time on, running the timers that come due on the way.
func (c *fakeClock) Advance(d time.Duration) {
	end := c.Now().Add(d)
	for {
		c.mu.Lock()
		var next *fakeTimer
		for _, t := range c.pending {
			if !t.dead && !t.at.After(end) && (next == nil || t.at.Before(next.at)) {
				next = t
			}
		}
		if next == nil {
			c.t = end
			c.mu.Unlock()
			return
		}
		next.dead = true
		c.t = next.at
		c.mu.Unlock()
		next.f()
	}
}

// padFor makes the Navigator of a built-in profile on a fake clock; got
// collects the verbs of repeats.
func padFor(t *testing.T, id string, nav map[string]string) (*Navigator, *fakeClock, *[]string) {
	t.Helper()
	ps, err := LoadProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	var p *Profile
	for _, c := range ps.list {
		if c.ID == id {
			p = c
		}
	}
	if p == nil {
		t.Fatalf("no profile %q", id)
	}
	clock := newClock()
	var got []string
	var mu sync.Mutex
	n := NewNavigator(p, nav, func(v []string) { mu.Lock(); got = append(got, v...); mu.Unlock() })
	n.now, n.after = clock.Now, clock.After
	return n, clock, &got
}

type ev struct {
	typ  uint16 // evKey or evAbs
	code uint16
	val  int32
}

func key(code uint16, val int32) ev { return ev{evKey, code, val} }
func abs(code uint16, val int32) ev { return ev{evAbs, code, val} }

func feed(n *Navigator, events ...ev) []string {
	var out []string
	for _, e := range events {
		if e.typ == evKey {
			out = append(out, n.Key(e.code, e.val)...)
		} else {
			out = append(out, n.Abs(e.code, e.val)...)
		}
	}
	return out
}

func eq(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: %q, want %q", what, got, want)
	}
}

// What every family's buttons do, from its own codes: the same menu, on
// controllers whose codes differ (an Xbox pad's X is the code a PlayStation
// pad's triangle is on; a Switch's A is on the right).
func TestEveryFamilyDrivesTheMenu(t *testing.T) {
	for _, c := range []struct {
		profile string
		// codes of: confirm, back, west (actions), north (close), L, R,
		// start (actions), select (home)
		confirm, back, west, north, l, r, start, sel uint16
	}{
		{"xbox", btnSouth, btnEast, btnX, btnY, btnTL, btnTR, btnStart, btnSelect},
		{"playstation", btnSouth, btnEast, btnWest, btnNorth, btnTL, btnTR, btnStart, btnSelect},
		{"switch", btnEast, btnSouth, btnWest, btnNorth, btnTL, btnTR, btnStart, btnSelect},
		{"wii-classic", btnSouth, btnEast, btnWest, btnNorth, btnTL, btnTR, btnStart, btnSelect}, // BTN_A (0x130) is its right-hand A
	} {
		n, _, _ := padFor(t, c.profile, nil)
		for _, k := range []struct {
			code uint16
			want string
		}{
			{c.confirm, "choose"}, {c.back, "back"}, {c.west, "actions"}, {c.north, "close"},
			{c.l, "prev_tab"}, {c.r, "next_tab"}, {c.start, "actions"}, {c.sel, "home"}, {btnDpadUp, "up"},
		} {
			eq(t, c.profile+" press", feed(n, key(k.code, 1)), []string{k.want})
			feed(n, key(k.code, 0))
		}
	}
}

func TestWiiRemoteAlone(t *testing.T) {
	n, _, _ := padFor(t, "wii-remote", nil)
	// A confirms, B goes back, 2 closes, 1 opens the actions, + too, the d-pad moves.
	for _, k := range []struct {
		code uint16
		want string
	}{{0x130, "choose"}, {0x131, "back"}, {0x102, "close"}, {0x101, "actions"}, {btnStart, "actions"},
		{keyUp, "up"}, {keyDown, "down"}, {105, "left"}, {106, "right"}} {
		eq(t, "wii remote", feed(n, key(k.code, 1)), []string{k.want})
		feed(n, key(k.code, 0))
	}
}

func TestNunchukStickAndButtons(t *testing.T) {
	n, _, _ := padFor(t, "wii-nunchuk", nil)
	eq(t, "C", feed(n, key(0x132, 1)), []string{"prev_tab"})
	feed(n, key(0x132, 0))
	eq(t, "Z", feed(n, key(0x135, 1)), []string{"next_tab"})
	feed(n, key(0x135, 0))
	eq(t, "stick up", feed(n, abs(absY, -90)), []string{"up"})
	eq(t, "stick back to centre", feed(n, abs(absY, 0)), nil)
	eq(t, "stick right", feed(n, abs(absX, 80)), []string{"right"})
}

func TestSticksHaveADeadzoneAndHysteresis(t *testing.T) {
	n, _, _ := padFor(t, "xbox", nil)
	// Resting, a little drift, a half push: nothing. Past 60 %: a verb, once.
	eq(t, "drift", feed(n, abs(absX, 3000), abs(absY, -2000), abs(absX, 12000)), nil)
	eq(t, "push", feed(n, abs(absX, 22000)), []string{"right"})
	eq(t, "pushed harder", feed(n, abs(absX, 32767)), nil)
	// Easing off to 50 % is still pushed (hysteresis): no flicker.
	eq(t, "easing", feed(n, abs(absX, 16500), abs(absX, 21000)), nil)
	eq(t, "let go", feed(n, abs(absX, 8000)), nil)
	eq(t, "again", feed(n, abs(absX, 22000)), []string{"right"})
	// Through the centre to the other side.
	eq(t, "flip", feed(n, abs(absX, -22000)), []string{"left"})
	// Up is negative y.
	eq(t, "up", feed(n, abs(absX, 0), abs(absY, -30000)), []string{"up"})
	eq(t, "down", feed(n, abs(absY, 30000)), []string{"down"})
}

func TestRightStickPagesAndChangesCategory(t *testing.T) {
	n, _, _ := padFor(t, "playstation", nil) // sticks are 0..255
	eq(t, "right stick up", feed(n, abs(absRY, 10)), []string{"page_up"})
	eq(t, "centre", feed(n, abs(absRY, 128)), nil)
	eq(t, "right stick right", feed(n, abs(absRX, 250)), []string{"next_tab"})
	eq(t, "left stick", feed(n, abs(absX, 5)), []string{"left"})
}

func TestTriggersPageOnceEvenWhenAnAxisAndAButton(t *testing.T) {
	// A DualSense sends L2 as an axis and as a button: one press.
	n, _, _ := padFor(t, "playstation", nil)
	got := feed(n, abs(absZ, 20), abs(absZ, 140), key(btnTL2, 1), abs(absZ, 255))
	eq(t, "L2", got, []string{"page_up"})
	eq(t, "released", feed(n, abs(absZ, 10), key(btnTL2, 0)), nil)
	eq(t, "R2 as an axis only", feed(n, abs(absRZ, 200)), []string{"page_down"})
	// An Xbox trigger has its own range.
	x, _, _ := padFor(t, "xbox", nil)
	eq(t, "light pull", feed(x, abs(absZ, 300)), nil)
	eq(t, "a pull", feed(x, abs(absZ, 700)), []string{"page_up"})
}

func TestTheHatIsTheDpad(t *testing.T) {
	n, _, _ := padFor(t, "xbox", nil)
	eq(t, "left", feed(n, abs(absHat0X, -1)), []string{"left"})
	eq(t, "released", feed(n, abs(absHat0X, 0)), nil)
	eq(t, "down", feed(n, abs(absHat0Y, 1)), []string{"down"})
	eq(t, "diagonal", feed(n, abs(absHat0X, 1)), []string{"right"})
}

func TestHeldDirectionsRepeat(t *testing.T) {
	n, clock, got := padFor(t, "xbox", nil)
	eq(t, "press", feed(n, abs(absHat0Y, 1)), []string{"down"})
	clock.Advance(repeatFirst - time.Millisecond)
	eq(t, "before the pause is over", *got, nil)
	clock.Advance(time.Millisecond)
	eq(t, "first repeat", *got, []string{"down"})
	clock.Advance(3 * repeatEvery)
	eq(t, "steady", *got, []string{"down", "down", "down", "down"})
	feed(n, abs(absHat0Y, 0))
	clock.Advance(time.Second)
	if len(*got) != 4 {
		t.Fatalf("repeated after release: %q", *got)
	}
}

func TestChoosingNeverRepeats(t *testing.T) {
	n, clock, got := padFor(t, "xbox", nil)
	feed(n, key(btnSouth, 1), key(btnEast, 1), key(btnStart, 1))
	clock.Advance(5 * time.Second)
	eq(t, "no repeats of choose, back, actions", *got, nil)
}

func TestAStickRepeatsAndStopsWhenReleased(t *testing.T) {
	n, clock, got := padFor(t, "xbox", nil)
	feed(n, abs(absY, 30000))
	clock.Advance(repeatFirst + 2*repeatEvery)
	eq(t, "repeats", *got, []string{"down", "down", "down"})
	feed(n, abs(absY, 0))
	clock.Advance(time.Second)
	eq(t, "stopped", *got, []string{"down", "down", "down"})
}

func TestTwoHeldDirectionsRepeatTogether(t *testing.T) {
	n, clock, got := padFor(t, "xbox", nil)
	feed(n, abs(absHat0Y, 1), key(btnTR, 1)) // down and next_tab
	clock.Advance(repeatFirst)
	if len(*got) != 2 {
		t.Fatalf("repeats %q", *got)
	}
}

func TestResetLetsGoOfEverything(t *testing.T) {
	n, clock, got := padFor(t, "xbox", nil)
	feed(n, abs(absHat0Y, 1))
	n.Reset() // the controller went away with the button held
	clock.Advance(time.Second)
	eq(t, "no repeats after a disconnect", *got, nil)
}

func TestNavOverrides(t *testing.T) {
	n, _, _ := padFor(t, "xbox", map[string]string{"south": "back", "ls": "", "rs": "home", "lt": "up"})
	eq(t, "south is back now", feed(n, key(btnSouth, 1)), []string{"back"})
	eq(t, "ls is off", feed(n, key(btnThumbL, 1)), nil)
	eq(t, "rs", feed(n, key(0x13e, 1)), []string{"home"})
	eq(t, "lt", feed(n, abs(absZ, 900)), []string{"up"})
	if err := ValidateNav(map[string]string{"south": "back", "ls": ""}); err != nil {
		t.Error(err)
	}
	for _, bad := range []map[string]string{{"banana": "up"}, {"south": "fly"}} {
		if err := ValidateNav(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestEveryBuiltinProfileResolves(t *testing.T) {
	ps, err := LoadProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"xbox": "xbox", "playstation": "playstation", "switch": "nintendo",
		"wii-remote": "wii", "wii-nunchuk": "wii", "wii-classic": "nintendo", "8bitdo": "generic", "gamepad": "generic"}
	for _, p := range append(ps.list, ps.generic) {
		if p.Raw {
			continue
		}
		if w, ok := want[p.ID]; !ok || p.Family() != w {
			t.Errorf("profile %s: family %q, want %q", p.ID, p.Family(), w)
		}
	}
}

func TestBadPadTables(t *testing.T) {
	base := "name = \"x\"\nmatch = { vendor = \"1\" }\n"
	for body, want := range map[string]string{
		"[pad]\nfamily = \"sega\"\n":                                 "pad.family",
		"[pad]\nconfirm = \"south\"\nback = \"south\"\n":             "two different face buttons",
		"[pad.keys]\nBTN_NOPE = \"south\"\n":                         "unknown key",
		"[pad.keys]\nBTN_SOUTH = \"jump\"\n":                         "not a button",
		"[pad.axes]\nABS_X = { as = \"lx\", min = 5, max = 5 }\n":    "max must be above min",
		"[pad.axes]\nABS_X = { as = \"wheel\", min = 0, max = 5 }\n": "not an axis",
		"[pad.axes]\nABS_NOPE = { as = \"lx\", min = 0, max = 5 }\n": "unknown axis",
	} {
		dir := t.TempDir()
		writeFile(t, dir, "bad.toml", base+body)
		if _, err := LoadProfiles(dir); err == nil || !contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", body, err, want)
		}
	}
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// Through the reader: a batch of raw events becomes verbs in InputEvent.Nav,
// and a device with no profile (a keyboard) never navigates.
func TestReadEventsNav(t *testing.T) {
	nav, _, _ := padFor(t, "xbox", nil)
	var stream []byte
	for _, e := range []ev{key(btnSouth, 1), key(btnSouth, 0), abs(absHat0Y, -1), abs(absHat0Y, 0), key(btnEast, 1),
		key(btnX, 1), key(btnTR, 1), abs(absRY, 30000), abs(absZ, 900), key(btnSelect, 1)} {
		stream = append(stream, inputEvent(e.typ, e.code, e.val)...)
	}
	stream = append(stream, inputEvent(0, 0, 0)...)
	var got []string
	readEvents(&chunked{data: stream, n: 1000}, map[uint16]string{btnMode: "guide"}, nav, func(e InputEvent) { got = append(got, e.Nav...) })
	want := []string{"choose", "up", "back", "actions", "next_tab", "page_down", "page_up", "home"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nav %q, want %q", got, want)
	}
	got = nil
	readEvents(&chunked{data: stream, n: 1000}, nil, nil, func(e InputEvent) { got = append(got, e.Nav...) })
	if len(got) != 0 {
		t.Fatalf("a keyboard navigated: %q", got)
	}
}

// Every button of the pad can be bound, by its canonical name.
func TestPadButtonsReachBindings(t *testing.T) {
	ps, _ := LoadProfiles("")
	p := ps.forInput(deviceInfo{Vendor: "054c", Gamepad: true})
	var got []string
	readEvents(&chunked{data: append(append(inputEvent(evKey, btnTL, 1), inputEvent(evKey, btnNorth, 1)...), inputEvent(0, 0, 0)...), n: 1000},
		p.codes, nil, func(e InputEvent) { got = append(got, e.Buttons...) })
	if !reflect.DeepEqual(got, []string{"lb", "north"}) {
		t.Fatalf("buttons %q", got)
	}
	// A profile that places "start" itself keeps its own placement.
	if p.codes[btnStart] != "start" {
		t.Fatalf("start: %v", p.codes)
	}
}
