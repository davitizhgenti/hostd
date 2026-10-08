package display

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/godbus/dbus/v5"
)

// Notifier shows a short notice on the screen.
type Notifier interface {
	Notify(ctx context.Context, summary, body string) error
}

// DesktopNotifier sends notices to the session's notification daemon
// (mako, drawn above fullscreen windows) over org.freedesktop.Notifications.
type DesktopNotifier struct {
	RuntimeDir string // where the user bus is: <RuntimeDir>/bus

	mu   sync.Mutex
	conn *dbus.Conn
}

func (n *DesktopNotifier) connect(ctx context.Context) (*dbus.Conn, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.conn != nil && n.conn.Connected() {
		return n.conn, nil
	}
	// Never let the library autolaunch a bus (dbus-launch): only the
	// session's own one will do.
	addr := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if addr == "" {
		path := filepath.Join(n.RuntimeDir, "bus")
		if fi, err := os.Stat(path); err != nil || fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("no user bus at %s", path)
		}
		addr = "unix:path=" + path
	}
	conn, err := dbus.Connect(addr, dbus.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	n.conn = conn
	return conn, nil
}

func (n *DesktopNotifier) Notify(ctx context.Context, summary, body string) error {
	conn, err := n.connect(ctx)
	if err != nil {
		return err
	}
	obj := conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")
	return obj.CallWithContext(ctx, "org.freedesktop.Notifications.Notify", 0,
		"hostd", uint32(0), "", summary, body, []string{}, map[string]dbus.Variant{}, int32(5000)).Err
}

// Close drops the bus connection.
func (n *DesktopNotifier) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.conn == nil {
		return nil
	}
	err := n.conn.Close()
	n.conn = nil
	return err
}
