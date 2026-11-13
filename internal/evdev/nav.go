package evdev

import (
	"fmt"
	"slices"
	"sync"
	"time"
)

// Menu verbs: what a controller does to hostd's menu. The shell acts on
// these (and a keyboard on the same ones), so every input does the same.
const (
	VerbUp, VerbDown, VerbLeft, VerbRight = "up", "down", "left", "right"
	VerbChoose, VerbBack                  = "choose", "back"
	VerbActions, VerbClose                = "actions", "close"
	VerbPrevTab, VerbNextTab              = "prev_tab", "next_tab"
	VerbPageUp, VerbPageDown              = "page_up", "page_down"
	VerbHome                              = "home"
)

// NavVerbs are the verbs a [display.nav] entry may name ("" turns a
// button off for the menu).
var NavVerbs = []string{VerbUp, VerbDown, VerbLeft, VerbRight, VerbChoose, VerbBack, VerbActions, VerbClose,
	VerbPrevTab, VerbNextTab, VerbPageUp, VerbPageDown, VerbHome}

// Held directions repeat, as a keyboard's arrows do: a pause, then steady.
// Choosing and going back never repeat.
const (
	repeatFirst = 400 * time.Millisecond
	repeatEvery = 90 * time.Millisecond
	// A stick is "pushed" past stickOn and "let go" below stickOff: the gap
	// keeps a stick resting near the edge from flickering.
	stickOn, stickOff = 0.6, 0.4
	// The same for an analog trigger.
	triggerOn, triggerOff = 0.5, 0.3
)

var repeats = []string{VerbUp, VerbDown, VerbLeft, VerbRight, VerbPrevTab, VerbNextTab, VerbPageUp, VerbPageDown}

// DefaultNav says which verb each canonical button does. The face buttons
// that confirm and go back depend on the controller (see NewNavigator).
func DefaultNav() map[string]string {
	return map[string]string{
		BtnWest: VerbActions, BtnNorth: VerbClose, BtnStart: VerbActions, BtnSelect: VerbHome,
		BtnLB: VerbPrevTab, BtnRB: VerbNextTab, BtnLT: VerbPageUp, BtnRT: VerbPageDown,
		BtnLS: VerbChoose,
		BtnUp: VerbUp, BtnDown: VerbDown, BtnLeft: VerbLeft, BtnRight: VerbRight,
	}
}

// ValidateNav checks a [display.nav] table: buttons to verbs.
func ValidateNav(nav map[string]string) error {
	for button, verb := range nav {
		if !slices.Contains(padButtons, button) {
			return fmt.Errorf("nav: %q is not a button (use one of %v)", button, padButtons)
		}
		if verb != "" && !slices.Contains(NavVerbs, verb) {
			return fmt.Errorf("nav %s: %q is not a menu verb (use one of %v, or \"\" for none)", button, verb, NavVerbs)
		}
	}
	return nil
}

// A Navigator turns one controller's canonical buttons and axes into menu
// verbs: a button press is a verb; a stick pushed past its deadzone, or a
// trigger pulled, is one too; a held direction repeats. It is pure logic
// on a clock, so it is tested without a device.
type Navigator struct {
	pad  *padRuntime
	nav  map[string]string
	now  func() time.Time
	emit func(verbs []string) // repeats, from the timer's goroutine
	// after runs f once after d and returns what cancels it; time.AfterFunc
	// outside tests.
	after func(d time.Duration, f func()) (cancel func())

	mu      sync.Mutex
	origins map[string]map[string]bool // button -> what holds it ("key", "axis", "hat")
	axisDir map[string]int             // axis -> -1, 0 or 1: the push it is in now
	held    map[string]*hold           // a source ("btn:lb", "stick:lx-") -> its verb, while held
	cancel  func()
}

type hold struct {
	verb string
	next time.Time // zero: never repeats
}

// NewNavigator makes the Navigator for a controller of this profile. nav
// overrides DefaultNav (and the confirm and back buttons of the profile's
// family); emit gets the verbs of repeats.
func NewNavigator(p *Profile, nav map[string]string, emit func([]string)) *Navigator {
	n := &Navigator{
		pad: p.pad, now: time.Now, emit: emit,
		after:   func(d time.Duration, f func()) func() { t := time.AfterFunc(d, f); return func() { t.Stop() } },
		origins: map[string]map[string]bool{}, axisDir: map[string]int{}, held: map[string]*hold{},
	}
	n.nav = DefaultNav()
	n.nav[p.pad.confirm] = VerbChoose
	n.nav[p.pad.back] = VerbBack
	for button, verb := range nav {
		n.nav[button] = verb
	}
	return n
}

