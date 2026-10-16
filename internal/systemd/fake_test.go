package systemd

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// The real D-Bus code, against a fake systemd user manager on a bus of the
// test's own: the methods and signals of org.freedesktop.systemd1 that
// UserSystemd and go-systemd use.

const (
	managerPath  = "/org/freedesktop/systemd1"
	managerIface = "org.freedesktop.systemd1.Manager"
)

type fakeUnit struct {
	description, active, sub, result string
	mainPID, exitStatus, exitCode    int
	since                            uint64
	props                            map[string]dbus.Variant // what StartTransientUnit was given
}

type fakeManager struct {
	conn *dbus.Conn

	mu       sync.Mutex
	units    map[string]*fakeUnit
	jobs     uint32
	failNext bool // the next start's job fails
	restarts []string
}

// privateBus starts a session bus of the test's own and points
// DBUS_SESSION_BUS_ADDRESS at it.
func privateBus(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("no dbus-daemon")
	}
	cmd := exec.Command("dbus-daemon", "--session", "--nofork", "--print-address=1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("dbus-daemon: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	buf := make([]byte, 512)
	n, err := out.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	addr := strings.TrimSpace(string(buf[:n]))
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)
	return addr
}

func newFakeManager(t *testing.T) *fakeManager {
	t.Helper()
	addr := privateBus(t)
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	m := &fakeManager{conn: conn, units: map[string]*fakeUnit{}}
	if err := conn.Export(m, managerPath, managerIface); err != nil {
		t.Fatal(err)
	}
	if err := conn.ExportSubtree(&unitProps{m}, unitPathPrefix, "org.freedesktop.DBus.Properties"); err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName("org.freedesktop.systemd1", dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("name: %v %v", reply, err)
	}
	return m
}

func (m *fakeManager) job(unit, result string) dbus.ObjectPath {
	m.jobs++
	id := m.jobs
	path := dbus.ObjectPath(managerPath + "/job/" + strings.Repeat("1", int(id)))
	go func() {
		time.Sleep(5 * time.Millisecond)
		_ = m.conn.Emit(managerPath, managerIface+".JobRemoved", id, path, unit, result)
	}()
	return path
}

func (m *fakeManager) changed(unit, sub string) {
	path := dbus.ObjectPath(unitPathPrefix + "/" + busEscape(unit))
	_ = m.conn.Emit(path, "org.freedesktop.DBus.Properties.PropertiesChanged", "org.freedesktop.systemd1.Unit",
		map[string]dbus.Variant{"SubState": dbus.MakeVariant(sub)}, []string{})
}

type property struct {
	Name  string
	Value dbus.Variant
}

type auxUnit struct {
	Name  string
	Props []property
}

func (m *fakeManager) StartTransientUnit(name, mode string, props []property, aux []auxUnit) (dbus.ObjectPath, *dbus.Error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u := m.units[name]; u != nil && u.active == "active" {
		return "", dbus.NewError("org.freedesktop.systemd1.UnitExists", []any{"Unit " + name + " was already loaded"})
	}
	u := &fakeUnit{active: "active", sub: "running", mainPID: 4242, since: uint64(time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC).UnixMicro()), props: map[string]dbus.Variant{}}
	for _, p := range props {
		u.props[p.Name] = p.Value
		if p.Name == "Description" {
			u.description, _ = p.Value.Value().(string)
		}
	}
	result := "done"
	if m.failNext {
		m.failNext, result = false, "failed"
		u.active, u.sub, u.result, u.mainPID = "failed", "failed", "exit-code", 0
	}
	m.units[name] = u
	go m.changed(name, u.sub)
	return m.job(name, result), nil
}

func (m *fakeManager) StopUnit(name, mode string) (dbus.ObjectPath, *dbus.Error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.units[name]
	if u == nil {
		return "", dbus.NewError("org.freedesktop.systemd1.NoSuchUnit", []any{"Unit " + name + " not loaded."})
	}
	u.active, u.sub, u.mainPID = "inactive", "dead", 0
	go m.changed(name, "dead")
	return m.job(name, "done"), nil
}

func (m *fakeManager) ResetFailedUnit(name string) *dbus.Error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.units[name] == nil {
		return dbus.NewError("org.freedesktop.systemd1.NoSuchUnit", []any{"Unit " + name + " not loaded."})
	}
	delete(m.units, name)
	return nil
}

func (m *fakeManager) RestartUnit(name, mode string) (dbus.ObjectPath, *dbus.Error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.restarts = append(m.restarts, name)
	return m.job(name, "done"), nil
}

func (m *fakeManager) Subscribe() *dbus.Error { return nil }

type listedUnit struct {
	Name, Description, LoadState, ActiveState, SubState, Followed string
	Path                                                          dbus.ObjectPath
	JobID                                                         uint32
	JobType                                                       string
	JobPath                                                       dbus.ObjectPath
}

func (m *fakeManager) ListUnitsByPatterns(states, patterns []string) ([]listedUnit, *dbus.Error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []listedUnit
	for name, u := range m.units {
		for _, p := range patterns {
			if ok, _ := pathMatch(p, name); ok {
				out = append(out, listedUnit{Name: name, Description: u.description, LoadState: "loaded", ActiveState: u.active, SubState: u.sub,
					Path: dbus.ObjectPath(unitPathPrefix + "/" + busEscape(name)), JobPath: "/"})
			}
		}
	}
	return out, nil
}

