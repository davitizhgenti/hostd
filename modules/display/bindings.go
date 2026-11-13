package display

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/davitizhgenti/hostd/internal/evdev"
	"github.com/davitizhgenti/hostd/sdk"
)

// Input model: every key or button that changes the screen is a named
// input, and one table maps inputs to display actions. The actions go
// through the core like any other, with source local, so they are
// audited and outrank phones and scripts.
//
// Keys reach hostd through the compositor (Backend.Bind installs them,
// Watch reports them back), so they work with any keyboard, VNC included.
// evdev.Controller buttons come from the evdev reader.

// DefaultKeys is the shortcut model: Super is the system key. Super alone
// opens the menu; Super+key acts on the app in front.
var DefaultKeys = map[string]string{
	"Super":           "display.menu",
	"Super+Tab":       "window.next",
	"Super+Shift+Tab": "window.prev",
	"Super+Q":         "window.close",
}

// DefaultButtons: the Guide button is the controller's Super. Apps see
// controller buttons too (they read the controller themselves; Steam
// opens its own menu on Guide), so a button's action runs when it is
// held (Options.HoldFor): a tap is the app's, a hold is hostd's.
var DefaultButtons = map[string]string{
	"guide": "display.menu",
}

// Bindable lists the actions a key or button can run: the display
// module's actions that need no arguments.
func Bindable() []string {
	var out []string
	for _, a := range New(Options{}).Manifest().Actions {
		if !requiresArgs(a.Schema) {
			out = append(out, a.Type)
		}
	}
	slices.Sort(out)
	return out
}

func requiresArgs(schema []byte) bool {
	return regexp.MustCompile(`"required"\s*:\s*\[\s*"`).Match(schema)
}

var reKeyName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

var modifierNames = map[string]bool{"super": true, "ctrl": true, "control": true, "alt": true, "shift": true}

// validKey checks an input name such as "Super+Shift+Tab" or "F1".
func validKey(name string) error {
	parts := strings.Split(name, "+")
	for i, p := range parts {
		switch {
		case p == "":
			return fmt.Errorf("key %q: empty part", name)
		case i < len(parts)-1 && !modifierNames[strings.ToLower(p)]:
			return fmt.Errorf("key %q: %q is not a modifier (use Super, Ctrl, Alt, Shift)", name, p)
		case !reKeyName.MatchString(p):
			return fmt.Errorf("key %q: %q is not a key name (as xkb names them: Tab, Q, F1, Escape...)", name, p)
		}
	}
	return nil
}

