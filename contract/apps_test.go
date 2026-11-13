package contract

import "testing"

func TestUnitNames(t *testing.T) {
	for _, id := range []string{"foot", "firefox#2", "steam-620", "a.b_c"} {
		got, ok := InstanceFromUnit(UnitName(id))
		if !ok || got != id {
			t.Errorf("%s -> %s -> %q %v", id, UnitName(id), got, ok)
		}
	}
	if UnitName("firefox#2") != `hostd-firefox\x232.service` {
		t.Fatalf("escaping: %s", UnitName("firefox#2"))
	}
	for _, unit := range []string{"hostd.service", "hostd-.service", "sway.service", "hostd-foot.scope", "app-steam-hostd.service",
		"hostd-shell.service", "hostd-notices.service", "hostd-rollback.service", "hostd-update.service",
		"hostd-steam-games.service", "hostd-seat-helper.service", "hostd-session-alice.service"} {
		if id, ok := InstanceFromUnit(unit); ok {
			t.Errorf("%s taken for instance %q", unit, id)
		}
	}
}
