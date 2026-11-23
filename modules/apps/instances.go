package apps

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/sdk"
)

// Instance events.
const (
	EventStarting = contract.EventInstanceStarting
	EventStarted  = contract.EventInstanceStarted
	EventExited   = contract.EventInstanceExited
	EventFailed   = contract.EventInstanceFailed
)

// keepEnded is how many ended instances stay visible (hostctl ps --all).
const keepEnded = 50

func instanceEvents() []sdk.EventSpec {
	return []sdk.EventSpec{
		{Type: EventStarting, Description: "An instance is starting"},
		{Type: EventStarted, Description: "An instance is running"},
		{Type: EventExited, Description: "An instance ended normally or was stopped"},
		{Type: EventFailed, Description: "An instance could not start, or ended with an error"},
		{Type: EventUnhealthy, Description: "A background app failed its health check 3 times in a row"},
		{Type: EventHealthy, Description: "A background app passes its health check again"},
	}
}

// live returns the instances of app that have not ended, by ID.
func (m *Module) live(app string) []*Instance {
	var out []*Instance
	for _, in := range m.instances {
		if in.App == app && !in.State.Ended() {
			out = append(out, in)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (m *Module) validateStart(_ context.Context, _ sdk.Action, args contract.AppStart) error {
	app, ok := m.catalog().Get(args.ID)
	if !ok {
		return sdk.Errorf(sdk.CodeNotFound, "no app %q (see hostctl apps --all)", args.ID)
	}
	if _, err := withArgs(app, args.Args); err != nil {
		return err
	}
	if args.Action != "" {
		if _, ok := app.Action(args.Action); !ok {
			return sdk.Errorf(sdk.CodeNotFound, "app %q has no action %q%s", app.ID, args.Action, actionList(app))
		}
		return nil // actions run as commands, by the exec runner
	}
	if m.backend(app.Runner.Type) == nil {
		return sdk.Errorf(sdk.CodeModuleUnavailable, "app %q uses the %s runner, which hostd cannot run yet", app.ID, app.Runner.Type)
	}
	return nil
}

// actionList names an app's actions for an error message.
func actionList(app *App) string {
	if len(app.Actions) == 0 {
		return " (it has none)"
	}
	ids := make([]string, len(app.Actions))
	for i, x := range app.Actions {
		ids[i] = x.ID
	}
	return "; it has: " + strings.Join(ids, ", ")
}

func (m *Module) validateStop(_ context.Context, _ sdk.Action, args instanceArgs) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	in, ok := m.instances[args.ID]
	switch {
	case !ok:
		for _, e := range m.ended {
			if e.ID == args.ID {
				return sdk.Errorf(sdk.CodeInstanceNotRunning, "instance %q has %s", args.ID, e.State)
			}
		}
		return sdk.Errorf(sdk.CodeNotFound, "no instance %q (see hostctl ps)", args.ID)
	case in.State.Ended():
		return sdk.Errorf(sdk.CodeInstanceNotRunning, "instance %q has %s", args.ID, in.State)
	}
	return nil
}

// startResult is the data of an app.open result.
type startResult struct {
	Instance       string `json:"instance"`
	App            string `json:"app"`
	State          State  `json:"state"`
	AlreadyRunning bool   `json:"already_running,omitempty"`
}

func (m *Module) handleStart(ctx context.Context, a sdk.Action, args contract.AppStart) (sdk.Result, error) {
	app, ok := m.catalog().Get(args.ID)
	if !ok {
		return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "no app %q", args.ID)
	}
	app, err := withArgs(app, args.Args)
	if err != nil {
		return sdk.Result{}, err
	}
	if args.Action != "" {
		// One of the app's actions: its own command, always a new
		// instance (a private window next to the normal one).
		act, ok := app.Action(args.Action)
		if !ok {
			return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "app %q has no action %q%s", app.ID, args.Action, actionList(app))
		}
		variant := *app
		variant.Runner = Runner{Type: RunnerExec, Command: act.Command, Handoff: app.Runner.Handoff}
		variant.Instance.Policy = "multiple"
		app = &variant
	}
	// What it requires starts first (a Steam game: Steam itself).
	for _, req := range app.Requires {
		m.mu.Lock()
		up := len(m.live(req)) > 0
		m.mu.Unlock()
		if up {
			continue
		}
		if _, err := m.core.Do(ctx, sdk.Action{Type: contract.ActionAppOpen, Args: sdk.MustJSON(map[string]any{"id": req})}); err != nil {
			return sdk.Result{}, sdk.Errorf(sdk.CodeOf(err), "%s requires %s: %v", app.ID, req, err)
		}
	}
	app, err = m.withSecrets(app)
	if err != nil {
		return sdk.Result{}, err
	}
	backend := m.backend(app.Runner.Type)

	m.mu.Lock()
	running := m.live(app.ID)
	var first Instance
	if len(running) > 0 {
		first = *running[0]
	}
	m.mu.Unlock()
	if p, ok := backend.(Passer); ok && args.Action != "" && app.Runner.Handoff != "" {
		// The app's program takes the action's request itself (Steam's
		// Big Picture): as an instance of its own, the action would last
		// as long as that program. If it does not run, the app itself is
		// started first, and the request follows once it is up.
		if len(running) == 0 {
			return m.startThenPass(ctx, p, app, args.Front)
		}
		if err := p.Pass(ctx, first, app); err != nil {
			return sdk.Result{}, sdk.Errorf(sdk.CodeInternal, "%s: %v", app.ID, err)
		}
		return m.focusRunning(ctx, app, first, args.Front)
	}
	if app.Instance.Policy == "single" && len(running) > 0 && !args.New {
		ifRunning := app.Instance.IfRunning
		if len(args.Args) > 0 {
			ifRunning = "restart" // new values: the running copy shows the old ones
		}
		switch ifRunning {
		case "focus":
			return m.focusRunning(ctx, app, first, args.Front)
		case "restart":
			for _, in := range running {
				if _, err := m.core.Do(ctx, sdk.Action{Type: contract.ActionInstanceStop, Args: sdk.MustJSON(map[string]string{"id": in.ID})}); err != nil &&
					sdk.CodeOf(err) != sdk.CodeInstanceNotRunning {
					return sdk.Result{}, err
				}
			}
		}
		// "new": fall through and start another copy
	}
	return m.launch(ctx, a.ID, app, backend, args)
}

