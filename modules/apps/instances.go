package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/internal/clock"
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

var (
	startSchema = json.RawMessage(`{"type":"object","properties":{
		"id":{"type":"string","description":"app ID or alias"},
		"front":{"type":"boolean","description":"open in front even while someone is using the screen (needs scope display.front)"},
		"action":{"type":"string","description":"one of the app's actions (e.g. new-private-window): always a new instance"}},"required":["id"]}`)
	instanceSchema = json.RawMessage(`{"type":"object","properties":{
		"id":{"type":"string","description":"instance ID, e.g. firefox or firefox#2"}},"required":["id"]}`)
)

func instanceActions() []sdk.ActionSpec {
	return []sdk.ActionSpec{
		{Type: contract.ActionAppStart, Description: "Start an app (if it already runs: focus, a new copy, or restart, per its settings)",
			Schema: startSchema, Keys: []sdk.KeyTemplate{"app:{id}"}, Scope: contract.ScopeApps,
			ArgScopes: map[string]string{"front": contract.ScopeFront},
			Timeout:   sdk.Duration(5 * time.Minute), // pulling a container image can take a while
			Route:     &sdk.Route{Method: "POST", Path: "/v1/apps/{id}/start"}},
		{Type: contract.ActionInstanceStop, Description: "Stop a running instance",
			Schema: instanceSchema, Keys: []sdk.KeyTemplate{"instance:{id}"}, Scope: contract.ScopeApps,
			Timeout: sdk.Duration(time.Minute),
			Route:   &sdk.Route{Method: "POST", Path: "/v1/instances/{id}/stop"}},
		{Type: contract.ActionInstanceClosing, Description: "Note that an instance was asked to close: its end counts as a normal exit",
			Schema: instanceSchema, Keys: []sdk.KeyTemplate{"instance:{id}"}, Scope: contract.ScopeApps,
			Timeout: sdk.Duration(10 * time.Second)},
	}
}

func instanceEvents() []sdk.EventSpec {
	return []sdk.EventSpec{
		{Type: EventStarting, Description: "An instance is starting"},
		{Type: EventStarted, Description: "An instance is running"},
		{Type: EventExited, Description: "An instance ended normally or was stopped"},
		{Type: EventFailed, Description: "An instance could not start, or ended with an error"},
	}
}

