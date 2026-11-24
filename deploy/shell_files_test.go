package deploy

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// Every file of the shell that git tracks (but its tests and settings) is
// what the installer ships. Go leaves out embedded files that start with
// "_", which once dropped the Python packages' __init__.py and left a
// machine with a shell that could not start.
func TestShellShipsEveryTrackedFile(t *testing.T) {
	out, err := exec.Command("git", "ls-files", "files/shell").Output()
	if err != nil {
		t.Skip("not a git checkout:", err)
	}
	shipped := shellFiles()
	for _, f := range strings.Fields(string(out)) {
		name := strings.TrimPrefix(f, "files/shell/")
		base := name[strings.LastIndex(name, "/")+1:]
		if strings.HasPrefix(base, "test_") || strings.HasSuffix(base, ".toml") {
			continue
		}
		if !slices.Contains(shipped, name) {
			t.Errorf("%s is in git but not shipped by the installer", name)
		}
	}
}
