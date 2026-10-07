// Package sdnotify tells systemd about the service's state (sd_notify), so
// a Type=notify unit counts as started only once hostd is really serving.
package sdnotify

import (
	"net"
	"os"
)

// Ready sends READY=1. Outside systemd (no NOTIFY_SOCKET) it does nothing.
func Ready() error { return send("READY=1") }

// Stopping sends STOPPING=1.
func Stopping() error { return send("STOPPING=1") }

func send(state string) error {
	path := os.Getenv("NOTIFY_SOCKET")
	if path == "" {
		return nil
	}
	if path[0] == '@' { // abstract socket
		path = "\x00" + path[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte(state))
	return err
}
