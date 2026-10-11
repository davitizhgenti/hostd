package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRollbackScript runs deploy/files/rollback.sh with systemctl and
// journalctl stubbed: back to the previous version, and the state and
// config from before the update restored only when the backup was made
// for the version that failed.
func TestRollbackScript(t *testing.T) {
	script, _ := filepath.Abs(filepath.Join("..", "..", "deploy", "files", "rollback.sh"))
	setup := func(t *testing.T, backupFor string) (lib, state, config string, run func() string) {
		dir := t.TempDir()
		lib, state = filepath.Join(dir, "lib"), filepath.Join(dir, "state")
		config = filepath.Join(dir, "config", "hostd.toml")
		bin := filepath.Join(dir, "bin")
		for _, d := range []string{lib + "/versions/v1", lib + "/versions/v2", state, filepath.Dir(config), bin} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for _, v := range []string{"v1", "v2"} {
			_ = os.WriteFile(filepath.Join(lib, "versions", v, "hostd"), []byte("#!/bin/sh\n"), 0o755)
		}
		_ = os.Symlink("versions/v2", filepath.Join(lib, "current"))
		_ = os.WriteFile(filepath.Join(lib, "previous"), []byte("versions/v1\n"), 0o644)
		_ = os.WriteFile(filepath.Join(lib, "versions/v1/state.db.bak"), []byte("old state"), 0o600)
		_ = os.WriteFile(filepath.Join(lib, "versions/v1/hostd.toml.bak"), []byte("old config"), 0o600)
		if backupFor != "" {
			_ = os.WriteFile(filepath.Join(lib, "versions/v1/backup-for"), []byte(backupFor+"\n"), 0o600)
		}
		_ = os.WriteFile(filepath.Join(state, "state.db"), []byte("new state"), 0o600)
		_ = os.WriteFile(filepath.Join(state, "state.db-wal"), []byte("wal"), 0o600)
		_ = os.WriteFile(config, []byte("new config"), 0o644)
		for _, stub := range []string{"systemctl", "journalctl"} {
			_ = os.WriteFile(filepath.Join(bin, stub), []byte("#!/bin/sh\nexit 0\n"), 0o755)
		}
		run = func() string {
			cmd := exec.Command("sh", script)
			cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + dir, "HOSTD_LIB=" + lib,
				"HOSTD_STATE=" + state, "HOSTD_CONFIG=" + config}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			return string(out)
		}
		return lib, state, config, run
	}
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }

	t.Run("the backup for this update", func(t *testing.T) {
		lib, state, config, run := setup(t, "versions/v2")
		out := run()
		if cur, _ := os.Readlink(filepath.Join(lib, "current")); cur != "versions/v1" {
			t.Fatalf("current = %q\n%s", cur, out)
		}
		if read(state+"/state.db") != "old state" || read(state+"/state.db.from-v2") != "new state" ||
			read(state+"/state.db-wal.from-v2") != "wal" || read(config) != "old config" || read(config+".from-v2") != "new config" {
			t.Fatalf("not restored:\n%s", out)
		}
		if _, err := os.Stat(filepath.Join(lib, "versions/v1/backup-for")); err == nil {
			t.Fatal("the backup can be used again")
		}
		if _, err := os.Stat(state + "/state.db-wal"); err == nil {
			t.Fatal("the newer WAL is still beside the restored database")
		}
	})
	t.Run("an old backup", func(t *testing.T) {
		// The update came through the installer: the backup is from an
		// earlier update and must not replace newer data.
		lib, state, config, run := setup(t, "versions/v0")
		out := run()
		if cur, _ := os.Readlink(filepath.Join(lib, "current")); cur != "versions/v1" {
			t.Fatalf("current = %q", cur)
		}
		if read(state+"/state.db") != "new state" || read(config) != "new config" || strings.Contains(out, "is back") {
			t.Fatalf("restored an old backup:\n%s", out)
		}
	})
}
