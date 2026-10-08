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
	"strings"
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
	EventActive  = "display.active" // someone started using the screen
	EventIdle    = "display.idle"   // no input for IdleAfter
	EventNotice  = "display.notice" // a notice shown on the screen
	EventOutputs = "display.output.changed"
)

// WorkspacePrefix names an instance's workspace: hostd:<instance>.
const WorkspacePrefix = "hostd:"

// Options configure the display module.
type Options struct {
	// Connect finds and connects to the compositor. It is called again
	// whenever the connection drops (Sway restarted, or not started yet).
	Connect  func(ctx context.Context) (Backend, error)
	ProcRoot string // default /proc

	// Input reports keyboard, mouse and controller activity. Without it
	// nobody is ever "at the screen" and every launch comes to the front.
	Input     Input
	IdleAfter time.Duration // no input this long = nobody there (default 5m)
	// Notifier shows notices such as "Firefox is ready" (optional).
	Notifier Notifier
	// Menu is the app display.menu opens: hostd's on-screen menu
	// (default hostd-menu).
	Menu string
	// Keys and Buttons bind inputs to actions (see Bindings; nil: the
	// defaults).
	Keys, Buttons map[string]string

	Clock        clock.Clock
	Logger       *slog.Logger
	RetryEvery   time.Duration // between connection attempts (default 2s)
	CloseTimeout time.Duration // how long window.close waits before stopping the app (default 5s)
	// EndWait is how long, after an app's last window closed, to wait for
	// the app itself to end before going back to the previous app, so it
	// is gone from every list by then (default 2s).
	EndWait time.Duration
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
	launches  map[string]*launch       // by instance
	ending    map[string]chan struct{} // instance -> closed when it ends
	gone      map[string]bool          // instances that ended (until the ID is reused)
	focusSeq  uint64                   // counts focus changes
	keys      map[string]string        // key name -> action
	buttons   map[string]string        // controller button -> action
	pressing  map[string]bool          // inputs whose action is being handled
	failed    int                      // compositor commands that failed
	warned    map[string]time.Time     // when each kind of failure was last logged as a warning
	presence  *presence

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
	if opts.EndWait == 0 {
		opts.EndWait = 2 * time.Second
	}
	if opts.IdleAfter == 0 {
		opts.IdleAfter = 5 * time.Minute
	}
	if opts.Menu == "" {
		opts.Menu = "hostd-menu"
	}
	if opts.Keys == nil {
		opts.Keys = DefaultKeys
	}
	if opts.Buttons == nil {
		opts.Buttons = DefaultButtons
	}
	m := &Module{opts: opts, log: opts.Logger, windows: map[int64]*trackedWindow{}, pressing: map[string]bool{},
		keys: opts.Keys, buttons: opts.Buttons,
		prefs: map[string]bool{}, launches: map[string]*launch{}, closeWait: map[int64]chan struct{}{},
		ending: map[string]chan struct{}{}, gone: map[string]bool{}, warned: map[string]time.Time{}}
	m.presence = newPresence(opts.Clock, opts.IdleAfter, m.presenceChanged)
	return m
}

// launch is how a started instance's windows are placed.
type launch struct {
	name  string
	front bool // its windows come to the front
	// noticed: a background window was announced with a notice already.
	noticed bool
}

