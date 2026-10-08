package display

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Window is an application window.
type Window struct {
	ID         int64  `json:"id"`
	PID        int    `json:"pid"`
	AppID      string `json:"app_id,omitempty"` // Wayland app_id
	Class      string `json:"class,omitempty"`  // X11 class (Xwayland apps, Steam games)
	Title      string `json:"title"`
	Workspace  string `json:"workspace"`
	Output     string `json:"output,omitempty"`
	Focused    bool   `json:"focused"`
	Fullscreen bool   `json:"fullscreen"`
}

// Output is a screen.
type Output struct {
	Name    string  `json:"name"`
	Make    string  `json:"make,omitempty"`
	Model   string  `json:"model,omitempty"`
	Width   int     `json:"width"`
	Height  int     `json:"height"`
	Refresh float64 `json:"refresh"` // Hz
	Active  bool    `json:"active"`
	Focused bool    `json:"focused"`
	Power   bool    `json:"power"`           // false: the screen is off (power saving)
	Modes   []Mode  `json:"modes,omitempty"` // what the screen supports
}

// Mode is a resolution and refresh rate.
type Mode struct {
	Width   int     `json:"width"`
	Height  int     `json:"height"`
	Refresh float64 `json:"refresh"` // Hz
}

func (m Mode) String() string { return fmt.Sprintf("%dx%d@%.3fHz", m.Width, m.Height, m.Refresh) }

// Event is something that happened in the session:
//   - a window change (Change is new, close, focus, title, fullscreen_mode,
//     move...; Window is set),
//   - "binding": one of hostd's key bindings fired (Binding is its input
//     name, e.g. "Super+Tab"),
//   - "reload": the compositor reloaded its config, which drops runtime
//     bindings.
type Event struct {
	Change  string
	Window  Window
	Binding string
}

// Backend is the compositor. The display module's logic only talks to this
// interface; Sway is the first implementation.
type Backend interface {
	Windows(ctx context.Context) ([]Window, error)
	Outputs(ctx context.Context) ([]Output, error)
	// Show switches the screen to a workspace.
	Show(ctx context.Context, workspace string) error
	// Move puts a window on a workspace.
	Move(ctx context.Context, window int64, workspace string) error
	Focus(ctx context.Context, window int64) error
	Fullscreen(ctx context.Context, window int64, on bool) error
	// CloseWindow asks a window to close, as its close button would.
	CloseWindow(ctx context.Context, window int64) error
	// Bind replaces hostd's key bindings. A fired binding is reported by
	// Watch as an Event with Change "binding".
	Bind(ctx context.Context, keys []string) error
	// Place puts a window next to another, both windowed, side by side.
	Place(ctx context.Context, window, beside int64) error
	// SetOutput changes a screen: output is a name or "*", setting one of
	// "power on|off", "enable", "disable", "mode WxH@RHz".
	SetOutput(ctx context.Context, output, setting string) error
	// Watch calls fn with window events until ctx ends or the compositor
	// goes away (then it returns an error).
	Watch(ctx context.Context, fn func(Event)) error
	// Disconnect drops the connection to the compositor.
	Disconnect() error
}

// Sway talks to Sway over its IPC socket.
type Sway struct {
	path  string
	mu    sync.Mutex
	conn  net.Conn
	bound []string // the keys hostd bound last
}

// FindSway returns the IPC socket of the running Sway: $SWAYSOCK if it
// answers, else the newest sway-ipc.*.sock in runtimeDir that does.
func FindSway(ctx context.Context, runtimeDir string) (string, error) {
	var candidates []string
	if s := os.Getenv("SWAYSOCK"); s != "" {
		candidates = append(candidates, s)
	}
	socks, _ := filepath.Glob(filepath.Join(runtimeDir, "sway-ipc.*.sock"))
	sort.Slice(socks, func(i, j int) bool { return mtime(socks[i]) > mtime(socks[j]) })
	candidates = append(candidates, socks...)
	var d net.Dialer
	for _, s := range candidates {
		c, err := d.DialContext(ctx, "unix", s)
		if err == nil {
			c.Close()
			return s, nil
		}
	}
	return "", errors.New("Sway is not running")
}

