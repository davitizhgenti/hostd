package systemd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

func TestSubStateChange(t *testing.T) {
	sig := func(path, sub string) *dbus.Signal {
		changed := map[string]dbus.Variant{"ActiveState": dbus.MakeVariant("active")}
		if sub != "" {
			changed["SubState"] = dbus.MakeVariant(sub)
		}
		return &dbus.Signal{Path: dbus.ObjectPath(path), Name: "org.freedesktop.DBus.Properties.PropertiesChanged",
			Body: []any{"org.freedesktop.systemd1.Unit", changed, []string{}}}
	}
	for _, c := range []struct {
		sig      *dbus.Signal
		name     string
		sub      string
		reported bool
	}{
		{sig("/org/freedesktop/systemd1/unit/hostd_2dsteam_2eservice", "exited"), "hostd-steam.service", "exited", true},
		{sig("/org/freedesktop/systemd1/unit/hostd_2dsteam_5cx232_2eservice", "failed"), `hostd-steam\x232.service`, "failed", true},
		// Not hostd's: never reported, never looked up.
		{sig("/org/freedesktop/systemd1/unit/app_2dflatpak_2dcom_2evalvesoftware_2eSteam_2d1_2escope", "running"), "", "", false},
		{sig("/org/freedesktop/systemd1/unit/sys_2emount", "mounted"), "", "", false},
		// No sub-state in the change.
		{sig("/org/freedesktop/systemd1/unit/hostd_2dsteam_2eservice", ""), "", "", false},
		{&dbus.Signal{Path: "/org/freedesktop/systemd1/unit/hostd_2dx_2eservice"}, "", "", false},
	} {
		name, sub, ok := subStateChange(c.sig)
		if ok != c.reported || name != c.name || sub != c.sub {
			t.Errorf("%s: got %q %q %v, want %q %q %v", c.sig.Path, name, sub, ok, c.name, c.sub, c.reported)
		}
	}
}

func TestNeverAutolaunchesABus(t *testing.T) {
	// With no bus address and no user bus, connecting fails at once: it
	// must not fall back to dbus-launch and a stray bus.
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	t.Setenv("PATH", t.TempDir()) // and if it tried, there is no dbus-launch to run
	s := &UserSystemd{RuntimeDir: t.TempDir()}
	start := time.Now()
	_, err := s.List(context.Background(), "hostd-*.service")
	if err == nil || !strings.Contains(err.Error(), "no user bus") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("took %v to give up", time.Since(start))
	}
}
