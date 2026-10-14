package display

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/davitizhgenti/hostd/internal/evdev"
	"github.com/davitizhgenti/hostd/sdk"
)

// followControllers announces controllers as they come and go. It watches
// /dev/input and /dev (raw HID devices appear there), and lists again a
// moment after the last change, since one controller brings several
// device nodes.
func (m *Module) followControllers(ctx context.Context) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		m.log.Warn("cannot follow controllers", "err", err)
		return
	}
	defer w.Close()
	for _, d := range []string{"dev/input", "dev"} {
		if err := w.Add(filepath.Join(m.opts.SysRoot, d)); err != nil {
			m.log.Debug("cannot watch for controllers", "dir", d, "err", err)
		}
	}
	known := map[string]evdev.Controller{}
	for _, c := range evdev.Scan(m.opts.SysRoot, m.opts.Profiles) {
		known[c.Device] = c
	}
	var settle <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			base := filepath.Base(ev.Name)
			if strings.HasPrefix(base, "event") || strings.HasPrefix(base, "hidraw") {
				settle = m.opts.Clock.After(300 * time.Millisecond)
			}
		case <-w.Errors:
		case <-settle:
			settle = nil
			now := map[string]evdev.Controller{}
			for _, c := range evdev.Scan(m.opts.SysRoot, m.opts.Profiles) {
				now[c.Device] = c
				if _, ok := known[c.Device]; !ok {
					m.emitController(EventControllerConnected, c)
				}
			}
			for dev, c := range known {
				if _, ok := now[dev]; !ok {
					m.emitController(EventControllerDisconnected, c)
				}
			}
			known = now
		}
	}
}

func (m *Module) emitController(typ string, c evdev.Controller) {
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	if core != nil {
		core.Emit(sdk.Event{Type: typ, Data: sdk.MustJSON(c), Source: &sdk.Source{Kind: sdk.SourceExternal, Name: "input"}})
	}
}
