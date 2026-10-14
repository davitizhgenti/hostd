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
	ver, prev, err := installVersion(ctx, u.lib, binary, u.store.Backup, u.configPath)
	switch {
	case errors.Is(err, errAlreadyCurrent):
		return "", "", sdk.Errorf(sdk.CodeInvalidArgs, "version %s is already running", ver)
	case errors.As(err, new(stageError)):
		return "", "", sdk.Errorf(sdk.CodeInvalidArgs, "%v", err)
	}
	return ver, prev, err
}

var errAlreadyCurrent = errors.New("already the current version")

// stageError is a binary that cannot be installed: not a hostd, not for
// this machine, too big.
type stageError struct{ error }

// installVersion is every way of installing a new hostd (an upload to the
// API, hostd -install, the installer): it stages binary, keeps a copy of the
// state database (backup writes it; os.ErrNotExist means there is none yet)
// and of hostd.toml next to the version that runs now, notes which update
// they are for, and switches. If the new version then fails to start, the
// rollback script returns to the old one and puts those copies back.
func installVersion(ctx context.Context, lib update.Lib, binary io.Reader, backup func(context.Context, string) error,
	configPath string) (ver, prev string, err error) {
	cur, err := lib.Current()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	ver, err = lib.Stage(ctx, binary)
	if err != nil {
		return "", "", stageError{err}
	}
	if filepath.Join("versions", ver) == cur {
		return ver, lib.Previous(), errAlreadyCurrent
	}
	if cur != "" {
		// The backup lets a rollback return to data the old version knows,
		// even if the new one migrates the database.
		backupDir := filepath.Join(lib.Dir, cur)
		dbCopy := filepath.Join(backupDir, "state.db.bak")
		switch err := backup(ctx, dbCopy); {
		case errors.Is(err, os.ErrNotExist):
			_ = os.Remove(dbCopy) // no database yet; an old copy must not come back
		case err != nil:
			return "", "", fmt.Errorf("backing up the state database: %w", err)
		}
		if b, err := os.ReadFile(configPath); err == nil {
			_ = os.WriteFile(filepath.Join(backupDir, "hostd.toml.bak"), b, 0o600)
		}
		// Which update the backup is for: the rollback script restores it
		// only when that version fails.
		if err := os.WriteFile(filepath.Join(backupDir, "backup-for"), []byte(filepath.Join("versions", ver)+"\n"), 0o600); err != nil {
			return "", "", err
		}
	}
	prev, err = lib.Switch(ver)
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

// installFromFile is `hostd -install FILE`: the same staging, backup and
// switch, run from a shell (over SSH, or by the installer) rather than
// through the API. libDir is the installation ("": the one this binary runs
// from). Then it restarts hostd with systemctl, which is safe here since
// this process is not hostd's own; with noRestart the caller does that.
func installFromFile(path, libDir, stateDir, configPath string, noRestart bool, stderr io.Writer) int {
	lib := update.Lib{Dir: libDir}
	if libDir == "" {
		var err error
		if lib, err = update.FromExecutable(); err != nil {
			fmt.Fprintln(stderr, "hostd -install:", err)
			return 1
		}
	}
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintln(stderr, "hostd -install:", err)
		return 1
	}
	defer f.Close()
	backup := func(ctx context.Context, dest string) error {
		return store.BackupFile(ctx, filepath.Join(stateDir, "state.db"), dest)
	}
	ver, prev, err := installVersion(context.Background(), lib, f, backup, configPath)
	switch {
	case errors.Is(err, errAlreadyCurrent):
		fmt.Fprintf(stderr, "hostd %s is already the current version\n", ver)
		return 0
	case err != nil:
		fmt.Fprintln(stderr, "hostd -install:", err)
		return 1
	}
	if prev == "" {
		fmt.Fprintf(stderr, "installed hostd %s\n", ver)
	} else {
		fmt.Fprintf(stderr, "installed hostd %s (previous: %s; its data is kept for a rollback)\n", ver, filepath.Base(prev))
	}
	if noRestart {
		return 0
	}
	fmt.Fprintln(stderr, "restarting")
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