// launch starts a new instance of app: app.open's, and app.run's for a
// command with no app file.
func (m *Module) launch(ctx context.Context, actionID string, app *App, backend Backend, args contract.AppStart) (sdk.Result, error) {
	m.mu.Lock()
	id := nextID(app.ID, func(id string) bool {
		in, ok := m.instances[id]
		return ok && !in.State.Ended()
	})
	inst := &Instance{ID: id, App: app.ID, Name: app.Name, Runner: app.Runner.Type, Surface: app.Surface,
		Fullscreen: app.Surface == SurfaceWindow && app.Window.Fullscreen, Front: args.Front, Match: matchRules(app), ShownBy: app.Window.ShownBy, Volume: app.Audio.Volume,
		Dir: args.Dir, Env: args.Env, Image: args.Image, Ports: args.Ports,
		Action: args.Action,
		State:  StateStarting, Started: m.opts.Clock.Now().UTC()}
	m.instances[id] = inst
	m.mu.Unlock()
	m.emitInstance(EventStarting, actionID, *inst)

	started, err := backend.Start(ctx, *inst, app)

	m.mu.Lock()
	cur := m.instances[id]
	if cur == nil {
		// It already ended (an app that exits at once): the watcher has
		// reported that; report what became of it.
		state := StateExited
		for _, e := range m.ended {
			if e.ID == id {
				state = e.State
				break
			}
		}
		m.mu.Unlock()
		if err != nil {
			return sdk.Result{}, sdk.Errorf(sdk.CodeInternal, "%s: %v", app.ID, err)
		}
		return sdk.Result{Data: sdk.MustJSON(startResult{Instance: id, App: app.ID, State: state})}, nil
	}
	if err != nil {
		cur.State = StateFailed
		cur.Error = err.Error()
		now := m.opts.Clock.Now().UTC()
		cur.Ended = &now
		m.retire(id)
		snapshot := *cur
		m.mu.Unlock()
		m.emitInstance(EventFailed, actionID, snapshot)
		var se *sdk.Error
		if errors.As(err, &se) {
			return sdk.Result{}, err
		}
		return sdk.Result{}, sdk.Errorf(sdk.CodeInternal, "%s: %v", app.ID, err)
	}
	cur.Unit, cur.Container, cur.PID = started.Unit, started.Container, started.PID
	if started.ExitCode != nil { // it ended while starting
		cur.State, _ = next(cur.State, changeEnded, *started.ExitCode)
		cur.ExitCode = started.ExitCode
		now := m.opts.Clock.Now().UTC()
		cur.Ended = &now
		typ := EventExited
		if cur.State == StateFailed {
			cur.Error, typ = started.Error, EventFailed
		}
		m.retire(id)
		snapshot := *cur
		m.mu.Unlock()
		m.emitInstance(typ, actionID, snapshot)
		return sdk.Result{Data: sdk.MustJSON(startResult{Instance: id, App: app.ID, State: snapshot.State})}, nil
	}
	emitStarted := false
	gated := app.Health.Set() && m.life != nil
	if !gated {
		if s, err := next(cur.State, changeStarted, 0); err == nil && s != cur.State {
			cur.State = s
			emitStarted = true
		}
	}
	snapshot := *cur
	life := m.life
	m.mu.Unlock()
	if gated {
		// Running once its health check passes (health.go).
		m.watchersRun.Add(1)
		h := healthFor(app.Health, *inst)
		go func() { defer m.watchersRun.Done(); m.watchHealth(life, id, h) }()
	}
	if emitStarted {
		m.emitInstance(EventStarted, actionID, snapshot)
	}
	if life != nil && (app.Runner.Type == RunnerExec || app.Runner.Type == RunnerProcess) {
		m.watchersRun.Add(1)
		go func() { defer m.watchersRun.Done(); m.checkEarlyEnd(life, app.Runner.Type, backend) }()
	}
	return sdk.Result{Data: sdk.MustJSON(startResult{Instance: id, App: app.ID, State: snapshot.State})}, nil
}

