package deploy

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareInstallsNoHostd(t *testing.T) {
	m := newFakeMachine(t)
	var out bytes.Buffer
	s := &Setup{Root: m.root, Run: m, Out: NewOutput(&out, true), Chown: func(string, string) error { return nil }}
	if err := s.Prepare(context.Background(), Options{GPU: "other", SudoUser: "admin"}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(m.root, "etc/greetd/config.toml")); err != nil {
		t.Error("no autologin")
	}
	for _, p := range []string{"usr/local/bin/hostctl", "home/screen/.local/lib/hostd/current"} {
		if _, err := os.Lstat(filepath.Join(m.root, p)); err == nil {
			t.Errorf("prepare installed %s", p)
		}
	}
	if !strings.Contains(out.String(), "as screen (no root)") {
		t.Errorf("no next step:\n%s", out.String())
	}
}

func TestAdminAddons(t *testing.T) {
	m := newFakeMachine(t)
	var out bytes.Buffer
	s := &Setup{Root: m.root, Run: m, Out: NewOutput(&out, true), Chown: func(string, string) error { return nil }}
	ctx := context.Background()
	if err := s.AdminAddAddons(ctx, Options{}, []string{"steam"}); err == nil || !strings.Contains(err.Error(), "hostd-setup add steam") {
		t.Fatalf("a user add-on: %v", err)
	}
	if err := s.AdminRemoveAddons(ctx, Options{}, []string{"steam"}); err == nil {
		t.Fatal("removed a user add-on as admin")
	}
	if err := s.AdminAddAddons(ctx, Options{}, []string{"controllers"}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if got := s.adminInstalled(); len(got) != 1 || got[0] != "controllers" {
		t.Fatalf("admin list %v", got)
	}
	var list bytes.Buffer
	if err := s.ListAdminAddons(&list); err != nil || !strings.Contains(list.String(), "* controllers") || strings.Contains(list.String(), "steam") {
		t.Errorf("%v\n%s", err, list.String())
	}
	if err := s.AdminRemoveAddons(ctx, Options{}, []string{"controllers"}); err != nil {
		t.Fatal(err)
	}
}
