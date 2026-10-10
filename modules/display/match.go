package display

import (
	"path"

	"github.com/davitizhgenti/hostd/contract"
)

// instanceOf finds which hostd instance a process belongs to, from its
// cgroup: apps run in hostd-<instance>.service units, and every process
// they start (helpers, child windows) stays in that unit's cgroup. Returns
// "" for windows hostd did not start.
func instanceOf(procRoot string, pid int) string { return contract.InstanceOfPID(procRoot, pid) }

func globMatch(pattern, s string) bool {
	if pattern == "" || s == "" {
		return false
	}
	ok, err := path.Match(pattern, s)
	return err == nil && ok
}

// matches applies an app's match rules (contract.Match) to a window:
// globs on class, app_id and title, and a KEY=value its process has in
// its environment. Any rule that is set and matches is enough.
func matches(r *contract.Match, procRoot string, w Window) bool {
	return score(r, procRoot, w) > 0
}

// score counts the rules that are set and match the window.
func score(r *contract.Match, procRoot string, w Window) int {
	if r == nil {
		return 0
	}
	n := 0
	for _, ok := range []bool{globMatch(r.Class, w.Class), globMatch(r.AppID, w.AppID), globMatch(r.Title, w.Title),
		r.Env != "" && hasEnv(procRoot, w.PID, r.Env)} {
		if ok {
			n++
		}
	}
	return n
}

// hasEnv reports whether a process has kv in its environment.
func hasEnv(procRoot string, pid int, kv string) bool {
	return contract.ProcessHasEnv(procRoot, pid, kv)
}

// resolveWindow finds a window's instance: by cgroup first, then by the
// running instances' match rules. A cgroup hit whose instance follows its
// app by an environment variable (a handoff, such as a Steam game) must
// match its rules too: the unit may host the launcher (Steam itself),
// whose own windows are not the game's. When several instances' rules
// match, the one matching most rules wins: a Steam game's window has
// Steam's FLATPAK_ID too, but also the game's SteamAppId and class.
func resolveWindow(procRoot string, w Window, live []contract.Instance) string {
	byID := map[string]contract.Instance{}
	for _, in := range live {
		byID[in.ID] = in
	}
	if id := instanceOf(procRoot, w.PID); id != "" {
		in, ok := byID[id]
		if !ok || in.Match == nil || in.Match.Env == "" || matches(in.Match, procRoot, w) {
			return id
		}
	}
	best, top := "", 0
	for _, in := range live {
		if n := score(in.Match, procRoot, w); n > top {
			best, top = in.ID, n
		}
	}
	return best
}