func mtime(path string) int64 {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime().UnixNano()
	}
	return 0
}

// DialSway connects to Sway's socket.
func DialSway(ctx context.Context, path string) (*Sway, error) {
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	return &Sway{path: path, conn: c}, nil
}

// Disconnect closes the command connection.
func (s *Sway) Disconnect() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.Close()
}

func (s *Sway) request(ctx context.Context, typ uint32, payload []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if dl, ok := ctx.Deadline(); ok {
		_ = s.conn.SetDeadline(dl)
		defer func() { _ = s.conn.SetDeadline(time.Time{}) }()
	}
	if err := writeMessage(s.conn, typ, payload); err != nil {
		return nil, err
	}
	gotTyp, reply, err := readMessage(s.conn)
	if err != nil {
		return nil, err
	}
	if gotTyp != typ {
		return nil, fmt.Errorf("sway ipc: asked %d, got reply %d", typ, gotTyp)
	}
	return reply, nil
}

// run sends a command and reports Sway's error, if any.
func (s *Sway) run(ctx context.Context, cmd string) error {
	reply, err := s.request(ctx, ipcRunCommand, []byte(cmd))
	if err != nil {
		return err
	}
	var results []struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(reply, &results); err != nil {
		return err
	}
	for _, r := range results {
		if !r.Success {
			return fmt.Errorf("sway: %s: %s", cmd, r.Error)
		}
	}
	return nil
}

// node is the part of Sway's tree the module reads.
type node struct {
	ID               int64   `json:"id"`
	Type             string  `json:"type"`
	Name             *string `json:"name"`
	PID              int     `json:"pid"`
	AppID            *string `json:"app_id"`
	Focused          bool    `json:"focused"`
	FullscreenMode   int     `json:"fullscreen_mode"`
	WindowProperties *struct {
		Class string `json:"class"`
	} `json:"window_properties"`
	Nodes         []node `json:"nodes"`
	FloatingNodes []node `json:"floating_nodes"`
}

func (n node) isWindow() bool {
	return (n.Type == "con" || n.Type == "floating_con") && n.PID > 0 && (n.AppID != nil || n.WindowProperties != nil)
}

func (n node) window() Window {
	w := Window{ID: n.ID, PID: n.PID, Focused: n.Focused, Fullscreen: n.FullscreenMode != 0}
	if n.AppID != nil {
		w.AppID = *n.AppID
	}
	if n.WindowProperties != nil {
		w.Class = n.WindowProperties.Class
	}
	if n.Name != nil {
		w.Title = *n.Name
	}
	return w
}

// windows walks the tree, noting each window's output and workspace.
func windowsIn(root node) []Window {
	var out []Window
	var walk func(n node, output, workspace string)
	walk = func(n node, output, workspace string) {
		switch n.Type {
		case "output":
			if n.Name != nil {
				output = *n.Name
			}
		case "workspace":
			if n.Name != nil {
				workspace = *n.Name
			}
		}
		if n.isWindow() {
			w := n.window()
			w.Output, w.Workspace = output, workspace
			out = append(out, w)
		}
		for _, c := range n.Nodes {
			walk(c, output, workspace)
		}
		for _, c := range n.FloatingNodes {
			walk(c, output, workspace)
		}
	}
	walk(root, "", "")
	return out
}

func (s *Sway) Windows(ctx context.Context) ([]Window, error) {
	reply, err := s.request(ctx, ipcGetTree, nil)
	if err != nil {
		return nil, err
	}
	var root node
	if err := json.Unmarshal(reply, &root); err != nil {
		return nil, err
	}
	return windowsIn(root), nil
}

