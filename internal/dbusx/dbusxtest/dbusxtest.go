// Package dbusxtest starts a private message bus for a test.
package dbusxtest

import (
	"bufio"
	"context"
	"os/exec"
	"strings"
	"testing"
)

// Private starts a bus of its own for the test and returns its address;
// the test is skipped where there is no dbus-daemon.
func Private(t testing.TB) string {
	t.Helper()
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("no dbus-daemon")
	}
	cmd := exec.CommandContext(context.Background(), "dbus-daemon", "--session", "--nofork", "--print-address=1") // ends with the test (Cleanup)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("dbus-daemon: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	addr, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(addr)
}
