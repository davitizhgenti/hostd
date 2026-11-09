package proc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/davitizhgenti/hostd/contract"
)

func TestDescends(t *testing.T) {
	root := t.TempDir()
	for pid, stat := range map[string]string{
		"10": "10 (sh) S 1 10", "20": "20 (my (odd) name) S 10 20", "30": "30 (x) S 20 30", "40": "40 (y) S 1 40",
	} {
		_ = os.MkdirAll(filepath.Join(root, pid), 0o755)
		_ = os.WriteFile(filepath.Join(root, pid, "stat"), []byte(stat), 0o644)
	}
	for _, c := range []struct {
		pid, ancestor int
		want          bool
	}{{30, 10, true}, {30, 30, true}, {20, 10, true}, {40, 10, false}, {10, 30, false}, {30, 0, false}} {
		if got := Descends(root, c.pid, c.ancestor); got != c.want {
			t.Errorf("Descends(%d, %d) = %v", c.pid, c.ancestor, got)
		}
	}
}

// fake writes a process into a /proc: its cgroup, environment and parent.
func fake(t *testing.T, root string, pid int, cgroup string, env []string, ppid int) {
	t.Helper()
	dir := filepath.Join(root, fmt.Sprint(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if cgroup != "" {
		write("cgroup", "0::"+cgroup+"\n")
	}
	if env != nil {
		write("environ", strings.Join(env, "\x00")+"\x00")
	}
	write("stat", fmt.Sprintf("%d (p) S %d %d", pid, ppid, pid))
}

func unit(id string) string { return "/user.slice/app.slice/hostd-" + id + ".service" }

func TestResolve(t *testing.T) {
	root := t.TempDir()
	steam := contract.Instance{ID: "steam", App: "steam", PID: 500, Match: &contract.Match{Env: "FLATPAK_ID=com.valvesoftware.Steam"}}
	game := contract.Instance{ID: "portal2", App: "portal2", PID: 501,
		Match: &contract.Match{Class: "portal2*", Env: "SteamAppId=620"}}
	foot := contract.Instance{ID: "foot", App: "foot", PID: 400}
	chrome := contract.Instance{ID: "chrome", App: "chrome", PID: 600}
	live := []contract.Instance{steam, game, foot, chrome}

	steamEnv := []string{"FLATPAK_ID=com.valvesoftware.Steam"}
	gameEnv := []string{"FLATPAK_ID=com.valvesoftware.Steam", "SteamAppId=620"}
	fake(t, root, 10, unit("foot"), nil, 1)                        // in its unit
	fake(t, root, 11, unit("foot#2"), nil, 1)                      // an instance that is not live
	fake(t, root, 20, unit("steam"), steamEnv, 1)                  // Steam's own window, in Steam's unit
	fake(t, root, 21, unit("steam"), []string{"HOME=/x"}, 20)      // in Steam's unit, but not Steam's (no marker)
	fake(t, root, 22, "/user.slice/app-flatpak.scope", gameEnv, 1) // the game in a scope of its own
	fake(t, root, 23, "/user.slice/app-flatpak.scope", steamEnv, 1)
	fake(t, root, 30, "/user.slice/app-chromium.scope", nil, 600) // moved out of its unit
	fake(t, root, 31, "/user.slice/x.scope", nil, 30)             // and a child of it
	fake(t, root, 40, "/user.slice/x.scope", nil, 1)              // nobody's

	for _, c := range []struct {
		name string
		s    Subject
		want string
	}{
		{"by its unit", Subject{PID: 10}, "foot"},
		{"a unit whose instance is not live is still named", Subject{PID: 11}, "foot#2"},
		{"Steam's own window: the unit, and its rule holds", Subject{PID: 20, Class: "steam"}, "steam"},
		{"in Steam's unit without Steam's marker: the unit does not claim it", Subject{PID: 21, Class: "other"}, ""},
		{"...unless its class says whose it is", Subject{PID: 21, Class: "portal2_linux"}, "portal2"},
		{"the game's sound, with no class: more specific rules win a tie", Subject{PID: 22}, "portal2"},
		{"Steam's sound in a scope", Subject{PID: 23}, "steam"},
		{"moved out of its unit: found by descent", Subject{PID: 30}, "chrome"},
		{"and its child", Subject{PID: 31}, "chrome"},
		{"nobody's", Subject{PID: 40}, ""},
		{"no process", Subject{PID: 0}, ""},
		{"a process that is gone", Subject{PID: 999}, ""},
	} {
		if got := Resolve(root, c.s, live); got != c.want {
			t.Errorf("%s: Resolve = %q, want %q", c.name, got, c.want)
		}
	}
	if got := Resolve(root, Subject{PID: 10}, nil); got != "foot" {
		t.Errorf("with nothing live the unit still names the instance: %q", got)
	}
}

func TestGlobMatch(t *testing.T) {
	for _, c := range []struct {
		pattern, s string
		want       bool
	}{{"steam*", "steam_app_620", true}, {"steam*", "foot", false}, {"", "x", false}, {"x", "", false}, {"[", "x", false}} {
		if got := GlobMatch(c.pattern, c.s); got != c.want {
			t.Errorf("GlobMatch(%q, %q) = %v", c.pattern, c.s, got)
		}
	}
}
