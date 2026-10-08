package apps

import (
	"testing"

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
