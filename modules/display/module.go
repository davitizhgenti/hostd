// Package display is the display module: it puts each app's windows on its
// own workspace, fullscreen by default, follows focus, and focuses, closes
// or fullscreens windows on request. Sway is the compositor (sway.go).
package display

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/davitizhgenti/hostd/internal/clock"
	"github.com/davitizhgenti/hostd/sdk"
)

// Events the module emits.
const (
	EventOpened  = "window.opened"
	EventFocused = "window.focused"
	EventClosed  = "window.closed"
)

// WorkspacePrefix names an instance's workspace: hostd:<instance>.
const WorkspacePrefix = "hostd:"

// Options configure the display module.
type Options struct {
	// Connect finds and connects to the compositor. It is called again
	// whenever the connection drops (Sway restarted, or not started yet).
	Connect  func(ctx context.Context) (Backend, error)
	ProcRoot string // default /proc

	Clock        clock.Clock
	Logger       *slog.Logger
	RetryEvery   time.Duration // between connection attempts (default 2s)
	CloseTimeout time.Duration // how long window.close waits before stopping the app (default 5s)
}

// SwayConnector connects to the Sway running in runtimeDir.
func SwayConnector(runtimeDir string) func(context.Context) (Backend, error) {
	return func(ctx context.Context) (Backend, error) {
		path, err := FindSway(ctx, runtimeDir)
		if err != nil {
			return nil, err
		}
		return DialSway(ctx, path)
	}
}

// Module is the display module.
type Module struct {
	opts Options
	log  *slog.Logger

	mu        sync.Mutex
	core      sdk.Core
	backend   Backend                  // nil while no compositor is attached
	windows   map[int64]*trackedWindow // by window ID
	stack     []string                 // instances by focus, most recent last
	prefs     map[string]bool          // instance -> wants fullscreen
	closeWait map[int64]chan struct{}

	stop context.CancelFunc
	done sync.WaitGroup
}

type trackedWindow struct {
	Window
	Instance string `json:"instance,omitempty"`
}

