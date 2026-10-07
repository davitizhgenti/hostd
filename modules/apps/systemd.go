package apps

import (
	"context"
	"errors"
	"fmt"
	"sync"

	sdbus "github.com/coreos/go-systemd/v22/dbus"
	"github.com/godbus/dbus/v5"
)

// UserSystemd is the systemd user manager over D-Bus. It connects on first
// use, so hostd starts even if the bus is briefly unavailable.
type UserSystemd struct {
	mu   sync.Mutex
	conn *sdbus.Conn
}

func (s *UserSystemd) connect(ctx context.Context) (*sdbus.Conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil && s.conn.Connected() {
		return s.conn, nil
	}
	conn, err := sdbus.NewUserConnectionContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errNoSystemd, err)
	}
	s.conn = conn
	return conn, nil
}

// Close closes the connection.
func (s *UserSystemd) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		s.conn.Close()
		s.conn = nil
	}
}

func waitJob(ctx context.Context, ch <-chan string, what string) error {
	select {
	case res := <-ch:
		if res != "done" {
			return fmt.Errorf("%s: systemd job %s", what, res)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *UserSystemd) StartTransient(ctx context.Context, name string, spec UnitSpec) error {
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	props := []sdbus.Property{
		sdbus.PropDescription(spec.Description),
		sdbus.PropExecStart(spec.Argv, true), // a non-zero exit fails the unit
		sdbus.PropType("exec"),               // started = the binary was executed
		sdbus.PropRemainAfterExit(true),      // keep the unit, and its exit status, after the app ends
		{Name: "Environment", Value: dbus.MakeVariant(spec.Env)},
		{Name: "KillMode", Value: dbus.MakeVariant("mixed")},
	}
	if spec.Dir != "" {
		props = append(props, sdbus.Property{Name: "WorkingDirectory", Value: dbus.MakeVariant(spec.Dir)})
	}
	ch := make(chan string, 1)
	if _, err := conn.StartTransientUnitContext(ctx, name, "fail", props, ch); err != nil {
		return err
	}
	return waitJob(ctx, ch, "start")
}

func (s *UserSystemd) Stop(ctx context.Context, name string) error {
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	ch := make(chan string, 1)
	if _, err := conn.StopUnitContext(ctx, name, "replace", ch); err != nil {
		if isNoSuchUnit(err) {
			return nil
		}
		return err
	}
	return waitJob(ctx, ch, "stop")
}

func (s *UserSystemd) ResetFailed(ctx context.Context, name string) error {
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	if err := conn.ResetFailedUnitContext(ctx, name); err != nil && !isNoSuchUnit(err) {
		return err
	}
	return nil
}

func (s *UserSystemd) Unit(ctx context.Context, name string) (UnitInfo, bool, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return UnitInfo{}, false, err
	}
	props, err := conn.GetUnitPropertiesContext(ctx, name)
	if err != nil {
		if isNoSuchUnit(err) {
			return UnitInfo{}, false, nil
		}
		return UnitInfo{}, false, err
	}
	if props["LoadState"] == "not-found" {
		return UnitInfo{}, false, nil
	}
	info := UnitInfo{
		Name:        name,
		Description: str(props["Description"]),
		ActiveState: str(props["ActiveState"]),
		SubState:    str(props["SubState"]),
	}
	if svc, err := conn.GetUnitTypePropertiesContext(ctx, name, "Service"); err == nil {
		info.MainPID = num(svc["MainPID"])
		info.ExitStatus = num(svc["ExecMainStatus"])
		info.ExitCode = num(svc["ExecMainCode"])
		info.Result = str(svc["Result"])
	}
	return info, true, nil
}

func (s *UserSystemd) List(ctx context.Context, pattern string) ([]UnitInfo, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	units, err := conn.ListUnitsByPatternsContext(ctx, nil, []string{pattern})
	if err != nil {
		return nil, err
	}
	out := make([]UnitInfo, 0, len(units))
	for _, u := range units {
		info, ok, err := s.Unit(ctx, u.Name)
		if err != nil || !ok {
			continue
		}
		out = append(out, info)
	}
	return out, nil
}

// Watch uses D-Bus signals, not polling: systemd tells us about every
// sub-state change of every unit.
func (s *UserSystemd) Watch(ctx context.Context, fn func(name, subState string)) error {
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	if err := conn.Subscribe(); err != nil {
		return err
	}
	updates := make(chan *sdbus.SubStateUpdate, 256)
	errs := make(chan error, 16)
	conn.SetSubStateSubscriber(updates, errs)
	for {
		select {
		case <-ctx.Done():
			conn.SetSubStateSubscriber(nil, nil)
			return nil
		case u := <-updates:
			fn(u.UnitName, u.SubState)
		case <-errs:
			// a property read for one unit failed; keep watching
		}
	}
}

func isNoSuchUnit(err error) bool {
	var name string
	var e dbus.Error
	var pe *dbus.Error
	switch {
	case errors.As(err, &e):
		name = e.Name
	case errors.As(err, &pe):
		name = pe.Name
	}
	return name == "org.freedesktop.systemd1.NoSuchUnit" || name == "org.freedesktop.systemd1.LoadFailed"
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) int {
	switch n := v.(type) {
	case int32:
		return int(n)
	case uint32:
		return int(n)
	case int64:
		return int(n)
	case uint64:
		return int(n)
	}
	return 0
}