// Bindings merges overrides onto the defaults: a binding to "" removes
// one. It checks every name and action, so a typo in hostd.toml ([display]
// keys and buttons) stops hostd with a clear message instead of a key that
// silently does nothing.
func Bindings(keys, buttons map[string]string) (map[string]string, map[string]string, error) {
	bindable := Bindable()
	merge := func(defaults, over map[string]string, check func(string) error) (map[string]string, error) {
		out := maps.Clone(defaults)
		for name, action := range over {
			if err := check(name); err != nil {
				return nil, err
			}
			if action == "" {
				delete(out, name)
				continue
			}
			if !slices.Contains(bindable, action) {
				return nil, fmt.Errorf("%s: %q cannot be bound; use one of %s", name, action, strings.Join(bindable, ", "))
			}
			out[name] = action
		}
		return out, nil
	}
	k, err := merge(DefaultKeys, keys, validKey)
	if err != nil {
		return nil, nil, fmt.Errorf("keys: %w", err)
	}
	knownButtons := evdev.ButtonNames
	b, err := merge(DefaultButtons, buttons, func(name string) error {
		if !slices.Contains(knownButtons, name) {
			return fmt.Errorf("button %q: use one of %s", name, strings.Join(knownButtons, ", "))
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("buttons: %w", err)
	}
	return k, b, nil
}

// onInput is called for every input from every device.
func (m *Module) onInput(ev evdev.InputEvent) {
	m.presence.Touch()
	if ev.Device != "" {
		m.mu.Lock()
		m.lastBy[ev.Device] = m.opts.Clock.Now()
		m.mu.Unlock()
	}
	if len(ev.Released) > 0 || len(ev.Buttons) > 0 {
		m.mu.Lock()
		for _, b := range ev.Released {
			delete(m.down, ev.Device+"/"+b)
		}
		for _, b := range ev.Buttons {
			m.down[ev.Device+"/"+b] = true
		}
		m.mu.Unlock()
		m.syncGrab()
	}
	for _, b := range ev.Released {
		m.release(ev.Device, b)
	}
	for _, b := range ev.Buttons {
		m.hold(ev.Device, ev.Player, b)
	}
	if ev.Pointer != nil {
		m.mu.Lock()
		front, core, owner := m.menuFront, m.core, m.menuOwner
		m.mu.Unlock()
		if front && core != nil && (owner == "" || owner == ev.Device) {
			core.Emit(sdk.Event{Type: EventPointer, Data: sdk.MustJSON(map[string]any{"x": ev.Pointer.X, "y": ev.Pointer.Y,
				"visible": ev.Pointer.Visible, "down": ev.Pointer.Down, "device": ev.Device, "player": ev.Player, "family": ev.Family}),
				Source: &sdk.Source{Kind: sdk.SourceLocal, Name: "controller"}})
		}
	}
	if len(ev.Nav) > 0 {
		m.mu.Lock()
		front, core, owner := m.menuFront, m.core, m.menuOwner
		m.mu.Unlock()
		// The menu belongs to the controller that opened it: another
		// player's buttons do nothing to it.
		if front && core != nil && (owner == "" || owner == ev.Device) {
			for _, n := range ev.Nav {
				core.Emit(sdk.Event{Type: EventNav, Data: sdk.MustJSON(map[string]any{"what": n, "device": ev.Device, "player": ev.Player, "family": ev.Family}),
					Source: &sdk.Source{Kind: sdk.SourceLocal, Name: "controller"}})
			}
		}
	}
}

// isMenu reports whether an instance is hostd's menu.
func (m *Module) isMenu(instance string) bool {
	return instance != "" && (instance == m.opts.Menu || strings.HasPrefix(instance, m.opts.Menu+"#"))
}

// takeControllers takes the controllers while hostd's menu is in front,
// and gives them back after: apps that read them (Steam) must not act on
// the menu's navigation.
func (m *Module) takeControllers(menuFront bool) {
	m.mu.Lock()
	m.menuFront = menuFront
	if menuFront {
		m.menuOwner = m.lastPad // the controller that asked for it; "" if none did
	}
	m.mu.Unlock()
	m.syncGrab()
	if !menuFront {
		m.mu.Lock()
		m.menuOwner, m.lastPad = "", ""
		m.mu.Unlock()
	}
}

// syncGrab takes or gives back the controllers. They are taken only once
// no button is held: an app saw a held button (Guide, held to open the
// menu) go down and must see it come up, or it stays down for the app.
// Once taken, they stay taken while the menu is in front.
func (m *Module) syncGrab() {
	g, ok := m.opts.Input.(evdev.Grabber)
	if !ok {
		return
	}
	m.mu.Lock()
	owner := m.menuOwner
	if m.grabbed && m.grabbedDev != owner && m.grabbedDev != "" { // the owner changed: give back the old one
		old := m.grabbedDev
		m.mu.Unlock()
		_ = g.Grab(old, false)
		m.mu.Lock()
		m.grabbed = false
	}
	held := false
	for k := range m.down {
		if owner == "" || strings.HasPrefix(k, owner+"/") {
			held = true
		}
	}
	want := m.menuFront && (m.grabbed || !held)
	change := m.grabbed != want
	m.grabbed, m.grabbedDev = want, owner
	m.mu.Unlock()
	if change {
		if err := g.Grab(owner, want); err != nil {
			m.log.Warn("taking the controllers for the menu", "on", want, "err", err)
		}
	}
}

// hold runs a controller button's action once it has been held for
// HoldFor; letting go sooner leaves the press to the app.
func (m *Module) hold(device string, player int, name string) {
	if _, ok := m.buttonAction(player, name); !ok {
		return
	}
	if m.opts.HoldFor < 0 {
		m.mu.Lock()
		m.lastPad = device
		m.mu.Unlock()
		m.press("controller", player, name)
		return
	}
	key := device + "/" + name
	m.mu.Lock()
	ctx := m.life
	if _, held := m.holds[key]; held || ctx == nil {
		m.mu.Unlock()
		return
	}
	released := make(chan struct{})
	m.holds[key] = released
	m.mu.Unlock()
	m.done.Add(1)
	go func() {
		defer m.done.Done()
		t := m.opts.Clock.NewTimer(m.opts.HoldFor)
		defer t.Stop()
		select {
		case <-t.C():
		case <-released:
			return
		case <-ctx.Done():
			return
		}
		m.mu.Lock()
		if m.holds[key] == released {
			delete(m.holds, key)
		}
		m.lastPad = device
		m.mu.Unlock()
		m.press("controller", player, name)
	}()
}

// release notes that a controller button was let go.
func (m *Module) release(device, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := device + "/" + name
	if ch, ok := m.holds[key]; ok {
		close(ch)
		delete(m.holds, key)
	}
}

// press runs the action bound to an input. A press while the previous
// press of the same input is still being handled is dropped (a held or
// bouncing button).
func (m *Module) press(device string, player int, name string) {
	var action string
	var ok bool
	if device == "controller" {
		action, ok = m.buttonAction(player, name)
	} else {
		action, ok = m.keys[name]
	}
	if !ok {
		return
	}
	m.mu.Lock()
	core := m.core
	busy := m.pressing[device+":"+name]
	if !busy {
		m.pressing[device+":"+name] = true
	}
	m.mu.Unlock()
	if core == nil || busy {
		return
	}
	m.done.Add(1)
	go func() {
		defer m.done.Done()
		defer func() { m.mu.Lock(); delete(m.pressing, device+":"+name); m.mu.Unlock() }()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := core.Do(ctx, sdk.Action{Type: action, Args: sdk.MustJSON(map[string]any{}),
			Source: sdk.Source{Kind: sdk.SourceLocal, Name: device}})
		if err != nil && sdk.CodeOf(err) != sdk.CodeNotFound {
			m.log.Warn("input", "device", device, "input", name, "action", action, "err", err)
		}
	}()
}

// bind installs the key bindings in the compositor.
func (m *Module) bind(ctx context.Context, b Backend) {
	keys := slices.Sorted(maps.Keys(m.keys))
	if err := b.Bind(ctx, keys); err != nil {
		m.log.Warn("installing key bindings", "err", err)
	}
}

// PlayerBindings gives each player slot the button bindings it differs in:
// base (the defaults and [display.buttons]) with that player's changes on
// top. It checks the slot's number and every name and action.
func PlayerBindings(base map[string]string, players map[string]PlayerConfig) (map[int]map[string]string, error) {
	out := map[int]map[string]string{}
	for slot, pc := range players {
		n, err := strconv.Atoi(slot)
		if err != nil || n < 1 || n > 16 {
			return nil, fmt.Errorf("players.%s: a player slot is a number from 1 to 16", slot)
		}
		_, merged, err := Bindings(nil, mergeButtons(base, pc.Buttons))
		if err != nil {
			return nil, fmt.Errorf("players.%d.buttons: %w", n, err)
		}
		out[n] = merged
	}
	return out, nil
}

// mergeButtons returns the overrides to hand Bindings so that base plus over
// is what comes out (Bindings starts from the defaults, which base already
// has changed): every binding of base, then over, a removal being "".
func mergeButtons(base, over map[string]string) map[string]string {
	out := map[string]string{}
	for name := range DefaultButtons {
		out[name] = "" // start from nothing: base says what is left
	}
	for name, action := range base {
		out[name] = action
	}
	for name, action := range over {
		out[name] = action
	}
	return out
}

// buttonAction is what a controller button does for a player: the player's
// own binding if the slot has a table, else the common one.
func (m *Module) buttonAction(player int, name string) (string, bool) {
	if t, ok := m.playerButtons[player]; ok {
		a, ok := t[name]
		return a, ok
	}
	a, ok := m.buttons[name]
	return a, ok
}
