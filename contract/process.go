package contract

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
			if id, ok := InstanceFromUnit(segs[i]); ok {
				return id
			}
		}
	}
	return ""
}

// ProcessHasEnv reports whether a process has kv (KEY=value) in its
// environment. Only the user's own processes are readable.
func ProcessHasEnv(procRoot string, pid int, kv string) bool {
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

// ProcessDescends reports whether pid is ancestor or one of its
// descendants, following parent PIDs in /proc.
func ProcessDescends(procRoot string, pid, ancestor int) bool {
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
