// Package notify shows short notices on the screen.
package notify

import (
	"context"

	"github.com/godbus/dbus/v5"

	"github.com/davitizhgenti/hostd/internal/dbusx"
)

// Notifier shows a short notice on the screen.
type Notifier interface {
	Notify(ctx context.Context, summary, body string) error
}

// Desktop sends notices to the session's notification daemon
// (hostd-notices, drawn above fullscreen windows) over org.freedesktop.Notifications.
type Desktop struct {
	Bus *dbusx.Bus // the session bus, shared
}

func (n *Desktop) Notify(ctx context.Context, summary, body string) error {
	conn, err := n.Bus.Conn(ctx)
	if err != nil {
		return err
	}
	obj := conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")
	return obj.CallWithContext(ctx, "org.freedesktop.Notifications.Notify", 0,
		"hostd", uint32(0), "", summary, body, []string{}, map[string]dbus.Variant{}, int32(5000)).Err
}