func (s *Sway) Outputs(ctx context.Context) ([]Output, error) {
	reply, err := s.request(ctx, ipcGetOutputs, nil)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Name        string `json:"name"`
		Make        string `json:"make"`
		Model       string `json:"model"`
		Active      bool   `json:"active"`
		Focused     bool   `json:"focused"`
		Power       *bool  `json:"power"` // Sway 1.9+
		DPMS        *bool  `json:"dpms"`  // older Sway
		CurrentMode struct {
			Width   int `json:"width"`
			Height  int `json:"height"`
			Refresh int `json:"refresh"` // mHz
		} `json:"current_mode"`
		Modes []struct {
			Width   int `json:"width"`
			Height  int `json:"height"`
			Refresh int `json:"refresh"`
		} `json:"modes"`
	}
	if err := json.Unmarshal(reply, &raw); err != nil {
		return nil, err
	}
	out := make([]Output, len(raw))
	for i, o := range raw {
		out[i] = Output{Name: o.Name, Make: o.Make, Model: o.Model, Active: o.Active, Focused: o.Focused,
			Width: o.CurrentMode.Width, Height: o.CurrentMode.Height, Refresh: float64(o.CurrentMode.Refresh) / 1000,
			Power: o.Active}
		switch {
		case o.Power != nil:
			out[i].Power = *o.Power
		case o.DPMS != nil:
			out[i].Power = *o.DPMS
		}
		for _, md := range o.Modes {
			out[i].Modes = append(out[i].Modes, Mode{Width: md.Width, Height: md.Height, Refresh: float64(md.Refresh) / 1000})
		}
	}
	return out, nil
}

// Workspace names are quoted in commands; instance IDs never contain
// quotes, but be safe anyway.
func quote(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }

// bindPrefix starts the command of every binding hostd installs: a nop
// that Sway reports back as a binding event.
const bindPrefix = "nop hostd key "

// swayKeys turns an input name ("Super+Shift+Tab", "Super", "F1") into a
// bindsym key combination and its flags. A lone modifier binds on release,
// so it does not fire when used in a combination.
func swayKeys(name string) (flags, combo string) {
	mods := map[string]string{"super": "Mod4", "ctrl": "Control", "control": "Control", "alt": "Mod1", "shift": "Shift"}
	parts := strings.Split(name, "+")
	if len(parts) == 1 {
		// A modifier is held while it is released, so its own modifier
		// is part of the combination.
		switch strings.ToLower(name) {
		case "super":
			return "--release", "Mod4+Super_L"
		case "alt":
			return "--release", "Mod1+Alt_L"
		}
		if len(name) == 1 {
			name = strings.ToLower(name)
		}
		return "--no-repeat", name
	}
	for i, p := range parts[:len(parts)-1] {
		if m, ok := mods[strings.ToLower(p)]; ok {
			parts[i] = m
		}
	}
	// Keysyms are case-sensitive: "Q" is shift+q. A letter means the key.
	if last := parts[len(parts)-1]; len(last) == 1 {
		parts[len(parts)-1] = strings.ToLower(last)
	}
	return "--no-repeat", strings.Join(parts, "+")
}

// Bind removes the bindings hostd installed before and installs keys.
// Runtime bindings do not survive a config reload; the module installs
// them again on the "reload" event.
func (s *Sway) Bind(ctx context.Context, keys []string) error {
	s.mu.Lock()
	old := s.bound
	s.bound = append([]string(nil), keys...)
	s.mu.Unlock()
	var cmds []string
	for _, k := range old {
		flags, combo := swayKeys(k)
		cmds = append(cmds, fmt.Sprintf("unbindsym %s %s", flags, combo))
	}
	for _, k := range keys {
		flags, combo := swayKeys(k)
		cmds = append(cmds, fmt.Sprintf("bindsym %s %s %s%s", flags, combo, bindPrefix, k))
	}
	if len(cmds) == 0 {
		return nil
	}
	return s.runEach(ctx, cmds)
}

