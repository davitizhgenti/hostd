package display

import (
	"bufio"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// instanceOf finds which hostd instance a process belongs to, from its
// cgroup: apps run in hostd-<instance>.service units, and every process
// they start (helpers, child windows) stays in that unit's cgroup. Returns
// "" for windows hostd did not start.
func instanceOf(procRoot string, pid int) string {
	if pid <= 0 {
		return ""
	}
	f, err := os.Open(filepath.Join(procRoot, strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		// cgroup v2: "0::/user.slice/.../app.slice/hostd-foot.service"
		_, path, ok := strings.Cut(sc.Text(), "::")
		if !ok {
			continue
		}
		segs := strings.Split(path, "/")
		for i := len(segs) - 1; i >= 0; i-- {
			if id, ok := instanceFromUnit(segs[i]); ok {
				return id
			}
		}
	}
	return ""
}

// instanceFromUnit reads an instance ID from a unit name such as
// hostd-firefox\x232.service ("firefox#2"). hostd.service itself is not an
// instance.
func instanceFromUnit(unit string) (string, bool) {
	name, ok := strings.CutSuffix(unit, ".service")
	if !ok {
		return "", false
	}
	id, ok := strings.CutPrefix(name, "hostd-")
	if !ok || id == "" {
		return "", false
	}
	return strings.ReplaceAll(id, `\x23`, "#"), true
}

// matchRules are an app's rules for windows outside its instance's unit
// (see apps.Match): globs on class, app_id and title, and a KEY=value the
// window's process has in its environment. Any rule that is set and
// matches is enough.
type matchRules struct {
	Class string `json:"class"`
	AppID string `json:"app_id"`
	Title string `json:"title"`
	Env   string `json:"env"`
}

type liveInstance struct {
	ID    string      `json:"id"`
	State string      `json:"state"`
	Match *matchRules `json:"match"`
}

func globMatch(pattern, s string) bool {
	if pattern == "" || s == "" {
		return false
	}
	ok, err := path.Match(pattern, s)
	return err == nil && ok
}

func (r *matchRules) matches(procRoot string, w Window) bool {
	if r == nil {
		return false
	}
	return globMatch(r.Class, w.Class) || globMatch(r.AppID, w.AppID) || globMatch(r.Title, w.Title) ||
		(r.Env != "" && hasEnv(procRoot, w.PID, r.Env))
}

// hasEnv reports whether a process has kv in its environment.
func hasEnv(procRoot string, pid int, kv string) bool {
	if pid <= 0 {
		return false
	}
	env, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "environ"))
	if err != nil {
		return false
	}
	for _, v := range strings.Split(string(env), "\x00") {
		if v == kv {
			return true
		}
	}
	return false
}

// resolveWindow finds a window's instance: by cgroup first, then by the
// running instances' match rules. A cgroup hit whose instance follows its
// app by an environment variable (a handoff, such as a Steam game) must
// match its rules too: the unit may host the launcher (Steam itself),
// whose own windows are not the game's.
func resolveWindow(procRoot string, w Window, live []liveInstance) string {
	byID := map[string]liveInstance{}
	for _, in := range live {
		byID[in.ID] = in
	}
	if id := instanceOf(procRoot, w.PID); id != "" {
		in, ok := byID[id]
		if !ok || in.Match == nil || in.Match.Env == "" || in.Match.matches(procRoot, w) {
			return id
		}
	}
	for _, in := range live {
		if in.Match.matches(procRoot, w) {
			return in.ID
		}
	}
	return ""
}
