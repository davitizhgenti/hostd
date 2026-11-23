package daemon

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/davitizhgenti/hostd/core/store"
	"github.com/davitizhgenti/hostd/internal/execx"
	"github.com/davitizhgenti/hostd/internal/update"
	"github.com/davitizhgenti/hostd/sdk"
)

func fakeHostd(version string) []byte {
	return []byte("#!/bin/sh\necho \"hostd " + version + "\" >&2\n")
}

func TestUpdaterRelease(t *testing.T) {
	ctx := context.Background()
	setup := filepath.Join(t.TempDir(), "hostd-setup")
	_ = os.WriteFile(setup, []byte("#!/bin/sh\n"), 0o755)
	var ran [][]string
	active := false
	u := &updater{setup: setup, run: execx.Func(func(_ context.Context, c execx.Cmd) ([]byte, error) {
		ran = append(ran, c.Argv)
		name := c.Argv[0]
		if name == "systemctl" {
			if active {
				return nil, nil
			}
			return nil, errors.New("inactive")
		}
		active = true
		return nil, nil
	})}
	if err := u.UpdateRelease(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"systemd-run", "--user", "--collect", "--quiet", "--unit", "hostd-update-now", setup, "update"}
	if got := ran[len(ran)-1]; !reflect.DeepEqual(got, want) {
		t.Errorf("ran %q, want %q", got, want)
	}
	if !u.ReleaseRunning(ctx) {
		t.Error("not running after starting")
	}
	if err := u.UpdateRelease(ctx); sdk.CodeOf(err) != sdk.CodePreconditionFailed {
		t.Errorf("twice: %v", err)
	}
	// A machine whose installer is not the user's: says what to do instead.
	root := &updater{setup: filepath.Join(t.TempDir(), "none")}
	if err := root.UpdateRelease(ctx); sdk.CodeOf(err) != sdk.CodeModuleUnavailable {
		t.Errorf("no installer: %v", err)
	}
	if err := (&updater{}).UpdateRelease(ctx); sdk.CodeOf(err) != sdk.CodeModuleUnavailable {
		t.Errorf("no setup path: %v", err)
	}
}

func TestUpdaterInstall(t *testing.T) {
	ctx := context.Background()
	lib := update.Lib{Dir: t.TempDir()}
	if _, err := lib.Stage(ctx, bytes.NewReader(fakeHostd("v1"))); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.Switch("v1"); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := filepath.Join(t.TempDir(), "hostd.toml")
	_ = os.WriteFile(cfg, []byte(`modules = ["apps"]`), 0o644)
	u := &updater{lib: lib, store: st, configPath: cfg, restart: func() error { return nil }}

	ver, prev, err := u.Install(ctx, bytes.NewReader(fakeHostd("v2")))
	if err != nil || ver != "v2" || prev != "versions/v1" {
		t.Fatalf("Install = %q %q %v", ver, prev, err)
	}
	for _, f := range []string{"state.db.bak", "hostd.toml.bak"} {
		if _, err := os.Stat(filepath.Join(lib.Dir, "versions", "v1", f)); err != nil {
			t.Errorf("no backup %s next to the old version: %v", f, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(lib.Dir, "versions", "v1", "backup-for")); string(b) != "versions/v2\n" {
		t.Errorf("backup-for = %q", b)
	}
	if cur, _ := lib.Current(); cur != "versions/v2" {
		t.Fatalf("current = %q", cur)
	}
	if info := u.Info(); info["previous"] != "v1" {
		t.Fatalf("Info = %v", info)
	}
	// Installing the running version again is refused.
	if _, _, err := u.Install(ctx, bytes.NewReader(fakeHostd("v2"))); sdk.CodeOf(err) != sdk.CodeInvalidArgs {
		t.Fatalf("same version: %v", err)
	}
	// So is something that is not a hostd; nothing is switched.
	if _, _, err := u.Install(ctx, bytes.NewReader([]byte("#!/bin/sh\necho nope\n"))); sdk.CodeOf(err) != sdk.CodeInvalidArgs {
		t.Fatalf("not hostd: %v", err)
	}
	if cur, _ := lib.Current(); cur != "versions/v2" {
		t.Fatalf("a refused upload changed current to %q", cur)
	}
}

// hostd -install is the installer's way in, and keeps the same backup as an
// upload: the state database of the hostd that runs (still open), and its
// config, next to the version it replaces.
func TestInstallFromFile(t *testing.T) {
	ctx := context.Background()
	lib := t.TempDir()
	state := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "hostd.toml")
	bin := func(ver string) string {
		p := filepath.Join(t.TempDir(), "hostd")
		if err := os.WriteFile(p, fakeHostd(ver), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	var out bytes.Buffer
	run := func(path string) int {
		out.Reset()
		return installFromFile(path, lib, state, cfg, true, &out)
	}

	// The first install: nothing runs yet, so there is nothing to keep.
	if code := run(bin("v1")); code != 0 {
		t.Fatalf("first install: %d %s", code, out.String())
	}
	l := update.Lib{Dir: lib}
	if cur, _ := l.Current(); cur != "versions/v1" || l.Previous() != "" {
		t.Fatalf("current %q, previous %q", cur, l.Previous())
	}
	if _, err := os.Stat(filepath.Join(lib, "versions", "v1", "backup-for")); err == nil {
		t.Fatal("a backup for a first install")
	}

	// v1 runs, with its database open; v2 is installed from outside.
	st, err := store.Open(ctx, filepath.Join(state, "state.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, _, err := st.CreateToken(ctx, "phone", sdk.SourceManual, []string{"read"}, 0); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(cfg, []byte(`modules = ["apps"]`), 0o644)
	if code := run(bin("v2")); code != 0 || !bytes.Contains(out.Bytes(), []byte("kept for a rollback")) {
		t.Fatalf("update: %d %s", code, out.String())
	}
	old := filepath.Join(lib, "versions", "v1")
	if b, _ := os.ReadFile(filepath.Join(old, "backup-for")); string(b) != "versions/v2\n" {
		t.Fatalf("backup-for = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(old, "hostd.toml.bak")); string(b) != `modules = ["apps"]` {
		t.Fatalf("config copy = %q", b)
	}
	copyDB, err := store.Open(ctx, filepath.Join(old, "state.db.bak"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	toks, _ := copyDB.ListTokens(ctx)
	copyDB.Close()
	if len(toks) != 1 || toks[0].Name != "phone" {
		t.Fatalf("the copy has %+v", toks)
	}
	if cur, _ := l.Current(); cur != "versions/v2" || l.Previous() != "versions/v1" {
		t.Fatalf("current %q, previous %q", cur, l.Previous())
	}

	// The same version again changes nothing and is not an error.
	if code := run(bin("v2")); code != 0 || !bytes.Contains(out.Bytes(), []byte("already the current version")) {
		t.Fatalf("same version: %d %s", code, out.String())
	}
	// Not a hostd: refused, nothing switched.
	bad := filepath.Join(t.TempDir(), "x")
	_ = os.WriteFile(bad, []byte("#!/bin/sh\necho nope\n"), 0o755)
	if code := run(bad); code == 0 {
		t.Fatalf("not a hostd accepted: %s", out.String())
	}
	if cur, _ := l.Current(); cur != "versions/v2" {
		t.Fatalf("a refused binary changed current to %q", cur)
	}
}
