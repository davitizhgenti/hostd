package notify

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"
)

// A notification daemon of the test's own (as mako is), on a bus of its own.
type daemon struct {
	mu   sync.Mutex
	got  []string
	conn *dbus.Conn
}

func (d *daemon) Notify(app string, replaces uint32, icon, summary, body string, actions []string, hints map[string]dbus.Variant, timeout int32) (uint32, *dbus.Error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.got = append(d.got, app+": "+summary+" / "+body)
	return uint32(len(d.got)), nil
}

func privateBus(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("no dbus-daemon")
	}
	cmd := exec.Command("dbus-daemon", "--session", "--nofork", "--print-address=1")
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Skipf("dbus-daemon: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	buf := make([]byte, 512)
	n, _ := out.Read(buf)
	return strings.TrimSpace(string(buf[:n]))
}

func TestDesktop(t *testing.T) {
	addr := privateBus(t)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	d := &daemon{conn: conn}
	_ = conn.Export(d, "/org/freedesktop/Notifications", "org.freedesktop.Notifications")
	if _, err := conn.RequestName("org.freedesktop.Notifications", dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}

	n := &Desktop{RuntimeDir: t.TempDir()}
	defer n.Close()
	for _, body := range []string{"is ready", "again, over the same connection"} {
		if err := n.Notify(context.Background(), "Jellyfin", body); err != nil {
			t.Fatal(err)
		}
	}
	d.mu.Lock()
	got := d.got
	d.mu.Unlock()
	if len(got) != 2 || got[0] != "hostd: Jellyfin / is ready" {
		t.Fatalf("notices %q", got)
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
	if err := n.Close(); err != nil {
		t.Fatal("closing twice")
	}
}

// Without a session bus: an error, and no bus started for it.
func TestDesktopWithoutABus(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	n := &Desktop{RuntimeDir: t.TempDir()}
	if err := n.Notify(context.Background(), "x", "y"); err == nil || !strings.Contains(err.Error(), "no user bus") {
		t.Fatalf("%v", err)
	}
}
