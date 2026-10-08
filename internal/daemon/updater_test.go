package daemon

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/davitizhgenti/hostd/core/store"
	"github.com/davitizhgenti/hostd/internal/update"
	"github.com/davitizhgenti/hostd/sdk"
)

func fakeHostd(version string) []byte {
	return []byte("#!/bin/sh\necho \"hostd " + version + "\" >&2\n")
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
