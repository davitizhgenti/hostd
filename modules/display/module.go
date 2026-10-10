// Package display is the display module: it puts each app's windows on its
// own workspace, fullscreen by default, follows focus, and focuses, closes
// or fullscreens windows on request. Sway is the compositor (sway.go).
package display

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/davitizhgenti/hostd/contract"
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
	EventNav     = "display.nav"    // controller navigation for the menu in front
	EventGPUFull = "display.gpu.full"
	EventGPUOK   = "display.gpu.ok"
	EventOutputs = "display.output.changed"

	EventControllerConnected    = "controller.connected"
	EventControllerDisconnected = "controller.disconnected"
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
	// Profiles describe controllers (nil: the built-in ones), and
	// SysRoot is where sysfs and /dev are ("/" outside tests): for the
	// controller list.
	Profiles *Profiles
	SysRoot  string
	// Keys and Buttons bind inputs to actions (see Bindings; nil: the
	// defaults).
	Keys, Buttons map[string]string
	// GPU reads video memory, watched every GPUEvery (default 15s) so the
	// person is told before it is full (nil: not watched).
	GPU      GPUMemory
	GPUEvery time.Duration
	// HoldFor is how long a controller button is held before its action
	// runs; a shorter press is left to the app (default 600ms; negative:
	// on press).
	HoldFor time.Duration

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

