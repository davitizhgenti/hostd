// Window tracking: which windows exist and whose they are, where a new
// one goes, the focus history, and going back when an app closes.
package display

import (
	"context"
	"encoding/json"
	"time"

	"github.com/davitizhgenti/hostd/sdk"
)

type trackedWindow struct {
	Window
	Instance string `json:"instance,omitempty"`
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
		m.windowOpened(ctx, w)
	case "close":
		m.windowClosed(w)
	case "focus":
		m.windowFocused(w)
	case "fullscreen_mode", "title":
		m.mu.Lock()
		if t, ok := m.windows[w.ID]; ok {
			t.Fullscreen, t.Title = w.Fullscreen, w.Title
		}
		m.mu.Unlock()
	}
}

// windowOpened tracks a new window and, if it is an instance's, places it:
// its own workspace, fullscreen unless the app says otherwise, and in
// front unless someone else is using the screen (then announced).
func (m *Module) windowOpened(ctx context.Context, w Window) {
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
}

// windowClosed forgets a window. When the focused app's last window
// closes, the screen goes back to the app before it, once the app has
// ended.
func (m *Module) windowClosed(w Window) {
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
}

// windowFocused records the focus: the focus history, and launches left
// behind no longer come forward.
func (m *Module) windowFocused(w Window) {
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
