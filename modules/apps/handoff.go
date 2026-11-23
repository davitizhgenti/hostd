package apps

import (
	"context"
	"time"
)

// A HandoffFinder is a backend that finds a handoff app's processes when
// hostd did not start it, and follows them as an instance's.
type HandoffFinder interface {
	HandoffsRunning(kvs []string) map[string]bool
	FollowHandoff(instance, kv string)
}

// findStartedElsewhere makes instances of handoff apps that run though
// hostd did not start them: a game started from Steam's own menu has
// its app file's variable (SteamAppId=620). It is then an instance like
// any other: in the menu, on a workspace of its own, closed from hostd,
// and ended when its processes are.
func (m *Module) findStartedElsewhere(ctx context.Context, f HandoffFinder) {
	t := m.opts.Clock.NewTicker(findEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C():
		}
		m.adoptStarted(f)
	}
}

func (m *Module) adoptStarted(f HandoffFinder) {
	apps := m.catalog().List(true)
	now := m.opts.Clock.Now()
	var want []*App
	var kvs []string
	m.mu.Lock()
	for _, a := range apps {
		if a.Runner.Handoff == "" || now.Sub(m.endedAt[a.ID]) < findAfterEnd || m.backend(a.Runner.Type) != m.opts.Backends[RunnerExec] {
			continue
		}
		if !m.runs(a.ID) {
			want = append(want, a)
			kvs = append(kvs, a.Runner.Handoff)
		}
	}
	m.mu.Unlock()
	if len(want) == 0 {
		return
	}
	found := f.HandoffsRunning(kvs)
	for _, a := range want {
		if !found[a.Runner.Handoff] {
			continue
		}
		m.mu.Lock()
		if m.runs(a.ID) {
			m.mu.Unlock()
			continue // started meanwhile
		}
		id := nextID(a.ID, func(id string) bool { _, ok := m.instances[id]; return ok })
		// Front: someone at this machine started it, from the app in front.
		inst := &Instance{ID: id, App: a.ID, Name: a.Name, Runner: a.Runner.Type, Surface: a.Surface,
			Fullscreen: a.Surface == SurfaceWindow && a.Window.Fullscreen, Front: true, Match: matchRules(a),
			ShownBy: a.Window.ShownBy, Volume: a.Audio.Volume, State: StateRunning, Started: now.UTC()}
		m.instances[id] = inst
		snapshot := *inst
		m.mu.Unlock()
		f.FollowHandoff(id, a.Runner.Handoff)
		m.log.Info("found an instance hostd did not start", "instance", id, "by", a.Runner.Handoff)
		m.emitInstance(EventStarted, "", snapshot)
	}
}

// runs reports whether an app has a live instance. Caller holds m.mu.
func (m *Module) runs(app string) bool {
	for _, in := range m.instances {
		if in.App == app {
			return true
		}
	}
	return false
}

// checkEarlyEnd looks again, shortly after a start, for a command that
// ended at once (echo): while systemd announces its unit, the unit's
// state lags and the signal that it ended can be folded into the
// announcement, so neither the start nor the watcher is sure to have
// seen it.
func (m *Module) checkEarlyEnd(ctx context.Context, name string, b Backend) {
	for _, d := range []time.Duration{250 * time.Millisecond, 2 * time.Second} {
		select {
		case <-ctx.Done():
			return
		case <-m.opts.Clock.After(d):
		}
		m.reconcile(ctx, name, b)
	}
}
