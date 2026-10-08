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
// opens the switcher; Super+key acts on the app in front.
var DefaultKeys = map[string]string{
	"Super":           "display.switcher",
	"Super+Tab":       "window.next",
	"Super+Shift+Tab": "window.prev",
	"Super+Q":         "window.close",
}

// DefaultButtons: the Guide button is the controller's Super.
var DefaultButtons = map[string]string{
	"guide": "display.switcher",
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
	knownButtons := slices.Sorted(maps.Values(Buttons))
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
		_, err := core.Do(ctx, sdk.Action{Type: action, Args: mustJSON(map[string]any{}),
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
