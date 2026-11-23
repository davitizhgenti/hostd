// Package dbusx is hostd's way to the message buses: the user's session
// bus (systemd --user, notifications, media players) and the system bus
// (logind). A Bus is one connection, made on first use and made again
// after it drops, shared by everything that talks over it.
//
// An address is never left to the D-Bus library to find: without one it
// runs dbus-launch, which starts a stray bus with nothing on it, and hostd
// would wait on that bus for a timeout.
package dbusx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/godbus/dbus/v5"
)

// ErrNoBus is a bus that is not there to connect to.
var ErrNoBus = errors.New("no message bus")

// A Bus is a shared connection to one message bus.
type Bus struct {
	// Address finds the bus.
	Address func() (string, error)

	mu   sync.Mutex
	conn *dbus.Conn
}

// Session is the user's bus: DBUS_SESSION_BUS_ADDRESS, else the socket in
// runtimeDir ($XDG_RUNTIME_DIR/bus).
func Session(runtimeDir string) *Bus {
	return &Bus{Address: func() (string, error) { return SessionAddress(runtimeDir) }}
}

// System is the machine's bus: DBUS_SYSTEM_BUS_ADDRESS, else the standard
// socket.
func System() *Bus {
	return &Bus{Address: func() (string, error) {
		if a := os.Getenv("DBUS_SYSTEM_BUS_ADDRESS"); a != "" {
			return a, nil
		}
		return socket("/run/dbus/system_bus_socket")
	}}
}

// SessionAddress is the user bus's address (see Session).
func SessionAddress(runtimeDir string) (string, error) {
	if a := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); a != "" {
		return a, nil
	}
	return socket(filepath.Join(runtimeDir, "bus"))
}

func socket(path string) (string, error) {
	if fi, err := os.Stat(path); err != nil || fi.Mode()&os.ModeSocket == 0 {
		return "", fmt.Errorf("%w at %s", ErrNoBus, path)
	}
	return "unix:path=" + path, nil
}

// Conn is the bus's connection, made now if there is none or it dropped.
// The caller must not close it (Close does).
func (b *Bus) Conn(ctx context.Context) (*dbus.Conn, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn != nil && b.conn.Connected() {
		return b.conn, nil
	}
	addr, err := b.Address()
	if err != nil {
		return nil, err
	}
	conn, err := dbus.Connect(addr, dbus.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoBus, err)
	}
	b.conn = conn
	return conn, nil
}

// Drop forgets the connection after a call on it failed in a way that
// means the bus went away; the next Conn makes a new one.
func (b *Bus) Drop(conn *dbus.Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn == conn && conn != nil {
		_ = conn.Close()
		b.conn = nil
	}
}

// Close closes the connection, if there is one.
func (b *Bus) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn == nil {
		return nil
	}
	err := b.conn.Close()
	b.conn = nil
	return err
}

// Property reads one property of an object.
func Property[T any](ctx context.Context, conn *dbus.Conn, dest string, path dbus.ObjectPath, iface, name string) (T, error) {
	var out T
	var v dbus.Variant
	err := conn.Object(dest, path).CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, iface, name).Store(&v)
	if err != nil {
		return out, err
	}
	err = dbus.Store([]any{v.Value()}, &out)
	return out, err
}
