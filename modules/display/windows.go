// Window tracking: which windows exist and whose they are, where a new
// one goes, the focus history, and going back when an app closes.
package display

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/davitizhgenti/hostd/sdk"
)

type trackedWindow struct {
	Window
	Instance string `json:"instance,omitempty"`
	used     uint64 // focusSeq when it was last focused
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
	full, known := m.prefs[inst]
	ws := m.workspaceFor(inst, w, full || !known)
	tw := &trackedWindow{Window: w, Instance: inst}
	m.windows[w.ID] = tw
	front, announce := m.placement(inst)
	b := m.backend
	m.mu.Unlock()
	if inst != "" && b != nil {
		// Its own workspace, fullscreen unless the app says otherwise.
		// In front, unless someone else is using the screen and did
		// not ask for it: then it waits on its workspace, announced.
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
	if inst == "" && !w.Dialog && b != nil {
		// Not an instance's (a window Steam opened by itself): it would
		// open next to whatever is on screen, splitting it (the menu and
		// Steam side by side). It gets a workspace of its own. It comes
		// forward unless someone is using an app, which keeps the screen;
		// then a notice says it opened.
		ws := WorkspacePrefix + "window-" + strconv.FormatInt(w.ID, 10)
		m.screen("move", b.Move(ctx, w.ID, ws))
		m.mu.Lock()
		inUse := false
		for _, t := range m.windows {
			if t.Focused && t.ID != w.ID && t.Instance != "" && !m.isMenu(t.Instance) {
				inUse = true
			}
		}
		inUse = inUse && m.presence.present()
		m.mu.Unlock()
		if inUse {
			name := w.Title
			if name == "" {
				name = w.AppID + w.Class
			}
			m.notice("", name+" opened in the background")
		} else {
			m.screen("show", b.Show(ctx, ws))
			m.screen("focus", b.Focus(ctx, w.ID))
		}
		m.mu.Lock()
		if t, ok := m.windows[w.ID]; ok {
			t.Workspace = ws
		}
		m.mu.Unlock()
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
// workspaceFor picks a new window's workspace: its instance's. A
// fullscreen app's further top-level windows (a game Steam started) each
// get one of their own, so two never share the screen; dialogs stay with
// their app. Caller holds m.mu.
func (m *Module) workspaceFor(inst string, w Window, fullscreen bool) string {
	ws := WorkspacePrefix + inst
	if inst == "" || w.Dialog || !fullscreen {
		return ws
	}
	for id, t := range m.windows {
		if id != w.ID && t.Instance == inst && t.Workspace == ws && !t.Dialog {
			return ws + ":" + strconv.FormatInt(w.ID, 10)
		}
	}
	return ws
}

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
	// The focused window closed but its app has others (a game started
	// from Steam): the app's window used last comes forward, rather than
	// an empty workspace.
	var stay *trackedWindow
	if ok && tw.Focused && tw.Instance != "" && back == nil {
		for _, t := range m.windows {
			if t.Instance == tw.Instance && (stay == nil || t.used > stay.used) {
				c := *t
				stay = &c
			}
		}
	}
	if ok && tw.Focused && tw.Instance == "" && !tw.Dialog {
		// A window of no instance closed in front: back to the app used
		// before it, not an empty workspace.
		stay = m.previous()
	}
	b := m.backend
	seq := m.focusSeq
	menuGone := ok && tw.Focused && m.isMenu(tw.Instance)
	m.mu.Unlock()
	if menuGone {
		m.takeControllers(false)
	}
	if stay != nil && b != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		m.screen("show", b.Show(ctx, stay.Workspace))
		m.screen("focus", b.Focus(ctx, stay.ID))
		cancel()
	}
	if !ok {
		tw = &trackedWindow{Window: w}
	}
	m.emit(EventClosed, tw)
	if back != nil && b != nil {
		// The app closes first, then the screen goes back: wait for it
		// to end, so the previous app (the menu, say) never lists
		// it as running. Unless someone moved on in the meantime.
		closing := tw.Instance
		m.mu.Lock()
		life := m.life
		m.mu.Unlock()
		if life == nil {
			life = context.Background()
		}
		m.done.Add(1)
		go func() {
			defer m.done.Done()
			m.waitEnded(life, closing, m.opts.EndWait) // ends with the module too
			if life.Err() != nil {
				return
			}
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
	if ok {
		tw.used = m.focusSeq
	}
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
	m.takeControllers(ok && m.isMenu(snap.Instance))
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
