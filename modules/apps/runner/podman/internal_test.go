package podman

import (
	"strings"
	"testing"
)

func TestParsePort(t *testing.T) {
	for in, want := range map[string][3]string{
		"8080:80":           {"", "8080", "80/tcp"},
		"127.0.0.1:8080:80": {"127.0.0.1", "8080", "80/tcp"},
		"53:53/udp":         {"", "53", "53/udp"},
	} {
		ip, hp, cp, err := parsePort(in)
		if err != nil || [3]string{ip, hp, cp} != want {
			t.Errorf("parsePort(%q) = %q %q %q %v", in, ip, hp, cp, err)
		}
	}
	for _, bad := range []string{"80", "a:b", "1:2:3:4", "0:80", "8080:70000"} {
		if _, _, _, err := parsePort(bad); err == nil {
			t.Errorf("parsePort(%q) accepted", bad)
		}
	}
}

func TestProjectName(t *testing.T) {
	for _, id := range []string{"jellyfin", "jellyfin#2", "my_app.v2#10", "a-b", "x__y"} {
		p := projectName(id)
		if strings.ContainsAny(strings.TrimPrefix(p, "hostd-"), ".#") {
			t.Errorf("%s: project %s", id, p)
		}
		if back, ok := instanceOfProject(p); !ok || back != id {
			t.Errorf("%s → %s → %s %v", id, p, back, ok)
		}
	}
	for _, p := range []string{"other", "hostd-", "hostd-a_x", "hostd-a_"} {
		if _, ok := instanceOfProject(p); ok {
			t.Errorf("%s taken for one of hostd's", p)
		}
	}
}
