package contract

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProcessDescends(t *testing.T) {
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
		if got := ProcessDescends(root, c.pid, c.ancestor); got != c.want {
			t.Errorf("ProcessDescends(%d, %d) = %v", c.pid, c.ancestor, got)
		}
	}
}
