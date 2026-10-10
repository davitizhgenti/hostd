// Health checks for background apps: an app with one counts as running
// once it passes (a deploy switches to a release only then), as failed if
// it does not within its start time, and is reported unhealthy when it
// keeps failing later.
package apps

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Health events.
const (
	EventUnhealthy = "instance.unhealthy" // failed 3 checks in a row
	EventHealthy   = "instance.healthy"   // passes again after being unhealthy
)

const unhealthyAfter = 3

// checkHealth runs one check: an HTTP GET that answers 2xx, or a TCP
// connection that is accepted.
func checkHealth(ctx context.Context, h Health) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if h.HTTP != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.HTTP, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return fmt.Errorf("%s answered %s", h.HTTP, resp.Status)
		}
	}
	if h.TCP != "" {
		var d net.Dialer
		c, err := d.DialContext(ctx, "tcp", h.TCP)
		if err != nil {
			return err
		}
		_ = c.Close()
	}
	return nil
}

// watchHealth holds an instance's start until its health check passes,
// then keeps checking it until it ends.
func (m *Module) watchHealth(ctx context.Context, id string, app *App) {
	start, every := app.Health.durations()
	deadline := m.opts.Clock.Now().Add(start)
	tick := m.opts.Clock.NewTicker(time.Second)
	defer tick.Stop()
	var last error
	for {
		if !m.alive(id) {
			return
		}
		if last = checkHealth(ctx, app.Health); last == nil {
			m.healthy(id)
			break
		}
		if m.opts.Clock.Now().After(deadline) {
			m.unhealthyStart(ctx, id, app, last, start)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C():
		}
	}
	tick.Reset(every)
	fails := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C():
		}
		if !m.alive(id) {
			return
		}
		err := checkHealth(ctx, app.Health)
		switch {
		case err == nil && fails >= unhealthyAfter:
			fails = 0
			m.emitHealth(EventHealthy, id, nil)
		case err == nil:
			fails = 0
		default:
			fails++
			if fails == unhealthyAfter {
				m.emitHealth(EventUnhealthy, id, err)
			}
		}
	}
}

func (m *Module) alive(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	in, ok := m.instances[id]
	return ok && !in.State.Ended()
}

// healthy marks a starting instance as running.
func (m *Module) healthy(id string) {
	m.mu.Lock()
	cur, ok := m.instances[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	s, err := next(cur.State, changeStarted, 0)
	changed := err == nil && s != cur.State
	cur.State = s
	snapshot := *cur
	m.mu.Unlock()
	if changed {
		m.emitInstance(EventStarted, "", snapshot)
	}
}

// unhealthyStart stops an instance whose check never passed, and records
// it as failed.
func (m *Module) unhealthyStart(ctx context.Context, id string, app *App, last error, within time.Duration) {
	m.mu.Lock()
	cur, ok := m.instances[id]
	var inst Instance
	if ok {
		inst = *cur
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	if b := m.backend(app.Runner.Type); b != nil {
		if err := b.Stop(ctx, inst); err != nil && !errors.Is(err, context.Canceled) {
			m.log.Warn("stopping an app that never became healthy", "instance", id, "err", err)
		}
	}
	m.mu.Lock()
	cur, ok = m.instances[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	cur.State = StateFailed
	cur.Error = fmt.Sprintf("its health check did not pass within %s: %v", within, last)
	now := m.opts.Clock.Now().UTC()
	cur.Ended = &now
	snapshot := *cur
	m.retire(id)
	m.mu.Unlock()
	m.emitInstance(EventFailed, "", snapshot)
}

func (m *Module) emitHealth(typ, id string, err error) {
	data := map[string]string{"id": id}
	if err != nil {
		data["error"] = err.Error()
	}
	m.mu.Lock()
	in, ok := m.instances[id]
	var snapshot Instance
	if ok {
		snapshot = *in
	}
	m.mu.Unlock()
	if ok {
		snapshot.Error = data["error"]
		m.emitInstance(typ, "", snapshot)
	}
}
