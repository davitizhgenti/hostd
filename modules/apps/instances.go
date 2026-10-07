package apps

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/davitizhgenti/hostd/sdk"
)

// Instance events.
const (
	EventStarting = "instance.starting"
	EventStarted  = "instance.started"
	EventExited   = "instance.exited"
	EventFailed   = "instance.failed"
)

// keepEnded is how many ended instances stay visible (hostctl ps --all).
const keepEnded = 50

var (
	startSchema = json.RawMessage(`{"type":"object","properties":{
		"id":{"type":"string","description":"app ID or alias"}},"required":["id"]}`)
	instanceSchema = json.RawMessage(`{"type":"object","properties":{
		"id":{"type":"string","description":"instance ID, e.g. firefox or firefox#2"}},"required":["id"]}`)
)

func instanceActions() []sdk.ActionSpec {
	return []sdk.ActionSpec{
		{Type: "app.start", Description: "Start an app (if it already runs: focus, a new copy, or restart, per its settings)",
			Schema: startSchema, Keys: []sdk.KeyTemplate{"app:{id}"}, Scope: "apps",
			Timeout: sdk.Duration(5 * time.Minute), // pulling a container image can take a while
			Route:   &sdk.Route{Method: "POST", Path: "/v1/apps/{id}/start"}},
		{Type: "instance.stop", Description: "Stop a running instance",
			Schema: instanceSchema, Keys: []sdk.KeyTemplate{"instance:{id}"}, Scope: "apps",
			Timeout: sdk.Duration(time.Minute),
			Route:   &sdk.Route{Method: "POST", Path: "/v1/instances/{id}/stop"}},
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
		{Name: "instances", Description: "Running instances (?all=true adds recently ended ones)", Path: "/v1/instances"},
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
	var args struct{ ID string }
	if err := a.DecodeArgs(&args); err != nil {
		return err
	}
	app, ok := m.catalog().Get(args.ID)
	if !ok {
		return sdk.Errorf(sdk.CodeNotFound, "no app %q (see hostctl apps --all)", args.ID)
	}
	if _, ok := m.opts.Backends[app.Runner.Type]; !ok {
		return sdk.Errorf(sdk.CodeModuleUnavailable, "app %q uses the %s runner, which hostd cannot run yet", app.ID, app.Runner.Type)
	}
	return nil
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
	var args struct{ ID string }
	if err := a.DecodeArgs(&args); err != nil {
		return sdk.Result{}, err
	}
	app, ok := m.catalog().Get(args.ID)
	if !ok {
		return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "no app %q", args.ID)
	}
	backend := m.opts.Backends[app.Runner.Type]

	m.mu.Lock()
	running := m.live(app.ID)
	m.mu.Unlock()
	if app.Instance.Policy == "single" && len(running) > 0 {
		switch app.Instance.IfRunning {
		case "focus":
			// Bring it forward. Without the display module (headless, or
			// before it exists) there is nothing to focus.
			id := running[0].ID
			if running[0].Surface == SurfaceWindow && m.core.Handles("window.focus") {
				if _, err := m.core.Do(ctx, sdk.Action{Type: "window.focus", Args: mustJSON(map[string]string{"instance": id})}); err != nil {
					return sdk.Result{}, err
				}
			}
			return sdk.Result{Data: mustJSON(startResult{Instance: id, App: app.ID, State: running[0].State, AlreadyRunning: true})}, nil
		case "restart":
			for _, in := range running {
				if _, err := m.core.Do(ctx, sdk.Action{Type: "instance.stop", Args: mustJSON(map[string]string{"id": in.ID})}); err != nil &&
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
		Fullscreen: app.Surface == SurfaceWindow && app.Window.Fullscreen,
		State:      StateStarting, Started: m.opts.Clock.Now().UTC()}
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
		return sdk.Result{Data: mustJSON(startResult{Instance: id, App: app.ID, State: state})}, nil
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
	return sdk.Result{Data: mustJSON(startResult{Instance: id, App: app.ID, State: snapshot.State})}, nil
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
	backend := m.opts.Backends[in.Runner]
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
		return sdk.Result{Data: mustJSON(map[string]string{"instance": args.ID, "state": string(StateExited)})}, nil
	}
	in.State, _ = next(in.State, changeEnded, 0)
	now := m.opts.Clock.Now().UTC()
	in.Ended = &now
	m.retire(args.ID)
	snapshot = *in
	m.mu.Unlock()
	m.emitInstance(EventExited, a.ID, snapshot)
	return sdk.Result{Data: mustJSON(map[string]string{"instance": args.ID, "state": string(snapshot.State)})}, nil
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
	in.State, _ = next(in.State, changeEnded, e.ExitCode)
	code := e.ExitCode
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
	ev := sdk.Event{Type: typ, Action: action, Resource: "instance:" + in.ID, Data: mustJSON(in)}
	if action == "" {
		ev.Source = &sdk.Source{Kind: sdk.SourceExternal, Name: in.Runner}
	}
	m.core.Emit(ev)
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
// breaks.
func (m *Module) watchBackend(ctx context.Context, name string, b Backend) {
	backoff := time.Second
	for {
		err := b.Watch(ctx, m.onEnded)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			m.log.Warn("watching instances", "runner", name, "err", err)
		}
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