// unitProps answers Properties.GetAll for every unit's object.
type unitProps struct{ m *fakeManager }

func (p *unitProps) GetAll(msg dbus.Message, iface string) (map[string]dbus.Variant, *dbus.Error) {
	path, _ := msg.Headers[dbus.FieldPath].Value().(dbus.ObjectPath)
	name := unescapeBusPath(strings.TrimPrefix(string(path), unitPathPrefix+"/"))
	p.m.mu.Lock()
	defer p.m.mu.Unlock()
	u := p.m.units[name]
	if u == nil {
		return map[string]dbus.Variant{"LoadState": dbus.MakeVariant("not-found"), "ActiveState": dbus.MakeVariant("inactive")}, nil
	}
	switch iface {
	case "org.freedesktop.systemd1.Unit":
		return map[string]dbus.Variant{"LoadState": dbus.MakeVariant("loaded"), "Description": dbus.MakeVariant(u.description),
			"ActiveState": dbus.MakeVariant(u.active), "SubState": dbus.MakeVariant(u.sub), "ActiveEnterTimestamp": dbus.MakeVariant(u.since)}, nil
	case "org.freedesktop.systemd1.Service":
		return map[string]dbus.Variant{"MainPID": dbus.MakeVariant(uint32(u.mainPID)), "ExecMainStatus": dbus.MakeVariant(int32(u.exitStatus)),
			"ExecMainCode": dbus.MakeVariant(int32(u.exitCode)), "Result": dbus.MakeVariant(u.result)}, nil
	}
	return map[string]dbus.Variant{}, nil
}

// busEscape is systemd's object path escaping, as go-systemd does it: a
// letter, or a digit not first, stays; any other byte is _ and its hex.
func busEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9' && i > 0) {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "_%02x", c)
		}
	}
	return b.String()
}

func pathMatch(pattern, name string) (bool, error) {
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(name, strings.TrimSuffix(pattern, "*")), nil
	}
	if i := strings.Index(pattern, "*"); i >= 0 {
		return strings.HasPrefix(name, pattern[:i]) && strings.HasSuffix(name, pattern[i+1:]), nil
	}
	return pattern == name, nil
}

func TestUserSystemdAgainstAManager(t *testing.T) {
	m := newFakeManager(t)
	s := &UserSystemd{RuntimeDir: t.TempDir()}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Watch reports hostd's units' state changes.
	changes := make(chan string, 16)
	watching := make(chan error, 1)
	go func() { watching <- s.Watch(ctx, func(name, sub string) { changes <- name + " " + sub }) }()
	time.Sleep(100 * time.Millisecond) // subscribed

	spec := UnitSpec{Description: "hostd instance term of app term", Argv: []string{"foot"}, Env: []string{"LANG=C.UTF-8"},
		Dir: "/home/screen", Restart: "on-failure", Service: true}
	if err := s.StartTransient(ctx, "hostd-term.service", spec); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	props := m.units["hostd-term.service"].props
	m.mu.Unlock()
	for _, p := range []string{"ExecStart", "WorkingDirectory", "Restart", "RestartUSec", "NoNewPrivileges", "PrivateTmp", "Environment", "KillMode"} {
		if _, ok := props[p]; !ok {
			t.Errorf("%s not given to systemd", p)
		}
	}
	info, ok, err := s.Unit(ctx, "hostd-term.service")
	if err != nil || !ok || info.MainPID != 4242 || info.SubState != "running" || info.Description != spec.Description ||
		!info.Since.Equal(time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("unit %+v %v %v", info, ok, err)
	}
	list, err := s.List(ctx, "hostd-*.service")
	if err != nil || len(list) != 1 || list[0].Name != "hostd-term.service" {
		t.Fatalf("list %+v %v", list, err)
	}
	if err := s.StartTransient(ctx, "hostd-term.service", spec); err == nil || !strings.Contains(err.Error(), "already loaded") {
		t.Fatalf("started twice: %v", err)
	}

	select {
	case c := <-changes:
		if c != "hostd-term.service running" {
			t.Fatalf("change %q", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no change reported")
	}
	if err := s.Stop(ctx, "hostd-term.service"); err != nil {
		t.Fatal(err)
	}
	if c := <-changes; c != "hostd-term.service dead" {
		t.Fatalf("change %q", c)
	}

	// Units that are not there: not errors.
	if err := s.Stop(ctx, "hostd-ghost.service"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := s.ResetFailed(ctx, "hostd-ghost.service"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, ok, err := s.Unit(ctx, "hostd-ghost.service"); ok || err != nil {
		t.Fatalf("ghost unit: %v %v", ok, err)
	}

	// A job that fails is an error that says so.
	m.mu.Lock()
	m.failNext = true
	m.mu.Unlock()
	if err := s.StartTransient(ctx, "hostd-missing.service", UnitSpec{Argv: []string{"missing"}}); err == nil || !strings.Contains(err.Error(), "systemd job failed") {
		t.Fatalf("failed job: %v", err)
	}
	if err := s.RestartUnit(ctx, "hostd.service"); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-watching; err != nil {
		t.Fatalf("watch ended with %v", err)
	}
}
