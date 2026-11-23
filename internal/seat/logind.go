package seat

import (
	"context"
	"os/user"
	"strconv"

	"github.com/godbus/dbus/v5"

	"github.com/davitizhgenti/hostd/internal/dbusx"
)

const (
	login1       = "org.freedesktop.login1"
	login1Path   = dbus.ObjectPath("/org/freedesktop/login1")
	seat0Path    = dbus.ObjectPath("/org/freedesktop/login1/seat/seat0")
	ifaceUser    = "org.freedesktop.login1.User"
	ifaceSeat    = "org.freedesktop.login1.Seat"
	ifaceSession = "org.freedesktop.login1.Session"
)

// Logind asks logind over the system bus, on one connection it keeps.
type Logind struct {
	Bus *dbusx.Bus
}

// sessionRef is how logind points at a session: its ID and its object.
type sessionRef struct {
	ID   string
	Path dbus.ObjectPath
}

// Active reports whether user's primary session (its Display) is the
// active one of its seat. When logind cannot say (no session, no logind, a
// remote session that has no seat) the answer is yes: nothing then takes
// the seat away.
func (l *Logind) Active(ctx context.Context, name string) bool {
	u, err := user.Lookup(name)
	if err != nil {
		return true
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return true
	}
	conn, err := l.Bus.Conn(ctx)
	if err != nil {
		return true
	}
	var userPath dbus.ObjectPath
	if err := conn.Object(login1, login1Path).CallWithContext(ctx, "org.freedesktop.login1.Manager.GetUser", 0, uint32(uid)).Store(&userPath); err != nil {
		l.dropIfGone(conn)
		return true
	}
	display, err := dbusx.Property[sessionRef](ctx, conn, login1, userPath, ifaceUser, "Display")
	if err != nil || display.ID == "" {
		return true
	}
	active, err := dbusx.Property[bool](ctx, conn, login1, display.Path, ifaceSession, "Active")
	return err != nil || active
}

// Holder is the user whose session is active on seat0, "" if none is.
func (l *Logind) Holder(ctx context.Context) string {
	conn, err := l.Bus.Conn(ctx)
	if err != nil {
		return ""
	}
	s, err := dbusx.Property[sessionRef](ctx, conn, login1, seat0Path, ifaceSeat, "ActiveSession")
	if err != nil {
		l.dropIfGone(conn)
		return ""
	}
	if s.ID == "" {
		return ""
	}
	name, _ := dbusx.Property[string](ctx, conn, login1, s.Path, ifaceSession, "Name")
	return name
}

// dropIfGone forgets a connection whose bus went away, so the next call
// connects again.
func (l *Logind) dropIfGone(conn *dbus.Conn) {
	if !conn.Connected() {
		l.Bus.Drop(conn)
	}
}

// system is the logind hostd asks by default.
var system = &Logind{Bus: dbusx.System()}

// Active asks the machine's logind (see Logind.Active).
func Active(ctx context.Context, user string) bool { return system.Active(ctx, user) }

// Holder asks the machine's logind (see Logind.Holder).
func Holder(ctx context.Context) string { return system.Holder(ctx) }