// autostart starts the apps marked autostart that do not run yet: at
// hostd's start, which is also after a reboot (their units, transient,
// did not survive it). Through the pipeline, so it is in the audit trail.
func (m *Module) autostart(ctx context.Context) {
	var ids []string
	apps := m.catalog().List(true)
	m.mu.Lock()
	for _, a := range apps {
		if !a.Autostart {
			continue
		}
		running := false
		for _, in := range m.instances {
			running = running || (in.App == a.ID && !in.State.Ended())
		}
		if !running {
			ids = append(ids, a.ID)
		}
	}
	core := m.core
	m.mu.Unlock()
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		if _, err := core.Do(ctx, sdk.Action{Type: contract.ActionAppOpen, Args: sdk.MustJSON(map[string]string{"id": id})}); err != nil {
			m.log.Warn("autostart", "app", id, "err", err)
		} else {
			m.log.Info("started at hostd's start", "app", id)
		}
	}
}

const (
	findEvery = 2 * time.Second
	// findAfterEnd: an app that just ended is not looked for again for
	// this long, while its processes finish.
	findAfterEnd = 30 * time.Second
)

// startThenPass starts a handoff app for one of its actions, then hands
// the action's request to it in the background: the program may take
// minutes to be ready (Steam updates itself first).
func (m *Module) startThenPass(ctx context.Context, p Passer, action *App, front bool) (sdk.Result, error) {
	res, err := m.core.Do(ctx, sdk.Action{Type: contract.ActionAppOpen, Args: sdk.MustJSON(map[string]any{"id": action.ID, "front": front})})
	if err != nil {
		return sdk.Result{}, err
	}
	var started startResult
	_ = json.Unmarshal(res.Data, &started)
	m.mu.Lock()
	in, ok := m.instances[started.Instance]
	var inst Instance
	if ok {
		inst = *in
	}
	life := m.life
	m.mu.Unlock()
	if !ok || life == nil {
		return res, nil
	}
	m.watchersRun.Add(1)
	go func() {
		defer m.watchersRun.Done()
		pctx, cancel := context.WithTimeout(life, 5*time.Minute)
		defer cancel()
		if err := p.Pass(pctx, inst, action); err != nil && life.Err() == nil {
			m.log.Warn("handing an action to its app", "app", action.ID, "err", err)
		}
	}()
	return res, nil
}

// focusRunning brings a running instance forward. Without the display
// module (headless, or before it exists) there is nothing to focus.
func (m *Module) focusRunning(ctx context.Context, app *App, in Instance, front bool) (sdk.Result, error) {
	if in.Surface == SurfaceWindow && m.core.Handles(contract.ActionWindowFocus) {
		if _, err := m.core.Do(ctx, sdk.Action{Type: contract.ActionWindowFocus, Args: sdk.MustJSON(map[string]any{"instance": in.ID, "front": front})}); err != nil {
			return sdk.Result{}, err
		}
	}
	return sdk.Result{Data: sdk.MustJSON(startResult{Instance: in.ID, App: app.ID, State: in.State, AlreadyRunning: true})}, nil
}
