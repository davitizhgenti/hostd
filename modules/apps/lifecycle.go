package apps

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/davitizhgenti/hostd/internal/clock"
	"github.com/davitizhgenti/hostd/sdk"
)

// handleClosing marks an instance as asked to close: its end will count as
// a normal exit.
func (m *Module) handleClosing(_ context.Context, _ sdk.Action, args instanceArgs) (sdk.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if in, ok := m.instances[args.ID]; ok {
		in.Closing = true
	}
	return sdk.Result{Data: sdk.MustJSON(map[string]string{"instance": args.ID})}, nil
}

func (m *Module) handleStop(ctx context.Context, a sdk.Action, args instanceArgs) (sdk.Result, error) {
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
	if in.Closing {
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
	m.endedAt[in.App] = m.opts.Clock.Now()
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
	if runner == RunnerFlatpak || runner == RunnerURL || runner == RunnerProcess {
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
				in.Match = matchRules(app)
				in.ShownBy = app.Window.ShownBy
				in.Volume = app.Audio.Volume
			}
			if in.Started.IsZero() { // the runner does not know: from now on
				in.Started = m.opts.Clock.Now().UTC()
			}
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