func instanceReads() []sdk.ReadSpec {
	return []sdk.ReadSpec{
		{Name: contract.ReadInstances, Description: "Running instances (?all=true adds recently ended ones)", Path: "/v1/instances"},
		{Name: "instance", Description: "One instance", Path: "/v1/instances/{id}"},
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

func (m *Module) validateStart(a sdk.Action) error {
	var args struct{ ID, Action string }
	if err := a.DecodeArgs(&args); err != nil {
		return err
	}
	app, ok := m.catalog().Get(args.ID)
	if !ok {
		return sdk.Errorf(sdk.CodeNotFound, "no app %q (see hostctl apps --all)", args.ID)
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

func (m *Module) validateStop(a sdk.Action) error {
	var args struct{ ID string }
	if err := a.DecodeArgs(&args); err != nil {
		return err
	}
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

// startResult is the data of an app.start result.
type startResult struct {
	Instance       string `json:"instance"`
	App            string `json:"app"`
	State          State  `json:"state"`
	AlreadyRunning bool   `json:"already_running,omitempty"`
}

func (m *Module) handleStart(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	var args struct {
		ID, Action string
		Front      bool
	}
	if err := a.DecodeArgs(&args); err != nil {
		return sdk.Result{}, err
	}
	app, ok := m.catalog().Get(args.ID)
	if !ok {
		return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "no app %q", args.ID)
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
		if _, err := m.core.Do(ctx, sdk.Action{Type: contract.ActionAppStart, Args: sdk.MustJSON(map[string]any{"id": req})}); err != nil {
			return sdk.Result{}, sdk.Errorf(sdk.CodeOf(err), "%s requires %s: %v", app.ID, req, err)
		}
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
	if app.Instance.Policy == "single" && len(running) > 0 {
		switch app.Instance.IfRunning {
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

	m.mu.Lock()
	id := nextID(app.ID, func(id string) bool {
		in, ok := m.instances[id]
		return ok && !in.State.Ended()
	})
	inst := &Instance{ID: id, App: app.ID, Name: app.Name, Runner: app.Runner.Type, Surface: app.Surface,
		Fullscreen: app.Surface == SurfaceWindow && app.Window.Fullscreen, Front: args.Front, Match: app.matchRules(), ShownBy: app.Window.ShownBy, Volume: app.Audio.Volume,
		Action: args.Action,
		State:  StateStarting, Started: m.opts.Clock.Now().UTC()}
	m.instances[id] = inst
	m.mu.Unlock()
	m.emitInstance(EventStarting, a.ID, *inst)

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
		m.emitInstance(EventFailed, a.ID, snapshot)
		var se *sdk.Error
		if errors.As(err, &se) {
			return sdk.Result{}, err
		}
		return sdk.Result{}, sdk.Errorf(sdk.CodeInternal, "%s: %v", app.ID, err)
	}
	cur.Unit, cur.Container, cur.PID = started.Unit, started.Container, started.PID
	emitStarted := false
	if s, err := next(cur.State, changeStarted, 0); err == nil && s != cur.State {
		cur.State = s
		emitStarted = true
	}
	snapshot := *cur
	m.mu.Unlock()
	if emitStarted {
		m.emitInstance(EventStarted, a.ID, snapshot)
	}
	return sdk.Result{Data: sdk.MustJSON(startResult{Instance: id, App: app.ID, State: snapshot.State})}, nil
}

// startThenPass starts a handoff app for one of its actions, then hands
// the action's request to it in the background: the program may take
// minutes to be ready (Steam updates itself first).
func (m *Module) startThenPass(ctx context.Context, p Passer, action *App, front bool) (sdk.Result, error) {
	res, err := m.core.Do(ctx, sdk.Action{Type: "app.start", Args: sdk.MustJSON(map[string]any{"id": action.ID, "front": front})})
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
	if in.Surface == SurfaceWindow && m.core.Handles("window.focus") {
		if _, err := m.core.Do(ctx, sdk.Action{Type: "window.focus", Args: sdk.MustJSON(map[string]any{"instance": in.ID, "front": front})}); err != nil {
			return sdk.Result{}, err
		}
	}
	return sdk.Result{Data: sdk.MustJSON(startResult{Instance: in.ID, App: app.ID, State: in.State, AlreadyRunning: true})}, nil
}

// handleClosing marks an instance as asked to close: its end will count as
// a normal exit.
func (m *Module) handleClosing(a sdk.Action) (sdk.Result, error) {
	var args struct{ ID string }
	if err := a.DecodeArgs(&args); err != nil {
		return sdk.Result{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if in, ok := m.instances[args.ID]; ok {
		in.closing = true
	}
	return sdk.Result{Data: sdk.MustJSON(map[string]string{"instance": args.ID})}, nil
}

func (m *Module) handleStop(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	var args struct{ ID string }
	if err := a.DecodeArgs(&args); err != nil {
		return sdk.Result{}, err
	}
	m.mu.Lock()
	in, ok := m.instances[args.ID]
	if !ok || in.State.Ended() {
		m.mu.Unlock()
		return sdk.Result{}, sdk.Errorf(sdk.CodeInstanceNotRunning, "instance %q is not running", args.ID)
	}
	in.State, _ = next(in.State, changeStop, 0)
	snapshot := *in
	backend := m.backend(in.Runner)
	m.mu.Unlock()

	if backend == nil {
		m.unstop(args.ID)
		return sdk.Result{}, sdk.Errorf(sdk.CodeModuleUnavailable, "no %s runner", snapshot.Runner)
	}
	if err := backend.Stop(ctx, snapshot); err != nil {
		// It may still be running: put it back, so stopping can be retried.
		m.unstop(args.ID)
		return sdk.Result{}, err
	}
	m.mu.Lock()
	in = m.instances[args.ID]
	if in == nil || in.State.Ended() { // the watcher saw it end first
		m.mu.Unlock()
		return sdk.Result{Data: sdk.MustJSON(map[string]string{"instance": args.ID, "state": string(StateExited)})}, nil
	}
	in.State, _ = next(in.State, changeEnded, 0)
	now := m.opts.Clock.Now().UTC()
	in.Ended = &now
	m.retire(args.ID)
	snapshot = *in
	m.mu.Unlock()
	m.emitInstance(EventExited, a.ID, snapshot)
	return sdk.Result{Data: sdk.MustJSON(map[string]string{"instance": args.ID, "state": string(snapshot.State)})}, nil
}

// unstop puts an instance whose stop failed back to running.
func (m *Module) unstop(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if in, ok := m.instances[id]; ok && in.State == StateStopping {
		in.State = StateRunning
	}
}

// onEnded handles an instance that ended on its own (the user closed the
// app, it crashed, a container exited): an outside change.
func (m *Module) onEnded(e Ended) {
	m.mu.Lock()
	in, ok := m.instances[e.Instance]
	if !ok || in.State.Ended() {
		m.mu.Unlock()
		return
	}
	if in.State == StateStopping { // hostd is stopping it; handleStop reports
		m.mu.Unlock()
		return
	}
	code := e.ExitCode
	status := code
	if in.closing {
		// Asked to close (instance.closing): whatever status an app ends
		// with then (a terminal's shell reports 1), it ended as asked.
		status = 0
	}
	in.State, _ = next(in.State, changeEnded, status)
	in.ExitCode = &code
	now := m.opts.Clock.Now().UTC()
	in.Ended = &now
	if in.State == StateFailed {
		in.Error = e.Reason
	}
	m.retire(e.Instance)
	snapshot := *in
	m.mu.Unlock()
	typ := EventExited
	if snapshot.State == StateFailed {
		typ = EventFailed
	}
	m.emitInstance(typ, "", snapshot)
}

// retire moves an ended instance from the live set to the recent list,
// freeing its ID. Caller holds m.mu.
func (m *Module) retire(id string) {
	in := m.instances[id]
	delete(m.instances, id)
	m.ended = append([]Instance{*in}, m.ended...)
	if len(m.ended) > keepEnded {
		m.ended = m.ended[:keepEnded]
	}
}

// emitInstance publishes an instance event. Linked to the action that
// caused it, or, for changes nobody asked for, marked as observed from the
// runner, which bumps the instance's version.
func (m *Module) emitInstance(typ, action string, in Instance) {
	ev := sdk.Event{Type: typ, Action: action, Resource: "instance:" + in.ID, Data: sdk.MustJSON(in)}
	if action == "" {
		ev.Source = &sdk.Source{Kind: sdk.SourceExternal, Name: in.Runner}
	}
	m.core.Emit(ev)
}

// backend returns the backend for a runner type. Flatpak apps and web
// pages are commands too: without a backend of their own, the exec
// backend runs them.
func (m *Module) backend(runner string) Backend {
	if b, ok := m.opts.Backends[runner]; ok {
		return b
	}
	if runner == RunnerFlatpak || runner == RunnerURL {
		return m.opts.Backends[RunnerExec]
	}
	return nil
}

// adopt picks up instances that were already running when hostd started.
func (m *Module) adopt(ctx context.Context) {
	names := make([]string, 0, len(m.opts.Backends))
	for n := range m.opts.Backends {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		// A backend that does not answer (no user bus, a stuck container
		// engine) must not hold up hostd's start.
		actx, cancel := context.WithTimeout(ctx, 10*time.Second)
		found, err := m.opts.Backends[name].Adopt(actx)
		cancel()
		if err != nil {
			m.log.Warn("finding running instances", "runner", name, "err", err)
			continue
		}
		for _, in := range found {
			in.Runner = name
			if app, ok := m.catalog().Get(in.App); ok {
				in.Name, in.Surface = app.Name, app.Surface
				in.Fullscreen = app.Surface == SurfaceWindow && app.Window.Fullscreen
				in.Match = app.matchRules()
				in.ShownBy = app.Window.ShownBy
				in.Volume = app.Audio.Volume
			}
			in.Started = m.opts.Clock.Now().UTC()
			m.mu.Lock()
			if in.State.Ended() {
				m.ended = append([]Instance{in}, m.ended...)
			} else {
				m.instances[in.ID] = &in
			}
			m.mu.Unlock()
			m.log.Info("found instance", "instance", in.ID, "state", in.State)
		}
	}
}

// watchBackend follows a backend's ended instances, reconnecting if the stream
// breaks. Ends that happen while the stream is down are not lost: once
// watching again, the backend is asked what still runs (reconcile).
func (m *Module) watchBackend(ctx context.Context, name string, b Backend) {
	backoff := time.Second
	reconnect := false
	for {
		var check clock.Timer
		if reconnect {
			// Give the new stream a moment to be in place, then compare: an
			// end after this shows up as an event, one before it here.
			check = m.opts.Clock.AfterFunc(time.Second, func() { m.reconcile(ctx, name, b) })
		}
		err := b.Watch(ctx, m.onEnded)
		if check != nil {
			check.Stop()
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			m.log.Warn("watching instances", "runner", name, "err", err)
		}
		reconnect = true
		select {
		case <-ctx.Done():
			return
		case <-m.opts.Clock.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// reconcile ends the live instances of a backend that it no longer finds
// running: they ended while hostd's view of the backend was cut off.
func (m *Module) reconcile(ctx context.Context, name string, b Backend) {
	if ctx.Err() != nil {
		return
	}
	actx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	found, err := b.Adopt(actx)
	if err != nil {
		m.log.Warn("checking instances after reconnecting", "runner", name, "err", err)
		return
	}
	seen := map[string]Instance{}
	for _, in := range found {
		seen[in.ID] = in
	}
	var ended []Ended
	m.mu.Lock()
	for id, in := range m.instances {
		if m.backend(in.Runner) != b || (in.State != StateRunning && in.State != StateStopping) {
			continue // another backend's, or still starting
		}
		cur, ok := seen[id]
		switch {
		case !ok:
			ended = append(ended, Ended{Instance: id, ExitCode: 0, Reason: "ended while hostd could not watch it"})
		case cur.State.Ended():
			code := 0
			if cur.ExitCode != nil {
				code = *cur.ExitCode
			}
			ended = append(ended, Ended{Instance: id, ExitCode: code, Reason: fmt.Sprintf("exit status %d (while hostd could not watch it)", code)})
		}
	}
	m.mu.Unlock()
	for _, e := range ended {
		m.log.Info("instance ended unseen", "instance", e.Instance, "runner", name)
		m.onEnded(e)
	}
}

func (m *Module) readInstances(params map[string]string) []Instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Instance, 0, len(m.instances))
	for _, in := range m.instances {
		out = append(out, *in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if params["all"] == "true" || params["all"] == "1" {
		out = append(out, m.ended...)
	}
	return out
}

func (m *Module) readInstance(id string) (Instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if in, ok := m.instances[id]; ok {
		return *in, nil
	}
	for _, e := range m.ended {
		if e.ID == id {
			return e, nil
		}
	}
	return Instance{}, sdk.Errorf(sdk.CodeNotFound, "no instance %q", id)
}