// New returns the display module.
func New(opts Options) *Module {
	if opts.ProcRoot == "" {
		opts.ProcRoot = "/proc"
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.RetryEvery == 0 {
		opts.RetryEvery = 2 * time.Second
	}
	if opts.CloseTimeout == 0 {
		opts.CloseTimeout = 5 * time.Second
	}
	return &Module{opts: opts, log: opts.Logger, windows: map[int64]*trackedWindow{},
		prefs: map[string]bool{}, closeWait: map[int64]chan struct{}{}}
}

var (
	instanceArg = json.RawMessage(`{"type":"object","properties":{
		"instance":{"type":"string","description":"instance ID, e.g. firefox or firefox#2"}},"required":["instance"]}`)
	fullscreenArg = json.RawMessage(`{"type":"object","properties":{
		"instance":{"type":"string","description":"instance ID"},
		"enabled":{"type":"boolean","description":"true (default) or false"}},"required":["instance"]}`)
)

func (m *Module) Manifest() sdk.Manifest {
	return sdk.Manifest{
		Name: "display", Version: "0.1.0",
		Owns: []string{"display.*", "window.*"},
		Scopes: []sdk.ScopeSpec{
			{Name: "display", Description: "Display power and mode, window fullscreen and placement"},
			{Name: "display.front", Description: "Force a window to the front while someone uses the screen"},
		},
		Actions: []sdk.ActionSpec{
			{Type: "window.focus", Description: "Bring an instance's window to the front",
				Schema: instanceArg, Keys: []sdk.KeyTemplate{"display.focus"}, Scope: "apps",
				Timeout: sdk.Duration(10 * time.Second),
				Route:   &sdk.Route{Method: "POST", Path: "/v1/windows/{instance}/focus"}},
			{Type: "window.close", Description: "Close an instance's windows politely; stop the app if they stay open",
				Schema: instanceArg, Keys: []sdk.KeyTemplate{"instance:{instance}"}, Scope: "apps",
				Timeout: sdk.Duration(time.Minute),
				Route:   &sdk.Route{Method: "POST", Path: "/v1/windows/{instance}/close"}},
			{Type: "window.fullscreen", Description: "Turn fullscreen on or off for an instance's windows",
				Schema: fullscreenArg, Keys: []sdk.KeyTemplate{"instance:{instance}"}, Scope: "display",
				Timeout: sdk.Duration(10 * time.Second),
				Route:   &sdk.Route{Method: "POST", Path: "/v1/windows/{instance}/fullscreen"}},
		},
		Events: []sdk.EventSpec{
			{Type: EventOpened, Description: "A window opened (instance is empty for windows hostd did not start)"},
			{Type: EventFocused, Description: "A window got the focus"},
			{Type: EventClosed, Description: "A window closed"},
		},
		Reads: []sdk.ReadSpec{
			{Name: "windows", Description: "All windows, including ones hostd did not start", Path: "/v1/windows"},
			{Name: "display", Description: "Whether a session is attached, outputs, the focused instance", Path: "/v1/display"},
		},
	}
}

// Start begins following the compositor and the apps' instances. A missing
// compositor is not an error: the module attaches when Sway appears.
func (m *Module) Start(_ context.Context, core sdk.Core) error {
	m.mu.Lock()
	m.core = core
	m.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	m.stop = cancel
	m.done.Add(2)
	go func() { defer m.done.Done(); m.attachLoop(ctx) }()
	go func() { defer m.done.Done(); m.followInstances(ctx, core.Subscribe(ctx, "instance.*")) }()
	return nil
}

func (m *Module) Stop(context.Context) error {
	if m.stop != nil {
		m.stop()
	}
	m.done.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.backend != nil {
		_ = m.backend.Disconnect()
		m.backend = nil
	}
	return nil
}

// attachLoop connects to the compositor, follows its window events until
// it goes away, and tries again.
func (m *Module) attachLoop(ctx context.Context) {
	warned := false
	for ctx.Err() == nil {
		b, err := m.opts.Connect(ctx)
		if err != nil {
			if !warned {
				m.log.Info("no display session yet; window actions wait for it", "reason", err)
				warned = true
			}
		} else {
			warned = false
			m.attach(ctx, b)
			err := b.Watch(ctx, m.onEvent)
			m.detach(b)
			if ctx.Err() != nil {
				return
			}
			m.log.Warn("display session ended", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-m.opts.Clock.After(m.opts.RetryEvery):
		}
	}
}

func (m *Module) attach(ctx context.Context, b Backend) {
	wins, err := b.Windows(ctx)
	if err != nil {
		m.log.Warn("reading windows", "err", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.backend = b
	m.windows = map[int64]*trackedWindow{}
	for _, w := range wins {
		tw := &trackedWindow{Window: w, Instance: instanceOf(m.opts.ProcRoot, w.PID)}
		m.windows[w.ID] = tw
		if w.Focused && tw.Instance != "" {
			m.pushFocus(tw.Instance)
		}
	}
	m.log.Info("display session attached", "windows", len(wins))
}

func (m *Module) detach(b Backend) {
	_ = b.Disconnect()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.backend == b {
		m.backend = nil
	}
	m.windows = map[int64]*trackedWindow{}
}

// followInstances learns each instance's fullscreen preference from the
// apps module's events.
func (m *Module) followInstances(ctx context.Context, events <-chan sdk.Event) {
	for ev := range events {
		var in struct {
			ID         string `json:"id"`
			Fullscreen *bool  `json:"fullscreen"`
		}
		if json.Unmarshal(ev.Data, &in) != nil || in.ID == "" {
			continue
		}
		switch ev.Type {
		case "instance.starting", "instance.started":
			if in.Fullscreen == nil {
				continue
			}
			m.mu.Lock()
			m.prefs[in.ID] = *in.Fullscreen
			// The window may already be up (placed fullscreen before this
			// event arrived): honour a "windowed" preference now.
			var fix []int64
			if !*in.Fullscreen {
				for id, w := range m.windows {
					if w.Instance == in.ID && w.Fullscreen {
						fix = append(fix, id)
					}
				}
			}
			b := m.backend
			m.mu.Unlock()
			for _, id := range fix {
				if b != nil {
					_ = b.Fullscreen(ctx, id, false)
				}
			}
		case "instance.exited", "instance.failed":
			m.mu.Lock()
			delete(m.prefs, in.ID)
			m.mu.Unlock()
		}
	}
}

// pushFocus moves an instance to the top of the focus history. Caller
// holds m.mu.
func (m *Module) pushFocus(instance string) {
	for i, s := range m.stack {
		if s == instance {
			m.stack = append(m.stack[:i], m.stack[i+1:]...)
			break
		}
	}
	m.stack = append(m.stack, instance)
}

// onEvent handles one window event from the compositor.
func (m *Module) onEvent(ev WindowEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w := ev.Window
	switch ev.Change {
	case "new":
		inst := instanceOf(m.opts.ProcRoot, w.PID)
		m.mu.Lock()
		tw := &trackedWindow{Window: w, Instance: inst}
		m.windows[w.ID] = tw
		full, known := m.prefs[inst]
		b := m.backend
		m.mu.Unlock()
		if inst != "" && b != nil {
			// Its own workspace, in front, fullscreen unless the app
			// says otherwise. (Background launches while someone uses the
			// screen come with presence detection, M2.)
			ws := WorkspacePrefix + inst
			if err := b.Move(ctx, w.ID, ws); err != nil {
				m.log.Warn("placing window", "instance", inst, "err", err)
			}
			_ = b.Show(ctx, ws)
			_ = b.Focus(ctx, w.ID)
			if full || !known {
				_ = b.Fullscreen(ctx, w.ID, true)
			}
			m.mu.Lock()
			if t, ok := m.windows[w.ID]; ok {
				t.Workspace = ws
			}
			m.mu.Unlock()
			tw = &trackedWindow{Window: w, Instance: inst}
			tw.Workspace = ws
		}
		m.emit(EventOpened, tw)
	case "close":
		m.mu.Lock()
		tw, ok := m.windows[w.ID]
		delete(m.windows, w.ID)
		if ch, waiting := m.closeWait[w.ID]; waiting {
			close(ch)
			delete(m.closeWait, w.ID)
		}
		var back *trackedWindow
		if ok && tw.Instance != "" && len(m.stack) > 0 && m.stack[len(m.stack)-1] == tw.Instance && !m.hasWindow(tw.Instance) {
			// The focused app's last window closed: go back to the app
			// that had the focus before it.
			m.stack = m.stack[:len(m.stack)-1]
			back = m.previous()
		}
		b := m.backend
		m.mu.Unlock()
		if !ok {
			tw = &trackedWindow{Window: w}
		}
		m.emit(EventClosed, tw)
		if back != nil && b != nil {
			_ = b.Show(ctx, back.Workspace)
			_ = b.Focus(ctx, back.ID)
		}
	case "focus":
		m.mu.Lock()
		for _, t := range m.windows {
			t.Focused = t.ID == w.ID
		}
		tw, ok := m.windows[w.ID]
		if ok && tw.Instance != "" {
			m.pushFocus(tw.Instance)
		}
		var snap trackedWindow
		if ok {
			snap = *tw
		}
		m.mu.Unlock()
		if ok {
			m.emit(EventFocused, &snap)
		}
	case "fullscreen_mode", "title":
		m.mu.Lock()
		if t, ok := m.windows[w.ID]; ok {
			t.Fullscreen, t.Title = w.Fullscreen, w.Title
		}
		m.mu.Unlock()
	}
}

// hasWindow reports whether an instance still has a window. Caller holds
// m.mu.
func (m *Module) hasWindow(instance string) bool {
	for _, t := range m.windows {
		if t.Instance == instance {
			return true
		}
	}
	return false
}

// previous returns a window of the most recently focused instance that
// still has one, dropping instances that have none. Caller holds m.mu.
func (m *Module) previous() *trackedWindow {
	for len(m.stack) > 0 {
		inst := m.stack[len(m.stack)-1]
		for _, t := range m.windows {
			if t.Instance == inst {
				c := *t
				return &c
			}
		}
		m.stack = m.stack[:len(m.stack)-1]
	}
	return nil
}

func (m *Module) emit(typ string, tw *trackedWindow) {
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	if core == nil {
		return
	}
	b, _ := json.Marshal(tw)
	core.Emit(sdk.Event{Type: typ, Data: b, Source: &sdk.Source{Kind: sdk.SourceExternal, Name: "sway"}})
}

// --- actions ---------------------------------------------------------------

type instanceArgs struct {
	Instance string `json:"instance"`
	Enabled  *bool  `json:"enabled"`
}

// windowsOf returns the instance's windows, freshly read from the
// compositor (they may have moved), the focused or most recent first.
func (m *Module) windowsOf(ctx context.Context, instance string) (Backend, []Window, error) {
	m.mu.Lock()
	b := m.backend
	m.mu.Unlock()
	if b == nil {
		return nil, nil, sdk.Errorf(sdk.CodeModuleUnavailable, "no display session: Sway is not running")
	}
	all, err := b.Windows(ctx)
	if err != nil {
		return nil, nil, err
	}
	var out []Window
	for _, w := range all {
		if instanceOf(m.opts.ProcRoot, w.PID) == instance {
			out = append(out, w)
		}
	}
	if len(out) == 0 {
		return nil, nil, sdk.Errorf(sdk.CodeNotFound, "instance %q has no window", instance)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Focused && !out[j].Focused })
	return b, out, nil
}

func (m *Module) Validate(ctx context.Context, a sdk.Action) error {
	var args instanceArgs
	if err := a.DecodeArgs(&args); err != nil {
		return err
	}
	_, _, err := m.windowsOf(ctx, args.Instance)
	return err
}

func (m *Module) Handle(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	var args instanceArgs
	if err := a.DecodeArgs(&args); err != nil {
		return sdk.Result{}, err
	}
	b, wins, err := m.windowsOf(ctx, args.Instance)
	if err != nil {
		return sdk.Result{}, err
	}
	switch a.Type {
	case "window.focus":
		w := wins[0]
		if err := b.Show(ctx, w.Workspace); err != nil {
			return sdk.Result{}, err
		}
		if err := b.Focus(ctx, w.ID); err != nil {
			return sdk.Result{}, err
		}
		return sdk.Result{Data: mustJSON(map[string]any{"instance": args.Instance, "window": w.ID})}, nil

	case "window.fullscreen":
		on := args.Enabled == nil || *args.Enabled
		for _, w := range wins {
			if err := b.Fullscreen(ctx, w.ID, on); err != nil {
				return sdk.Result{}, err
			}
		}
		return sdk.Result{Data: mustJSON(map[string]any{"instance": args.Instance, "fullscreen": on})}, nil

	case "window.close":
		return m.closeInstance(ctx, b, args.Instance, wins)
	}
	return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "display module has no action %q", a.Type)
}

// closeInstance asks every window to close, as its close button would. If
// any is still open after CloseTimeout, the app is stopped.
func (m *Module) closeInstance(ctx context.Context, b Backend, instance string, wins []Window) (sdk.Result, error) {
	m.mu.Lock()
	var waits []chan struct{}
	for _, w := range wins {
		ch := make(chan struct{})
		m.closeWait[w.ID] = ch
		waits = append(waits, ch)
	}
	m.mu.Unlock()
	for _, w := range wins {
		if err := b.CloseWindow(ctx, w.ID); err != nil {
			m.log.Warn("closing window", "instance", instance, "err", err)
		}
	}
	timer := m.opts.Clock.NewTimer(m.opts.CloseTimeout)
	defer timer.Stop()
	closed := 0
	for _, ch := range waits {
		select {
		case <-ch:
			closed++
		case <-timer.C():
		case <-ctx.Done():
		}
	}
	m.mu.Lock()
	for _, w := range wins {
		delete(m.closeWait, w.ID)
	}
	core := m.core
	m.mu.Unlock()
	if closed == len(wins) {
		return sdk.Result{Data: mustJSON(map[string]any{"instance": instance, "closed": closed})}, nil
	}
	// Still open (a "save changes?" dialog, a hung app): stop it.
	if core == nil || !core.Handles("instance.stop") {
		return sdk.Result{}, sdk.Errorf(sdk.CodeTimeout, "%d of %d windows of %s did not close", len(wins)-closed, len(wins), instance)
	}
	if _, err := core.Do(ctx, sdk.Action{Type: "instance.stop", Args: mustJSON(map[string]string{"id": instance})}); err != nil &&
		sdk.CodeOf(err) != sdk.CodeInstanceNotRunning {
		return sdk.Result{}, err
	}
	return sdk.Result{Data: mustJSON(map[string]any{"instance": instance, "closed": closed, "stopped": true})}, nil
}

// --- reads -----------------------------------------------------------------

func (m *Module) Read(ctx context.Context, name string, _ map[string]string) (any, error) {
	m.mu.Lock()
	b := m.backend
	wins := make([]trackedWindow, 0, len(m.windows))
	for _, t := range m.windows {
		wins = append(wins, *t)
	}
	focused := ""
	if len(m.stack) > 0 {
		focused = m.stack[len(m.stack)-1]
	}
	m.mu.Unlock()
	sort.Slice(wins, func(i, j int) bool { return wins[i].ID < wins[j].ID })
	switch name {
	case "windows":
		return wins, nil
	case "display":
		st := map[string]any{"attached": b != nil, "focused_instance": focused, "outputs": []Output{}}
		if b != nil {
			if outs, err := b.Outputs(ctx); err == nil {
				st["outputs"] = outs
			}
		}
		return st, nil
	}
	return nil, sdk.Errorf(sdk.CodeNotFound, "display module has no read %q", name)
}

func (m *Module) State(ctx context.Context) (any, error) {
	st, _ := m.Read(ctx, "display", nil)
	wins, _ := m.Read(ctx, "windows", nil)
	out := st.(map[string]any)
	out["windows"] = wins
	return out, nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("display: %v", err))
	}
	return b
}
