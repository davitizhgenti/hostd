package display

import (
	"bufio"
	"os"
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
