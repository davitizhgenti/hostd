package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/davitizhgenti/hostd/core/store"
	"github.com/davitizhgenti/hostd/internal/update"
	"github.com/davitizhgenti/hostd/sdk"
)

// updater installs uploaded binaries (POST /v1/update).
type updater struct {
	lib        update.Lib
	store      *store.Store
	configPath string
	restart    func() error
}

// Install backs up the state database and config next to the running
// version, stages the new binary, and switches to it. Nothing is switched
// unless the binary runs here and is a hostd.
func (u *updater) Install(ctx context.Context, binary io.Reader) (string, string, error) {
	cur, err := u.lib.Current()
	if err != nil {
		return "", "", err
	}
	ver, err := u.lib.Stage(ctx, binary)
	if err != nil {
		return "", "", sdk.Errorf(sdk.CodeInvalidArgs, "%v", err)
	}
	if filepath.Join("versions", ver) == cur {
		return "", "", sdk.Errorf(sdk.CodeInvalidArgs, "version %s is already running", ver)
	}
	// The backup lets a rollback return to data the old version knows,
	// even if the new one migrates the database.
	backupDir := filepath.Join(u.lib.Dir, cur)
	if err := u.store.Backup(ctx, filepath.Join(backupDir, "state.db.bak")); err != nil {
		return "", "", fmt.Errorf("backing up the state database: %w", err)
	}
	if b, err := os.ReadFile(u.configPath); err == nil {
		_ = os.WriteFile(filepath.Join(backupDir, "hostd.toml.bak"), b, 0o600)
	}
	prev, err := u.lib.Switch(ver)
	if err != nil {
		return "", "", err
	}
	return ver, prev, nil
}

func (u *updater) Restart() error { return u.restart() }

func (u *updater) Info() map[string]any {
	out := map[string]any{"installed": u.lib.Versions()}
	if p := u.lib.Previous(); p != "" {
		out["previous"] = filepath.Base(p)
	}
	if r := u.lib.RolledBackFrom(); r != "" {
		out["rolled_back_from"] = filepath.Base(r)
	}
	return out
}

// installFromFile is `hostd -install FILE`: the same staging and switch,
// run from a shell (over SSH) when the running hostd cannot take an
// upload. It restarts hostd with systemctl, which is safe here since this
// process is not hostd's own.
func installFromFile(path string, stderr io.Writer) int {
	lib, err := update.FromExecutable()
	if err != nil {
		fmt.Fprintln(stderr, "hostd -install:", err)
		return 1
	}
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintln(stderr, "hostd -install:", err)
		return 1
	}
	defer f.Close()
	ver, err := lib.Stage(context.Background(), f)
	if err != nil {
		fmt.Fprintln(stderr, "hostd -install:", err)
		return 1
	}
	prev, err := lib.Switch(ver)
	if err != nil {
		fmt.Fprintln(stderr, "hostd -install:", err)
		return 1
	}
	fmt.Fprintf(stderr, "installed hostd %s (previous: %s); restarting\n", ver, filepath.Base(prev))
	// Restarting waits for the new hostd to report ready (Type=notify), or
	// for systemd to give up: at most a few start attempts.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "systemctl", "--user", "restart", "hostd.service")
	cmd.Stdout, cmd.Stderr = stderr, stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			fmt.Fprintln(stderr, "hostd did not start; systemd rolls back to the previous version")
		}
		return 1
	}
	return 0
}
