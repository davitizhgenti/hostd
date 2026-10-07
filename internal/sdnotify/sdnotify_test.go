package sdnotify

import (
	"net"
	"path/filepath"
	"testing"
)

func TestReadyWithoutSystemd(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	if err := Ready(); err != nil {
		t.Fatal(err)
	}
}

func TestReadyAndStopping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	t.Setenv("NOTIFY_SOCKET", path)
	for _, want := range []string{"READY=1", "STOPPING=1"} {
		send := Ready
		if want == "STOPPING=1" {
			send = Stopping
		}
		if err := send(); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 64)
		n, _, err := conn.ReadFromUnix(buf)
		if err != nil || string(buf[:n]) != want {
			t.Fatalf("got %q, %v; want %q", buf[:n], err, want)
		}
	}
}

func TestBadSocket(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", filepath.Join(t.TempDir(), "nobody-listens"))
	if err := Ready(); err == nil {
		t.Fatal("no error for a missing socket")
	}
}
