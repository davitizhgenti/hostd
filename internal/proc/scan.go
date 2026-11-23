package proc

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Root is /proc, unless a test points it elsewhere.
func Root(procRoot string) string {
	if procRoot == "" {
		return "/proc"
	}
	return procRoot
}

// Each calls fn with every process's PID until fn returns false.
func Each(procRoot string, fn func(pid int) bool) {
	entries, err := os.ReadDir(Root(procRoot))
	if err != nil {
		return
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if !fn(pid) {
			return
		}
	}
}

// environ is a process's environment, nil if it cannot be read (only the
// user's own processes can).
func environ(procRoot string, pid int) [][]byte {
	env, err := os.ReadFile(filepath.Join(Root(procRoot), strconv.Itoa(pid), "environ"))
	if err != nil {
		return nil
	}
	return bytes.Split(env, []byte{0})
}

// WithEnv lists the processes whose environment has kv (KEY=value).
func WithEnv(procRoot, kv string) []int {
	want := []byte(kv)
	var out []int
	Each(procRoot, func(pid int) bool {
		for _, v := range environ(procRoot, pid) {
			if bytes.Equal(v, want) {
				out = append(out, pid)
				break
			}
		}
		return true
	})
	return out
}

// EnvsRunning reports which of kvs some process has in its environment,
// in one pass over the processes.
func EnvsRunning(procRoot string, kvs []string) map[string]bool {
	found := map[string]bool{}
	want := map[string]bool{}
	for _, kv := range kvs {
		want[kv] = true
	}
	if len(want) == 0 {
		return found
	}
	Each(procRoot, func(pid int) bool {
		for _, v := range environ(procRoot, pid) {
			if want[string(v)] {
				found[string(v)] = true
			}
		}
		return len(found) < len(want)
	})
	return found
}

// Named reports whether a process whose name (comm) is name runs.
func Named(procRoot, name string) bool {
	found := false
	Each(procRoot, func(pid int) bool {
		b, err := os.ReadFile(filepath.Join(Root(procRoot), strconv.Itoa(pid), "comm"))
		found = err == nil && strings.TrimSpace(string(b)) == name
		return !found
	})
	return found
}