// Module is the display module. Its files: windows.go (tracking, placing,
// focus history), launches.go (what it learns from the apps module),
// actions.go (actions and their checks), bindings.go and input.go (keys
// and buttons), presence.go, match.go (whose window is it), sway.go and
// ipc.go (the compositor).
//
// One mutex, mu, guards every field below it. It is never held while
// calling the compositor (Backend) or the core: take what is needed under
// the lock, release it, then call.
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
	holds     map[string]chan struct{} // controller buttons held down; closed on release
	menuFront bool                     // hostd's menu has the focus: controllers are taken for it
	grabbed   bool                     // the controllers are taken (Grabber)
	down      map[string]bool          // controller buttons held down
	gpuUsed   int64                    // video memory in use, bytes (GPU)
	gpuTotal  int64
	life      context.Context          // ends when the module stops
	failed    int                      // compositor commands that failed
	warned    map[string]time.Time     // when each kind of failure was last logged as a warning
	presence  *presence

	stop context.CancelFunc
	done sync.WaitGroup
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
	if opts.GPUEvery == 0 {
		opts.GPUEvery = 15 * time.Second
	}
	if opts.HoldFor == 0 {
		opts.HoldFor = 600 * time.Millisecond
	}
	m := &Module{opts: opts, log: opts.Logger, windows: map[int64]*trackedWindow{}, pressing: map[string]bool{}, holds: map[string]chan struct{}{}, down: map[string]bool{},
		keys: opts.Keys, buttons: opts.Buttons,
		prefs: map[string]bool{}, launches: map[string]*launch{}, closeWait: map[int64]chan struct{}{},
		ending: map[string]chan struct{}{}, gone: map[string]bool{}, warned: map[string]time.Time{}}
	if m.opts.Profiles == nil {
		m.opts.Profiles, _ = LoadProfiles("") // the built-in ones always load (a test checks)
	}
	if m.opts.SysRoot == "" {
		m.opts.SysRoot = "/"
	}
	m.presence = newPresence(opts.Clock, opts.IdleAfter, m.presenceChanged)
	return m
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
		// The apps module's instances are what the display module places,
		// focuses and closes (see contract).
		Requires: []string{contract.AppsModule},
		Owns:     []string{"display.*", "window.*", "controller.*"},
		Scopes: []sdk.ScopeSpec{
			{Name: "display", Description: "Display power and mode, window fullscreen and placement"},
		},
		Actions: []sdk.ActionSpec{
			{Type: "window.focus", Description: "Bring an instance's window to the front",
				Schema: focusArg, Keys: []sdk.KeyTemplate{"display.focus"}, Scope: contract.ScopeApps,
				ArgScopes: map[string]string{"front": contract.ScopeFront},
				Timeout:   sdk.Duration(10 * time.Second),
				Route:     &sdk.Route{Method: "POST", Path: "/v1/windows/{instance}/focus"}},
			{Type: "window.close", Description: "Close an instance's windows politely (default: the app in front); stop the app if they stay open",
				Schema: closeArg, Keys: []sdk.KeyTemplate{"display.close"}, Scope: contract.ScopeApps,
				Timeout: sdk.Duration(time.Minute),
				Route:   &sdk.Route{Method: "POST", Path: "/v1/windows/{instance}/close"}},
			{Type: "window.fullscreen", Description: "Turn fullscreen on or off for an instance's windows",
				Schema: fullscreenArg, Keys: []sdk.KeyTemplate{"instance:{instance}"}, Scope: "display",
				Timeout: sdk.Duration(10 * time.Second),
				Route:   &sdk.Route{Method: "POST", Path: "/v1/windows/{instance}/fullscreen"}},
			{Type: "window.back", Description: "Go back to the app that was in front before",
				Schema: noArgs, Keys: []sdk.KeyTemplate{"display.focus"}, Scope: contract.ScopeApps,
				Timeout: sdk.Duration(10 * time.Second),
				Route:   &sdk.Route{Method: "POST", Path: "/v1/windows/back"}},
			{Type: "window.next", Description: "Bring the next running app to the front",
				Schema: noArgs, Keys: []sdk.KeyTemplate{"display.focus"}, Scope: contract.ScopeApps,
				Timeout: sdk.Duration(10 * time.Second),
				Route:   &sdk.Route{Method: "POST", Path: "/v1/windows/next"}},
			{Type: "window.prev", Description: "Bring the previous running app to the front",
				Schema: noArgs, Keys: []sdk.KeyTemplate{"display.focus"}, Scope: contract.ScopeApps,
				Timeout: sdk.Duration(10 * time.Second),
				Route:   &sdk.Route{Method: "POST", Path: "/v1/windows/prev"}},
			{Type: "display.menu", Description: "Open hostd's on-screen menu, or go back if it is in front",
				Schema: noArgs, Keys: []sdk.KeyTemplate{"display.focus"}, Scope: contract.ScopeApps,
				Timeout: sdk.Duration(time.Minute),
				Route:   &sdk.Route{Method: "POST", Path: "/v1/display/menu"}},
			{Type: "window.place", Description: "Put an instance's window beside another's, side by side",
				Schema: placeArg, Keys: []sdk.KeyTemplate{"display.focus"}, Scope: "display",
				ArgScopes: map[string]string{"front": contract.ScopeFront}, Timeout: sdk.Duration(10 * time.Second),
				Route: &sdk.Route{Method: "POST", Path: "/v1/windows/{instance}/place"}},
			{Type: "display.power", Description: "Turn screens on or off (power saving)",
				Schema: powerArg, Keys: []sdk.KeyTemplate{"display.outputs"}, Scope: "display",
				ArgScopes: map[string]string{"front": contract.ScopeFront}, Timeout: sdk.Duration(10 * time.Second),
				Route: &sdk.Route{Method: "POST", Path: "/v1/display/power"}},
			{Type: "display.mode", Description: "Set a screen's resolution and refresh rate",
				Schema: modeArg, Keys: []sdk.KeyTemplate{"display.outputs"}, Scope: "display",
				ArgScopes: map[string]string{"front": contract.ScopeFront}, Timeout: sdk.Duration(10 * time.Second),
				Route: &sdk.Route{Method: "POST", Path: "/v1/display/outputs/{output}/mode"}},
			{Type: "display.output.enable", Description: "Use or stop using a screen",
				Schema: enableArg, Keys: []sdk.KeyTemplate{"display.outputs"}, Scope: "display",
				ArgScopes: map[string]string{"front": contract.ScopeFront}, Timeout: sdk.Duration(10 * time.Second),
				Route: &sdk.Route{Method: "POST", Path: "/v1/display/outputs/{output}/enable"}},
		},
		Events: []sdk.EventSpec{
			{Type: EventOpened, Description: "A window opened (instance is empty for windows hostd did not start)"},
			{Type: EventFocused, Description: "A window got the focus"},
			{Type: EventClosed, Description: "A window closed"},
			{Type: EventActive, Description: "Someone started using the screen (keyboard, mouse or controller input)"},
			{Type: EventIdle, Description: "Nobody has used the screen for a while"},
			{Type: EventNotice, Description: "A notice was shown on the screen, e.g. an app opened in the background"},
			{Type: EventGPUFull, Description: "Video memory is nearly full (90%); a notice asks to lower the game's settings"},
			{Type: EventGPUOK, Description: "Video memory is below 80% again"},
			{Type: EventNav, Description: "Controller navigation for hostd's menu while it is in front (up, down, left, right, choose, back, actions); apps do not see the controllers then"},
			{Type: EventOutputs, Description: "Screens were turned on or off, enabled, disabled or changed mode"},
			{Type: EventControllerConnected, Description: "A game controller was plugged in (see GET /v1/controllers)"},
			{Type: EventControllerDisconnected, Description: "A game controller was unplugged"},
		},
		Reads: []sdk.ReadSpec{
			{Name: "windows", Description: "All windows, including ones hostd did not start", Path: "/v1/windows"},
			{Name: "display", Description: "Whether a session is attached, outputs, the focused instance", Path: "/v1/display"},
			{Name: "controllers", Description: "Connected game controllers, with their profile and bindable buttons", Path: "/v1/controllers"},
		},
	}
}

// Start begins following the compositor and the apps' instances. A missing
// compositor is not an error: the module attaches when Sway appears.
func (m *Module) Start(_ context.Context, core sdk.Core) error {
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.core = core
	m.life = ctx
	m.mu.Unlock()
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
	m.done.Add(2)
	go func() { defer m.done.Done(); m.followControllers(ctx) }()
	go func() { defer m.done.Done(); m.followGPU(ctx, m.opts.GPU) }()
	// Subscribed before Start returns, so no event after it is missed.
	instances := core.Subscribe(ctx, contract.EventInstances)
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
	m.takeControllers(false)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.backend == b {
		m.backend = nil
	}
	m.windows = map[int64]*trackedWindow{}
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
	case "controllers":
		return scanControllers(sysfs{root: m.opts.SysRoot}, m.opts.Profiles), nil
	case "display":
		m.mu.Lock()
		failed := m.failed
		gpuUsed, gpuTotal := m.gpuUsed, m.gpuTotal
		m.mu.Unlock()
		st := map[string]any{"attached": b != nil, "focused_instance": focused, "outputs": []Output{},
			"present": m.presence.present(), "failed_commands": failed}
		if gpuTotal > 0 {
			st["gpu_memory"] = map[string]int64{"used": gpuUsed, "total": gpuTotal}
		}
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