// runEach runs commands one by one; an unbindsym of a binding a reload
// already dropped fails harmlessly, so only bindsym errors count.
func (s *Sway) runEach(ctx context.Context, cmds []string) error {
	var first error
	for _, c := range cmds {
		if err := s.run(ctx, c); err != nil && first == nil && strings.HasPrefix(c, "bindsym") {
			first = fmt.Errorf("%s: %w", c, err)
		}
	}
	return first
}

// Place uses a mark on the target: "move container to mark" makes the
// window a sibling of it, and splith lays the two out side by side.
func (s *Sway) Place(ctx context.Context, window, beside int64) error {
	return s.run(ctx, fmt.Sprintf("[con_id=%d] fullscreen disable; [con_id=%d] fullscreen disable; "+
		"[con_id=%d] mark --add hostd_place; [con_id=%d] splith; [con_id=%d] move container to mark hostd_place; "+
		"[con_id=%d] unmark hostd_place; [con_id=%d] focus",
		beside, window, beside, beside, window, beside, window))
}

func (s *Sway) SetOutput(ctx context.Context, output, setting string) error {
	name := "*"
	if output != "*" {
		name = quote(output)
	}
	return s.run(ctx, "output "+name+" "+setting)
}

func (s *Sway) Show(ctx context.Context, workspace string) error {
	return s.run(ctx, "workspace "+quote(workspace))
}

func (s *Sway) Move(ctx context.Context, window int64, workspace string) error {
	return s.run(ctx, fmt.Sprintf("[con_id=%d] move container to workspace %s", window, quote(workspace)))
}

func (s *Sway) Focus(ctx context.Context, window int64) error {
	return s.run(ctx, fmt.Sprintf("[con_id=%d] focus", window))
}

func (s *Sway) Fullscreen(ctx context.Context, window int64, on bool) error {
	state := "disable"
	if on {
		state = "enable"
	}
	return s.run(ctx, fmt.Sprintf("[con_id=%d] fullscreen %s", window, state))
}

func (s *Sway) CloseWindow(ctx context.Context, window int64) error {
	return s.run(ctx, fmt.Sprintf("[con_id=%d] kill", window))
}

// Watch subscribes on a second connection, since the first one carries
// requests and replies.
func (s *Sway) Watch(ctx context.Context, fn func(Event)) error {
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", s.path)
	if err != nil {
		return err
	}
	defer c.Close()
	// Unblock the read below when ctx ends; released when Watch returns,
	// so a Sway restart leaks nothing.
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	if err := writeMessage(c, ipcSubscribe, []byte(`["window","binding","workspace","shutdown"]`)); err != nil {
		return err
	}
	typ, reply, err := readMessage(c)
	if err != nil {
		return err
	}
	var ok struct {
		Success bool `json:"success"`
	}
	if typ != ipcSubscribe || json.Unmarshal(reply, &ok) != nil || !ok.Success {
		return fmt.Errorf("sway refused the subscription: %s", reply)
	}
	for {
		typ, payload, err := readMessage(c)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("sway went away: %w", err)
		}
		switch typ {
		case eventShutdown:
			return errors.New("sway is shutting down")
		case eventWindow:
			var ev struct {
				Change    string `json:"change"`
				Container node   `json:"container"`
			}
			if json.Unmarshal(payload, &ev) == nil {
				fn(Event{Change: ev.Change, Window: ev.Container.window()})
			}
		case eventBinding:
			var ev struct {
				Binding struct {
					Command string `json:"command"`
				} `json:"binding"`
			}
			if json.Unmarshal(payload, &ev) == nil {
				if name, ok := strings.CutPrefix(ev.Binding.Command, bindPrefix); ok {
					fn(Event{Change: "binding", Binding: name})
				}
			}
		case eventWorkspace:
			var ev struct {
				Change string `json:"change"`
			}
			if json.Unmarshal(payload, &ev) == nil && ev.Change == "reload" {
				fn(Event{Change: "reload"})
			}
		}
	}
}
