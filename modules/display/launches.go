// What the display module learns from the apps module (see contract):
// each instance's window preferences and launch placement, its end, and
// the notices and presence that decide whether a launch takes the screen.
package display

import (
	"context"
	"encoding/json"
	"time"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/sdk"
)

// launch is how a started instance's windows are placed.
type launch struct {
	name  string
	front bool // its windows come to the front
	// noticed: a background window was announced with a notice already.
	noticed bool
}

// followInstances learns each instance's fullscreen preference from the
// apps module's events.
func (m *Module) followInstances(ctx context.Context, events <-chan sdk.Event) {
	for ev := range events {
		if ev.Type == sdk.EventLagged {
			m.resyncInstances(ctx) // events were missed: read the state instead
			continue
		}
		var in contract.Instance
		if json.Unmarshal(ev.Data, &in) != nil || in.ID == "" {
			continue
		}
		switch ev.Type {
		case contract.EventInstanceStarting, contract.EventInstanceStarted:
			m.mu.Lock()
			delete(m.gone, in.ID) // the ID is in use again
			m.mu.Unlock()
			if ev.Type == contract.EventInstanceStarting || !m.launchKnown(in.ID) {
				m.learnLaunch(ctx, in.ID, in.Name, ev.Source, in.Front)
			}
			if ev.Type == contract.EventInstanceStarted {
				m.claimWindows(ctx, in.ID)
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
		case contract.EventInstanceExited, contract.EventInstanceFailed:
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

// claimWindows gives a started instance the open windows that turn out
// to be its own: a handoff app whose program ran before the instance
// (Steam, still running, started again from the menu). They are placed
// as if they had just opened.
func (m *Module) claimWindows(ctx context.Context, instance string) {
	m.mu.Lock()
	var free []Window
	for _, t := range m.windows {
		if t.Instance == "" {
			free = append(free, t.Window)
		}
	}
	m.mu.Unlock()
	if len(free) == 0 {
		return
	}
	resolve := m.resolver(ctx)
	for _, w := range free {
		if resolve(w) == instance {
			m.windowOpened(ctx, w)
		}
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
		l.front = l.front || old.front // asked for in front while starting
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
		core.Emit(sdk.Event{Type: EventNotice, Data: sdk.MustJSON(map[string]string{"instance": instance, "text": text})})
	}
	if m.opts.Notifier != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := m.opts.Notifier.Notify(ctx, text, ""); err != nil {
			m.log.Debug("showing notice", "text", text, "err", err)
		}
	}
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
		core.Emit(sdk.Event{Type: typ, Data: sdk.MustJSON(map[string]bool{"present": present}),
			Source: &sdk.Source{Kind: sdk.SourceExternal, Name: "input"}})
	}
}
