package unitstest

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// FakeSession makes a runtime dir with a Wayland socket and a Sway socket.
func FakeSession(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "rt-") // short: socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	for _, name := range []string{"wayland-1", "sway-ipc.1000.42.sock"} {
		ln, err := new(net.ListenConfig).Listen(context.Background(), "unix", filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
	}
	if err := os.WriteFile(filepath.Join(dir, "wayland-1.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}
