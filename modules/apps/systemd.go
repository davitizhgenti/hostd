package apps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	sdbus "github.com/coreos/go-systemd/v22/dbus"
	"github.com/godbus/dbus/v5"
)

// UserSystemd is the systemd user manager over D-Bus. It connects on first
// use, so hostd starts even if the bus is briefly unavailable.
type UserSystemd struct {
	// RuntimeDir is $XDG_RUNTIME_DIR; the user bus is RuntimeDir/bus
	// unless DBUS_SESSION_BUS_ADDRESS says otherwise.
	RuntimeDir string

	mu   sync.Mutex
	conn *sdbus.Conn
}

// address is the user bus. It is never left to the D-Bus library to find:
// without an address that library runs dbus-launch, which starts a stray
// bus with no systemd on it, and hostd would wait on it for a timeout.
func (s *UserSystemd) address() (string, error) {
	if a := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); a != "" {
		return a, nil
	}
	path := filepath.Join(s.RuntimeDir, "bus")
	if fi, err := os.Stat(path); err != nil || fi.Mode()&os.ModeSocket == 0 {
		return "", fmt.Errorf("%w: no user bus at %s", errNoSystemd, path)
	}
	return "unix:path=" + path, nil
}

func (s *UserSystemd) connect(ctx context.Context) (*sdbus.Conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil && s.conn.Connected() {
		return s.conn, nil
	}
	addr, err := s.address()
	if err != nil {
		return nil, err
	}
	conn, err := sdbus.NewConnection(func() (*dbus.Conn, error) { return dbus.Connect(addr, dbus.WithContext(ctx)) })
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
	if spec.Restart != "" {
		props = append(props, sdbus.Property{Name: "Restart", Value: dbus.MakeVariant(spec.Restart)},
			sdbus.Property{Name: "RestartUSec", Value: dbus.MakeVariant(uint64(2 * time.Second / time.Microsecond))})
	}
	if spec.Service {
		props = append(props, sdbus.Property{Name: "NoNewPrivileges", Value: dbus.MakeVariant(true)},
			sdbus.Property{Name: "PrivateTmp", Value: dbus.MakeVariant(true)})
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
// sub-state change of every unit, with the new state in the signal. It
// never calls systemd back per signal (go-systemd's subscriber does, for
// every unit: reading a unit that was just removed loads it again, which
// is a new signal, a loop that kept systemd busy while Flatpak and Steam
// ran). Only hostd's units, hostd-*, are reported.
func (s *UserSystemd) Watch(ctx context.Context, fn func(name, subState string)) error {
	addr, err := s.address()
	if err != nil {
		return err
	}
	conn, err := dbus.Connect(addr, dbus.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("%w: %w", errNoSystemd, err)
	}
	defer conn.Close()
	if err := conn.AddMatchSignalContext(ctx, dbus.WithMatchInterface("org.freedesktop.DBus.Properties"),
		dbus.WithMatchMember("PropertiesChanged"), dbus.WithMatchPathNamespace(unitPathPrefix),
		dbus.WithMatchArg(0, "org.freedesktop.systemd1.Unit")); err != nil {
		return err
	}
	signals := make(chan *dbus.Signal, 256)
	conn.Signal(signals)
	// Without a subscriber, systemd sends no unit signals.
	if err := conn.Object("org.freedesktop.systemd1", "/org/freedesktop/systemd1").
		CallWithContext(ctx, "org.freedesktop.systemd1.Manager.Subscribe", 0).Err; err != nil {
		return err
	}
	// systemd repeats a state in several signals: report changes only.
	last := map[string]string{}
	for {
		select {
		case <-ctx.Done():
			return nil
		case sig, ok := <-signals:
			if !ok {
				return errors.New("the user bus connection closed")
			}
			name, sub, ok := subStateChange(sig)
			if !ok || last[name] == sub {
				continue
			}
			if sub == "dead" {
				delete(last, name) // gone, or about to be
			} else {
				last[name] = sub
			}
			fn(name, sub)
		}
	}
}

const unitPathPrefix = "/org/freedesktop/systemd1/unit"

// subStateChange reads a hostd unit's new sub-state from a
// PropertiesChanged signal of the systemd1.Unit interface.
func subStateChange(sig *dbus.Signal) (name, sub string, ok bool) {
	esc, found := strings.CutPrefix(string(sig.Path), unitPathPrefix+"/")
	if !found || !strings.HasPrefix(esc, "hostd_2d") || len(sig.Body) < 2 {
		return "", "", false
	}
	changed, _ := sig.Body[1].(map[string]dbus.Variant)
	v, found := changed["SubState"]
	if !found {
		return "", "", false
	}
	sub, ok = v.Value().(string)
	return unescapeBusPath(esc), sub, ok
}

// unescapeBusPath undoes systemd's object path escaping: "_2d" is '-'.
func unescapeBusPath(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '_' && i+2 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(n))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
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

// RestartUnit asks systemd to restart a unit and returns at once, without
// waiting for the job: used by hostd to restart itself after an update,
// when waiting would mean waiting for its own death.
func (s *UserSystemd) RestartUnit(ctx context.Context, name string) error {
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	_, err = conn.RestartUnitContext(ctx, name, "replace", nil)
	return err
}