var (
	focusArg = json.RawMessage(`{"type":"object","properties":{
		"instance":{"type":"string","description":"instance ID, e.g. firefox or firefox#2"},
		"front":{"type":"boolean","description":"even while someone is using the screen (needs scope display.front)"}},"required":["instance"]}`)
	closeArg = json.RawMessage(`{"type":"object","properties":{
		"instance":{"type":"string","description":"instance ID; default: the window in front"}}}`)
	noArgs   = json.RawMessage(`{"type":"object","properties":{}}`)
	placeArg = json.RawMessage(`{"type":"object","properties":{
		"instance":{"type":"string","description":"the instance to place"},
		"beside":{"type":"string","description":"the instance to place it next to"},
		"front":{"type":"boolean","description":"even while someone is using the screen (needs scope display.front)"}},"required":["instance","beside"]}`)
	powerArg = json.RawMessage(`{"type":"object","properties":{
		"on":{"type":"boolean","description":"true: screen on; false: off (power saving)"},
		"output":{"type":"string","description":"a screen name (see GET /v1/display); default all"},
		"front":{"type":"boolean","description":"even while someone is using the screen (needs scope display.front)"}},"required":["on"]}`)
	modeArg = json.RawMessage(`{"type":"object","properties":{
		"output":{"type":"string","description":"a screen name (see GET /v1/display)"},
		"mode":{"type":"string","description":"WIDTHxHEIGHT or WIDTHxHEIGHT@HZ, e.g. 1920x1080@60"},
		"front":{"type":"boolean","description":"even while someone is using the screen (needs scope display.front)"}},"required":["output","mode"]}`)
	enableArg = json.RawMessage(`{"type":"object","properties":{
		"output":{"type":"string","description":"a screen name (see GET /v1/display)"},
		"enabled":{"type":"boolean","description":"true (default) or false"},
		"front":{"type":"boolean","description":"even while someone is using the screen (needs scope display.front)"}},"required":["output"]}`)
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
		},
		Actions: []sdk.ActionSpec{
			{Type: "window.focus", Description: "Bring an instance's window to the front",
				Schema: focusArg, Keys: []sdk.KeyTemplate{"display.focus"}, Scope: "apps",
				ArgScopes: map[string]string{"front": "display.front"},
				Timeout:   sdk.Duration(10 * time.Second),
				Route:     &sdk.Route{Method: "POST", Path: "/v1/windows/{instance}/focus"}},
			{Type: "window.close", Description: "Close an instance's windows politely (default: the app in front); stop the app if they stay open",
				Schema: closeArg, Keys: []sdk.KeyTemplate{"display.close"}, Scope: "apps",
				Timeout: sdk.Duration(time.Minute),
				Route:   &sdk.Route{Method: "POST", Path: "/v1/windows/{instance}/close"}},
			{Type: "window.fullscreen", Description: "Turn fullscreen on or off for an instance's windows",
				Schema: fullscreenArg, Keys: []sdk.KeyTemplate{"instance:{instance}"}, Scope: "display",
				Timeout: sdk.Duration(10 * time.Second),
				Route:   &sdk.Route{Method: "POST", Path: "/v1/windows/{instance}/fullscreen"}},
			{Type: "window.back", Description: "Go back to the app that was in front before",
				Schema: noArgs, Keys: []sdk.KeyTemplate{"display.focus"}, Scope: "apps",
				Timeout: sdk.Duration(10 * time.Second),
				Route:   &sdk.Route{Method: "POST", Path: "/v1/windows/back"}},
			{Type: "window.next", Description: "Bring the next running app to the front",
				Schema: noArgs, Keys: []sdk.KeyTemplate{"display.focus"}, Scope: "apps",
				Timeout: sdk.Duration(10 * time.Second),
				Route:   &sdk.Route{Method: "POST", Path: "/v1/windows/next"}},
			{Type: "window.prev", Description: "Bring the previous running app to the front",
				Schema: noArgs, Keys: []sdk.KeyTemplate{"display.focus"}, Scope: "apps",
				Timeout: sdk.Duration(10 * time.Second),
				Route:   &sdk.Route{Method: "POST", Path: "/v1/windows/prev"}},
			{Type: "display.menu", Description: "Open hostd's on-screen menu, or go back if it is in front",
				Schema: noArgs, Keys: []sdk.KeyTemplate{"display.focus"}, Scope: "apps",
				Timeout: sdk.Duration(time.Minute),
				Route:   &sdk.Route{Method: "POST", Path: "/v1/display/menu"}},
			{Type: "window.place", Description: "Put an instance's window beside another's, side by side",
				Schema: placeArg, Keys: []sdk.KeyTemplate{"display.focus"}, Scope: "display",
				ArgScopes: map[string]string{"front": "display.front"}, Timeout: sdk.Duration(10 * time.Second),
				Route: &sdk.Route{Method: "POST", Path: "/v1/windows/{instance}/place"}},
			{Type: "display.power", Description: "Turn screens on or off (power saving)",
				Schema: powerArg, Keys: []sdk.KeyTemplate{"display.outputs"}, Scope: "display",
				ArgScopes: map[string]string{"front": "display.front"}, Timeout: sdk.Duration(10 * time.Second),
				Route: &sdk.Route{Method: "POST", Path: "/v1/display/power"}},
			{Type: "display.mode", Description: "Set a screen's resolution and refresh rate",
				Schema: modeArg, Keys: []sdk.KeyTemplate{"display.outputs"}, Scope: "display",
				ArgScopes: map[string]string{"front": "display.front"}, Timeout: sdk.Duration(10 * time.Second),
				Route: &sdk.Route{Method: "POST", Path: "/v1/display/outputs/{output}/mode"}},
			{Type: "display.output.enable", Description: "Use or stop using a screen",
				Schema: enableArg, Keys: []sdk.KeyTemplate{"display.outputs"}, Scope: "display",
				ArgScopes: map[string]string{"front": "display.front"}, Timeout: sdk.Duration(10 * time.Second),
				Route: &sdk.Route{Method: "POST", Path: "/v1/display/outputs/{output}/enable"}},
		},
		Events: []sdk.EventSpec{
			{Type: EventOpened, Description: "A window opened (instance is empty for windows hostd did not start)"},
			{Type: EventFocused, Description: "A window got the focus"},
			{Type: EventClosed, Description: "A window closed"},
			{Type: EventActive, Description: "Someone started using the screen (keyboard, mouse or controller input)"},
			{Type: EventIdle, Description: "Nobody has used the screen for a while"},
			{Type: EventNotice, Description: "A notice was shown on the screen, e.g. an app opened in the background"},
			{Type: EventOutputs, Description: "Screens were turned on or off, enabled, disabled or changed mode"},
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
	if m.opts.Input != nil {
		m.done.Add(2)
		go func() { defer m.done.Done(); m.presence.run(ctx) }()
		go func() {
			defer m.done.Done()
			if err := m.opts.Input.Watch(ctx, m.onInput); err != nil {
				m.log.Warn("cannot read input devices; every launch comes to the front", "err", err)
			}
		}()
	}
	m.done.Add(2)
	go func() { defer m.done.Done(); m.attachLoop(ctx) }()
	// Subscribed before Start returns, so no event after it is missed.
	instances := core.Subscribe(ctx, "instance.*")
	go func() { defer m.done.Done(); m.followInstances(ctx, instances) }()
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
			m.bind(ctx, b)
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
	resolve := m.resolver(ctx)
	insts := make([]string, len(wins))
	for i, w := range wins {
		insts[i] = resolve(w)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.backend = b
	m.windows = map[int64]*trackedWindow{}
	for i, w := range wins {
		tw := &trackedWindow{Window: w, Instance: insts[i]}
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
		if ev.Type == sdk.EventLagged {
			m.resyncInstances(ctx) // events were missed: read the state instead
			continue
		}
		var in struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Fullscreen *bool  `json:"fullscreen"`
			Front      bool   `json:"front"`
		}
		if json.Unmarshal(ev.Data, &in) != nil || in.ID == "" {
			continue
		}
		switch ev.Type {
		case "instance.starting", "instance.started":
			m.mu.Lock()
			delete(m.gone, in.ID) // the ID is in use again
			m.mu.Unlock()
			if ev.Type == "instance.starting" || !m.launchKnown(in.ID) {
				m.learnLaunch(ctx, in.ID, in.Name, ev.Source, in.Front)
			}
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
					m.screen("fullscreen", b.Fullscreen(ctx, id, false))
				}
			}
		case "instance.exited", "instance.failed":
			m.mu.Lock()
			delete(m.prefs, in.ID)
			delete(m.launches, in.ID)
			m.gone[in.ID] = true
			if ch, ok := m.ending[in.ID]; ok {
				close(ch)
				delete(m.ending, in.ID)
			}
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
func (m *Module) onEvent(ev Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w := ev.Window
	switch ev.Change {
	case "binding":
		m.presence.touch() // a key in the session: someone is there (VNC too)
		m.press("keyboard", ev.Binding)
	case "reload": // the compositor dropped hostd's bindings
		m.mu.Lock()
		b := m.backend
		m.mu.Unlock()
		if b != nil {
			m.bind(ctx, b)
		}
	case "new":
		inst := m.resolver(ctx)(w)
		m.mu.Lock()
		tw := &trackedWindow{Window: w, Instance: inst}
		m.windows[w.ID] = tw
		full, known := m.prefs[inst]
		front, announce := m.placement(inst)
		b := m.backend
		m.mu.Unlock()
		if inst != "" && b != nil {
			// Its own workspace, fullscreen unless the app says otherwise.
			// In front, unless someone else is using the screen and did
			// not ask for it: then it waits on its workspace, announced.
			ws := WorkspacePrefix + inst
			if err := b.Move(ctx, w.ID, ws); err != nil {
				m.log.Warn("placing window", "instance", inst, "err", err)
			}
			if front {
				m.screen("show", b.Show(ctx, ws))
				m.screen("focus", b.Focus(ctx, w.ID))
			}
			if full || !known {
				m.screen("fullscreen", b.Fullscreen(ctx, w.ID, true))
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
		if announce != "" {
			m.notice(inst, announce+" is ready")
		}
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
		seq := m.focusSeq
		m.mu.Unlock()
		if !ok {
			tw = &trackedWindow{Window: w}
		}
		m.emit(EventClosed, tw)
		if back != nil && b != nil {
			// The app closes first, then the screen goes back: wait for it
			// to end, so the previous app (the menu, say) never lists
			// it as running. Unless someone moved on in the meantime.
			closing := tw.Instance
			m.done.Add(1)
			go func() {
				defer m.done.Done()
				m.waitEnded(context.Background(), closing, m.opts.EndWait)
				m.mu.Lock()
				moved := m.focusSeq != seq
				m.mu.Unlock()
				if moved {
					return
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				m.screen("show", b.Show(ctx, back.Workspace))
				m.screen("focus", b.Focus(ctx, back.ID))
			}()
		}
	case "focus":
		m.mu.Lock()
		for _, t := range m.windows {
			t.Focused = t.ID == w.ID
		}
		m.focusSeq++
		tw, ok := m.windows[w.ID]
		if ok && tw.Instance != "" {
			m.pushFocus(tw.Instance)
			// Apps that showed a window and were then left behind do not
			// jump back to the front with their next window.
			for inst, l := range m.launches {
				if inst != tw.Instance && m.hasWindow(inst) {
					l.front = false
				}
			}
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
	resolve := m.resolver(ctx)
	var out []Window
	for _, w := range all {
		if resolve(w) == instance {
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
	switch a.Type {
	case "display.power", "display.mode", "display.output.enable":
		_, _, err := m.outputArgs(ctx, a)
		return err
	case "window.place":
		_, _, _, err := m.placeArgs(ctx, a)
		return err
	case "window.back", "window.next", "window.prev", "display.menu":
		return m.attached()
	case "window.close":
		var args instanceArgs
		if err := a.DecodeArgs(&args); err != nil {
			return err
		}
		if args.Instance == "" {
			_, _, err := m.frontWindow(ctx)
			return err
		}
	}
	var args instanceArgs
	if err := a.DecodeArgs(&args); err != nil {
		return err
	}
	_, _, err := m.windowsOf(ctx, args.Instance)
	return err
}

func (m *Module) Handle(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	switch a.Type {
	case "display.power", "display.mode", "display.output.enable":
		return m.handleOutput(ctx, a)
	case "window.place":
		return m.handlePlace(ctx, a)
	case "window.back":
		return m.handleBack(ctx, a)
	case "window.next", "window.prev":
		return m.handleCycle(ctx, a)
	case "display.menu":
		return m.handleMenu(ctx, a)
	case "window.close":
		var args instanceArgs
		if err := a.DecodeArgs(&args); err != nil {
			return sdk.Result{}, err
		}
		if args.Instance == "" {
			return m.closeFront(ctx)
		}
	}
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
		if !wins[0].Focused && m.inUse(a) {
			// Someone at the screen is in the middle of something; a phone
			// or script does not take it from them. Tell them instead.
			m.notice(args.Instance, m.nameOf(args.Instance)+" wants the screen")
			return sdk.Result{Status: sdk.StatusSkipped, Reason: "in_use",
				Data: mustJSON(map[string]any{"instance": args.Instance, "focused": false})}, nil
		}
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
		// Report done once the app has ended too, so whoever asked (the
		// menu) lists it no more.
		m.waitEnded(ctx, instance, m.opts.EndWait)
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

// inUse reports whether someone at the screen should keep it: the action
// does not come from them and does not insist (front=true).
func (m *Module) inUse(a sdk.Action) bool {
	var args struct {
		Front bool `json:"front"`
	}
	_ = a.DecodeArgs(&args)
	return a.Source.Kind != sdk.SourceLocal && !args.Front && m.presence.present()
}

// skippedInUse answers an action refused for inUse, telling the person.
func (m *Module) skippedInUse(instance, text string, data any) (sdk.Result, error) {
	m.notice(instance, text)
	return sdk.Result{Status: sdk.StatusSkipped, Reason: "in_use", Data: mustJSON(data)}, nil
}

// --- presence and launches -------------------------------------------------

// onInput is called for every input from every device.
func (m *Module) onInput(ev InputEvent) {
	m.presence.touch()
	for _, b := range ev.Buttons {
		m.press("controller", b)
	}
}

// resyncInstances rebuilds what the module learns from instance events,
// from the running instances, after events were missed: fullscreen
// preferences, and the end of instances someone is waiting for.
func (m *Module) resyncInstances(ctx context.Context) {
	live := m.liveInstances(ctx)
	running := map[string]bool{}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, in := range live {
		running[in.ID] = true
		if in.Fullscreen != nil {
			m.prefs[in.ID] = *in.Fullscreen
		}
	}
	for id, ch := range m.ending {
		if !running[id] {
			m.gone[id] = true
			close(ch)
			delete(m.ending, id)
		}
	}
	for id := range m.prefs {
		if !running[id] {
			delete(m.prefs, id)
		}
	}
	for id := range m.launches {
		if !running[id] {
			delete(m.launches, id)
		}
	}
}

// screen notes a compositor command that failed. Placing and focusing is
// done on a best-effort basis (the window may have closed meanwhile), but
// a failure must show: a warning, at most once a minute for each kind,
// the rest at debug level, and a count in GET /v1/display.
func (m *Module) screen(op string, err error) {
	if err == nil {
		return
	}
	now := m.opts.Clock.Now()
	m.mu.Lock()
	m.failed++
	warn := now.Sub(m.warned[op]) >= time.Minute
	if warn {
		m.warned[op] = now
	}
	m.mu.Unlock()
	if warn {
		m.log.Warn("compositor command failed", "op", op, "err", err)
	} else {
		m.log.Debug("compositor command failed", "op", op, "err", err)
	}
}

// waitEnded waits until an instance has ended, at most d.
func (m *Module) waitEnded(ctx context.Context, instance string, d time.Duration) {
	m.mu.Lock()
	if m.gone[instance] {
		m.mu.Unlock()
		return
	}
	ch, ok := m.ending[instance]
	if !ok {
		ch = make(chan struct{})
		m.ending[instance] = ch
	}
	m.mu.Unlock()
	timer := m.opts.Clock.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ch:
	case <-timer.C():
	case <-ctx.Done():
	}
}

// backTarget returns a window of the instance focused before the current
// one, or nil. Caller holds m.mu.
func (m *Module) backTarget() *trackedWindow {
	n := len(m.stack)
	if n == 0 {
		return nil
	}
	top := m.stack[n-1]
	m.stack = m.stack[:n-1]
	back := m.previous()
	m.stack = append(m.stack, top)
	return back
}

func (m *Module) presenceChanged(present bool) {
	typ := EventIdle
	if present {
		typ = EventActive
	}
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	if core != nil {
		core.Emit(sdk.Event{Type: typ, Data: mustJSON(map[string]bool{"present": present}),
			Source: &sdk.Source{Kind: sdk.SourceExternal, Name: "input"}})
	}
}

func (m *Module) launchKnown(instance string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.launches[instance]
	return ok
}

// learnLaunch records where a starting instance's windows go. Someone at
// the screen keeps it unless they started the app themselves or the
// sender asked for the front (which needs scope display.front). A window
// that opened before this was known went to the background; it comes
// forward now if it should have.
func (m *Module) learnLaunch(ctx context.Context, instance, name string, src *sdk.Source, front bool) {
	local := src != nil && src.Kind == sdk.SourceLocal
	l := &launch{name: name, front: local || front || !m.presence.present()}
	m.mu.Lock()
	old, placed := m.launches[instance]
	if placed {
		l.noticed = old.noticed
	}
	m.launches[instance] = l
	var bring *trackedWindow
	if l.front && placed && !old.front {
		for _, t := range m.windows {
			if t.Instance == instance && !t.Focused {
				c := *t
				bring = &c
				break
			}
		}
	}
	b := m.backend
	m.mu.Unlock()
	if bring != nil && b != nil {
		m.screen("show", b.Show(ctx, WorkspacePrefix+instance))
		m.screen("focus", b.Focus(ctx, bring.ID))
	}
}

// placement decides whether a new window of instance comes to the front,
// and returns the app's name if it goes to the background unannounced so
// far. Caller holds m.mu.
func (m *Module) placement(instance string) (front bool, announce string) {
	if instance == "" {
		return false, ""
	}
	l, ok := m.launches[instance]
	if !ok {
		// Its start is not known yet (the window beat the event) or it
		// was adopted: front only if nobody is using the screen.
		if !m.presence.present() {
			return true, ""
		}
		l = &launch{name: instance}
		m.launches[instance] = l
	}
	if l.front || !m.presence.present() {
		return true, ""
	}
	if l.noticed {
		return false, ""
	}
	l.noticed = true
	name := l.name
	if name == "" {
		name = instance
	}
	return false, name
}

func (m *Module) nameOf(instance string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.launches[instance]; ok && l.name != "" {
		return l.name
	}
	return instance
}

// notice tells the person at the screen something, as an event and, when
// a notification daemon runs, on the screen.
func (m *Module) notice(instance, text string) {
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	if core != nil {
		core.Emit(sdk.Event{Type: EventNotice, Data: mustJSON(map[string]string{"instance": instance, "text": text})})
	}
	if m.opts.Notifier != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := m.opts.Notifier.Notify(ctx, text, ""); err != nil {
			m.log.Debug("showing notice", "text", text, "err", err)
		}
	}
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
		m.mu.Lock()
		failed := m.failed
		m.mu.Unlock()
		st := map[string]any{"attached": b != nil, "focused_instance": focused, "outputs": []Output{},
			"present": m.presence.present(), "failed_commands": failed}
		if last := m.presence.lastInput(); !last.IsZero() {
			st["last_input"] = last.UTC()
		}
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

// appOfInstance is the app part of an instance ID ("foot#2" -> "foot").
func appOfInstance(id string) string {
	app, _, _ := strings.Cut(id, "#")
	return app
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("display: %v", err))
	}
	return b
}
