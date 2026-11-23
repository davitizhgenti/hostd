package seat

import (
	"context"
	"os/user"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/internal/dbusx"
	"github.com/davitizhgenti/hostd/internal/dbusx/dbusxtest"
	"github.com/davitizhgenti/hostd/internal/evdev"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

type fakeInput struct {
	mu      sync.Mutex
	running int32
	starts  int
	grabs   []string
}

func (f *fakeInput) Watch(ctx context.Context, fn func(evdev.InputEvent)) error {
	f.mu.Lock()
	f.starts++
	f.mu.Unlock()
	atomic.StoreInt32(&f.running, 1)
	defer atomic.StoreInt32(&f.running, 0)
	<-ctx.Done()
	return nil
}
func (f *fakeInput) Grab(d string, on bool) error {
	f.grabs = append(f.grabs, d)
	return nil
}
func (f *fakeInput) Devices() map[string]int { return map[string]int{"pad": 1} }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", what)
}

func TestGateReadsOnlyWhileTheSeatIsHeld(t *testing.T) {
	in := &fakeInput{}
	var active atomic.Bool
	active.Store(true)
	g := &Gate{Inner: in, Every: 10 * time.Millisecond, Active: func(context.Context) bool { return active.Load() }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = g.Watch(ctx, func(evdev.InputEvent) {}) }()
	running := func() bool { return atomic.LoadInt32(&in.running) == 1 }
	waitFor(t, "reading while held", running)
	active.Store(false)
	waitFor(t, "stops when the seat goes", func() bool { return !running() })
	active.Store(true)
	waitFor(t, "reads again when it is back", running)
	if in.starts != 2 {
		t.Errorf("starts = %d", in.starts)
	}
	cancel()
	<-done
	if running() {
		t.Error("still reading after the context ended")
	}
	if err := g.Grab("pad", true); err != nil || len(in.grabs) != 1 {
		t.Errorf("grab: %v %v", err, in.grabs)
	}
	if g.Devices()["pad"] != 1 {
		t.Error("devices do not pass through")
	}
	if (&Gate{Inner: nilInput{}}).Devices() != nil || (&Gate{Inner: nilInput{}}).Grab("", true) != nil {
		t.Error("an Input without them")
	}
}

type nilInput struct{}

func (nilInput) Watch(context.Context, func(evdev.InputEvent)) error { return nil }

func TestActiveAndHolderWithoutLogind(t *testing.T) {
	l := &Logind{Bus: &dbusx.Bus{Address: func() (string, error) { return "unix:path=/nonexistent/bus", nil }}}
	if !l.Active(context.Background(), "root") {
		t.Error("unknown must be active")
	}
	if l.Holder(context.Background()) != "" {
		t.Error("no holder without logind")
	}
	if !l.Active(context.Background(), "hostd-no-such-user") {
		t.Error("a user that does not exist must be active")
	}
}

// fakeLogind serves the few objects Logind reads: the manager, seat0, one
// user, and the sessions.
type fakeLogind struct{ userPath dbus.ObjectPath }

func (f *fakeLogind) GetUser(uid uint32) (dbus.ObjectPath, *dbus.Error) { return f.userPath, nil }

func serveLogind(t *testing.T, conn *dbus.Conn, uid string, display, active sessionRef, activeOn bool) {
	t.Helper()
	if _, err := conn.RequestName(login1, dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	userPath := dbus.ObjectPath("/org/freedesktop/login1/user/_" + uid)
	if err := conn.Export(&fakeLogind{userPath: userPath}, login1Path, "org.freedesktop.login1.Manager"); err != nil {
		t.Fatal(err)
	}
	export := func(path dbus.ObjectPath, iface string, props map[string]any) {
		p := prop.Map{iface: {}}
		for k, v := range props {
			p[iface][k] = &prop.Prop{Value: v}
		}
		if _, err := prop.Export(conn, path, p); err != nil {
			t.Fatal(err)
		}
	}
	export(userPath, ifaceUser, map[string]any{"Display": display})
	export(seat0Path, ifaceSeat, map[string]any{"ActiveSession": active})
	export("/org/freedesktop/login1/session/_31", ifaceSession, map[string]any{"Active": activeOn, "Name": "serve"})
	export("/org/freedesktop/login1/session/_32", ifaceSession, map[string]any{"Active": !activeOn, "Name": "guest"})
}

func TestLogind(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	ctx := context.Background()
	s1 := sessionRef{"1", "/org/freedesktop/login1/session/_31"}
	s2 := sessionRef{"2", "/org/freedesktop/login1/session/_32"}
	for name, c := range map[string]struct {
		display, active sessionRef
		on              bool
		wantActive      bool
		wantHolder      string
	}{
		"holds the seat":  {s1, s1, true, true, "serve"},
		"another holds":   {s1, s2, false, false, "guest"},
		"no display":      {sessionRef{"", "/"}, s1, true, true, "serve"},
		"nobody holds it": {s1, sessionRef{"", "/"}, false, false, ""},
	} {
		t.Run(name, func(t *testing.T) {
			addr := dbusxtest.Private(t)
			server, err := dbus.Connect(addr)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			serveLogind(t, server, me.Uid, c.display, c.active, c.on)
			l := &Logind{Bus: &dbusx.Bus{Address: func() (string, error) { return addr, nil }}}
			defer l.Bus.Close()
			if got := l.Active(ctx, me.Username); got != c.wantActive {
				t.Errorf("active %v", got)
			}
			if got := l.Holder(ctx); got != c.wantHolder {
				t.Errorf("holder %q", got)
			}
		})
	}
}
