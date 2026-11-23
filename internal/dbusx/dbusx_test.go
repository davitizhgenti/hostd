package dbusx

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"

	"github.com/davitizhgenti/hostd/internal/dbusx/dbusxtest"
)

func TestSessionAddress(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	dir := t.TempDir()
	if _, err := SessionAddress(dir); !errors.Is(err, ErrNoBus) {
		t.Fatalf("no socket: %v", err)
	}
	l, err := net.Listen("unix", filepath.Join(dir, "bus"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if a, err := SessionAddress(dir); err != nil || a != "unix:path="+filepath.Join(dir, "bus") {
		t.Fatalf("%q %v", a, err)
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/elsewhere")
	if a, _ := SessionAddress(dir); a != "unix:path=/elsewhere" {
		t.Fatalf("the variable comes first: %q", a)
	}
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path=/sys-bus")
	if a, _ := System().Address(); a != "unix:path=/sys-bus" {
		t.Fatalf("system %q", a)
	}
}

func TestBusSharesOneConnection(t *testing.T) {
	addr := dbusxtest.Private(t)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)
	ctx := context.Background()
	b := Session(t.TempDir())
	defer b.Close()
	c1, err := b.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c2, _ := b.Conn(ctx); c2 != c1 {
		t.Fatal("a second connection")
	}
	features, err := Property[[]string](ctx, c1, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "Features")
	if err != nil {
		t.Fatalf("features %v %v", features, err)
	}
	if _, err := Property[int](ctx, c1, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "Features"); err == nil {
		t.Fatal("a list stored as a number")
	}
	b.Drop(c1)
	c3, err := b.Conn(ctx)
	if err != nil || c3 == c1 {
		t.Fatalf("after a drop: %v", err)
	}
	if err := b.Close(); err != nil || b.Close() != nil {
		t.Fatal(err)
	}
	if _, err := (&Bus{Address: func() (string, error) { return "unix:path=/nonexistent/bus", nil }}).Conn(ctx); !errors.Is(err, ErrNoBus) {
		t.Fatalf("unreachable: %v", err)
	}
}
