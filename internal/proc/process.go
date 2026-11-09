// Package proc finds which hostd instance a process belongs to: the one place
// that knows how, so the display module (for windows) and the audio module
// (for sound streams) cannot disagree. Everything is read from /proc.
package proc

import (
	"bufio"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/davitizhgenti/hostd/contract"
)

// InstanceOfPID finds the instance a process belongs to from its cgroup:
// apps run in hostd-<instance>.service units, and every process they start
// stays in that unit's cgroup. "" for processes hostd did not start (and
// for Flatpak apps, which move to a scope of their own: see Match.Env).
func InstanceOfPID(procRoot string, pid int) string {
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
			if id, ok := contract.InstanceFromUnit(segs[i]); ok {
				return id
			}
		}
	}
	return ""
}

// HasEnv reports whether a process has kv (KEY=value) in its
// environment. Only the user's own processes are readable.
func HasEnv(procRoot string, pid int, kv string) bool {
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

// Descends reports whether pid is ancestor or one of its
// descendants, following parent PIDs in /proc.
func Descends(procRoot string, pid, ancestor int) bool {
	for range 64 {
		if pid <= 1 || ancestor <= 0 {
			return false
		}
		if pid == ancestor {
			return true
		}
		stat, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
		if err != nil {
			return false
		}
		// "pid (comm) state ppid ...": comm may hold spaces and parentheses.
		i := strings.LastIndexByte(string(stat), ')')
		if i < 0 {
			return false
		}
		f := strings.Fields(string(stat)[i+1:])
		if len(f) < 2 {
			return false
		}
		if pid, err = strconv.Atoi(f[1]); err != nil {
			return false
		}
	}
	return false
}

// Subject is what is known of the thing to place: its process, and, for a
// window, the attributes the match rules look at.
type Subject struct {
	PID                 int
	Class, AppID, Title string
}

// GlobMatch reports whether s matches a glob; an empty pattern or string
// never does.
func GlobMatch(pattern, s string) bool {
	if pattern == "" || s == "" {
		return false
	}
	ok, err := path.Match(pattern, s)
	return err == nil && ok
}

// Score counts the match rules (contract.Match) that are set and match the
// subject: globs on class, app_id and title, and a KEY=value its process has
// in its environment.
func Score(r *contract.Match, procRoot string, s Subject) int {
	if r == nil {
		return 0
	}
	n := 0
	for _, ok := range []bool{GlobMatch(r.Class, s.Class), GlobMatch(r.AppID, s.AppID), GlobMatch(r.Title, s.Title),
		r.Env != "" && HasEnv(procRoot, s.PID, r.Env)} {
		if ok {
			n++
		}
	}
	return n
}

// rulesSet counts the rules a Match has.
func rulesSet(r *contract.Match) int {
	n := 0
	for _, v := range []string{r.Class, r.AppID, r.Title, r.Env} {
		if v != "" {
			n++
		}
	}
	return n
}

// Resolve finds the instance among live that a subject belongs to: by cgroup
// first, then by the instances' match rules. A cgroup hit whose instance
// follows its app by an environment variable (a handoff, such as a Steam
// game) must match its rules too: the unit may host the launcher (Steam
// itself), whose own windows and sound are not the game's. When several
// instances' rules match, the one matching most rules wins: a Steam game's
// window has Steam's FLATPAK_ID too, but also the game's SteamAppId and
// class. Last, an app that moved itself out of its unit (Chromium into a
// scope of its own) is found by descent from its main process, which comes
// after the rules because a game's process descends from Steam's too.
// "" means none.
func Resolve(procRoot string, s Subject, live []contract.Instance) string {
	if s.PID <= 0 {
		return ""
	}
	byID := map[string]contract.Instance{}
	for _, in := range live {
		byID[in.ID] = in
	}
	if id := InstanceOfPID(procRoot, s.PID); id != "" {
		in, ok := byID[id]
		if !ok || in.Match == nil || in.Match.Env == "" || Score(in.Match, procRoot, s) > 0 {
			return id
		}
	}
	best, top, topSet := "", 0, 0
	for _, in := range live {
		n := Score(in.Match, procRoot, s)
		if n == 0 {
			continue
		}
		// Most rules matched wins; of equals, the one with more rules set is
		// the more specific (a sound stream has no class to tell a game
		// from Steam, but the game's rules say more).
		if set := rulesSet(in.Match); n > top || (n == top && set > topSet) {
			best, top, topSet = in.ID, n, set
		}
	}
	if best != "" {
		return best
	}
	for _, in := range live {
		if Descends(procRoot, s.PID, in.PID) {
			return in.ID
		}
	}
	return ""
}