// Key reads one key event (value 1 pressed, 0 released; 2, a repeat, is
// ignored) and returns the verbs it causes now.
func (n *Navigator) Key(code uint16, value int32) []string {
	button, ok := n.pad.keys[code]
	if !ok || value == 2 {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.finish(n.setButton(button, "key", value == 1))
}

// Abs reads one axis event and returns the verbs it causes now.
func (n *Navigator) Abs(code uint16, value int32) []string {
	ax, ok := n.pad.axes[code]
	if !ok {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	var verbs []string
	switch ax.as {
	case AxisLX, AxisLY, AxisRX, AxisRY:
		verbs = n.stick(ax, value)
	case AxisLT, AxisRT:
		v := float64(value-ax.min) / float64(ax.max-ax.min)
		prev := n.origins[ax.as]["axis"]
		switch {
		case !prev && v >= triggerOn:
			verbs = n.setButton(ax.as, "axis", true)
		case prev && v <= triggerOff:
			verbs = n.setButton(ax.as, "axis", false)
		}
	case AxisHatX:
		verbs = append(n.setButton(BtnLeft, "hat", value < 0), n.setButton(BtnRight, "hat", value > 0)...)
	case AxisHatY:
		verbs = append(n.setButton(BtnUp, "hat", value < 0), n.setButton(BtnDown, "hat", value > 0)...)
	}
	return n.finish(verbs)
}

// stickVerbs: the left stick moves in the list, the right one pages and
// changes category. y grows downwards.
var stickVerbs = map[string][2]string{
	AxisLX: {VerbLeft, VerbRight}, AxisLY: {VerbUp, VerbDown},
	AxisRX: {VerbPrevTab, VerbNextTab}, AxisRY: {VerbPageUp, VerbPageDown},
}

func (n *Navigator) stick(ax axisRT, value int32) []string {
	mid, half := float64(ax.min+ax.max)/2, float64(ax.max-ax.min)/2
	v := (float64(value) - mid) / half
	cur := n.axisDir[ax.as]
	next := cur
	switch {
	case cur == 0 && v >= stickOn:
		next = 1
	case cur == 0 && v <= -stickOn:
		next = -1
	case cur == 1 && v < stickOff:
		next = 0
		if v <= -stickOn {
			next = -1
		}
	case cur == -1 && v > -stickOff:
		next = 0
		if v >= stickOn {
			next = 1
		}
	}
	if next == cur {
		return nil
	}
	n.axisDir[ax.as] = next
	var verbs []string
	if cur != 0 {
		n.release(fmt.Sprintf("stick:%s%+d", ax.as, cur))
	}
	if next != 0 {
		verb := stickVerbs[ax.as][(next+1)/2]
		verbs = n.press(fmt.Sprintf("stick:%s%+d", ax.as, next), verb)
	}
	return verbs
}

// setButton records that origin holds or lets go of a button; a button
// is down while any origin holds it (a trigger is both a key and an axis).
func (n *Navigator) setButton(button, origin string, down bool) []string {
	was := n.down(button)
	if n.origins[button] == nil {
		n.origins[button] = map[string]bool{}
	}
	n.origins[button][origin] = down
	is := n.down(button)
	switch {
	case !was && is:
		if verb := n.nav[button]; verb != "" {
			return n.press("btn:"+button, verb)
		}
	case was && !is:
		n.release("btn:" + button)
	}
	return nil
}

func (n *Navigator) down(button string) bool {
	for _, d := range n.origins[button] {
		if d {
			return true
		}
	}
	return false
}

func (n *Navigator) press(source, verb string) []string {
	h := &hold{verb: verb}
	if slices.Contains(repeats, verb) {
		h.next = n.now().Add(repeatFirst)
	}
	n.held[source] = h
	return []string{verb}
}

func (n *Navigator) release(source string) { delete(n.held, source) }

// finish schedules the next repeat, if something repeating is held.
func (n *Navigator) finish(verbs []string) []string {
	n.arm()
	return verbs
}

func (n *Navigator) arm() {
	if n.cancel != nil {
		n.cancel()
		n.cancel = nil
	}
	var first time.Time
	for _, h := range n.held {
		if !h.next.IsZero() && (first.IsZero() || h.next.Before(first)) {
			first = h.next
		}
	}
	if first.IsZero() || n.emit == nil {
		return
	}
	d := first.Sub(n.now())
	if d < 0 {
		d = 0
	}
	n.cancel = n.after(d, n.fire)
}

// fire is the repeat timer: every held direction that is due repeats.
func (n *Navigator) fire() {
	n.mu.Lock()
	now := n.now()
	var verbs []string
	for _, h := range n.held {
		if !h.next.IsZero() && !h.next.After(now) {
			verbs = append(verbs, h.verb)
			h.next = now.Add(repeatEvery)
		}
	}
	n.arm()
	n.mu.Unlock()
	if len(verbs) > 0 {
		n.emit(verbs)
	}
}

// Reset lets go of everything: the controller went away or was taken.
func (n *Navigator) Reset() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.origins, n.axisDir, n.held = map[string]map[string]bool{}, map[string]int{}, map[string]*hold{}
	n.arm()
}

// setRange replaces an axis's range with the device's own.
func (n *Navigator) setRange(code uint16, as string, min, max int32) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.pad = n.pad.withRange(code, as, min, max)
}
