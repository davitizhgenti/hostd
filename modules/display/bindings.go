package display

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/davitizhgenti/hostd/sdk"
)

// Input model: every key or button that changes the screen is a named
// input, and one table maps inputs to display actions. The actions go
// through the core like any other, with source local, so they are
// audited and outrank phones and scripts.
//
// Keys reach hostd through the compositor (Backend.Bind installs them,
// Watch reports them back), so they work with any keyboard, VNC included.
// Controller buttons come from the evdev reader.

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
// one. It checks every name and action, so a typo in hostd.toml stops
// hostd with a clear message instead of a key that silently does nothing.
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
		return nil, nil, fmt.Errorf("input.keys: %w", err)
	}
	knownButtons := ButtonNames
	b, err := merge(DefaultButtons, buttons, func(name string) error {
		if !slices.Contains(knownButtons, name) {
			return fmt.Errorf("button %q: use one of %s", name, strings.Join(knownButtons, ", "))
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("input.buttons: %w", err)
	}
	return k, b, nil
}

// onInput is called for every input from every device.
func (m *Module) onInput(ev InputEvent) {
	m.presence.touch()
	if len(ev.Released) > 0 || len(ev.Buttons) > 0 {
		m.mu.Lock()
		for _, b := range ev.Released {
			delete(m.down, b)
		}
		for _, b := range ev.Buttons {
			m.down[b] = true
		}
		m.mu.Unlock()
		m.syncGrab()
	}
	for _, b := range ev.Released {
		m.release(b)
	}
	for _, b := range ev.Buttons {
		m.hold(b)
	}
	if len(ev.Nav) > 0 {
		m.mu.Lock()
		front, core := m.menuFront, m.core
		m.mu.Unlock()
		if front && core != nil {
			for _, n := range ev.Nav {
				core.Emit(sdk.Event{Type: EventNav, Data: sdk.MustJSON(map[string]string{"what": n}),
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
	m.mu.Unlock()
	m.syncGrab()
}

// syncGrab takes or gives back the controllers. They are taken only once
// no button is held: an app saw a held button (Guide, held to open the
// menu) go down and must see it come up, or it stays down for the app.
// Once taken, they stay taken while the menu is in front.
func (m *Module) syncGrab() {
	g, ok := m.opts.Input.(Grabber)
	if !ok {
		return
	}
	m.mu.Lock()
	want := m.menuFront && (m.grabbed || len(m.down) == 0)
	change := m.grabbed != want
	m.grabbed = want
	m.mu.Unlock()
	if change {
		if err := g.Grab(want); err != nil {
			m.log.Warn("taking the controllers for the menu", "on", want, "err", err)
		}
	}
}

// hold runs a controller button's action once it has been held for
// HoldFor; letting go sooner leaves the press to the app.
func (m *Module) hold(name string) {
	if _, ok := m.buttons[name]; !ok {
		return
	}
	if m.opts.HoldFor < 0 {
		m.press("controller", name)
		return
	}
	m.mu.Lock()
	ctx := m.life
	if _, held := m.holds[name]; held || ctx == nil {
		m.mu.Unlock()
		return
	}
	released := make(chan struct{})
	m.holds[name] = released
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
		if m.holds[name] == released {
			delete(m.holds, name)
		}
		m.mu.Unlock()
		m.press("controller", name)
	}()
}

// release notes that a controller button was let go.
func (m *Module) release(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ch, ok := m.holds[name]; ok {
		close(ch)
		delete(m.holds, name)
	}
}

// press runs the action bound to an input. A press while the previous
// press of the same input is still being handled is dropped (a held or
// bouncing button).
func (m *Module) press(device, name string) {
	table := m.keys
	if device == "controller" {
		table = m.buttons
	}
	action, ok := table[name]
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
