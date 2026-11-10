package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

func TestUsageAndRoot(t *testing.T) {
	var out, errs bytes.Buffer
	if code := run(context.Background(), []string{"--help"}, &out, &errs); code != 0 || !strings.Contains(out.String(), "sudo hostd-admin prepare") {
		t.Fatalf("help: %d", code)
	}
	if code := run(context.Background(), []string{"frobnicate"}, &out, &errs); code != 2 {
		t.Fatalf("unknown command: %d", code)
	}
	if code := run(context.Background(), []string{"prepare", "--bogus"}, &out, &errs); code != 1 || !strings.Contains(errs.String(), "unknown option") {
		t.Fatalf("option: %d %s", code, errs.String())
	}
	if os.Geteuid() == 0 {
		t.Skip("runs as root")
	}
	errs.Reset()
	if code := run(context.Background(), []string{"prepare"}, &out, &errs); code != 1 || !strings.Contains(errs.String(), "run as root: sudo hostd-admin prepare") {
		t.Fatalf("not root: %d %s", code, errs.String())
	}
	if code := run(context.Background(), []string{"add"}, &out, &errs); code != 1 {
		t.Fatalf("add without root: %d", code)
	}
}

func TestArgumentsAsRoot(t *testing.T) {
	old := geteuid
	geteuid = func() int { return 0 } // only commands that change nothing follow
	defer func() { geteuid = old }()
	var out, errs bytes.Buffer
	for argv, want := range map[string]int{
		"addons":              0,
		"user list":           0,
		"add":                 2,
		"remove":              2,
		"user":                2,
		"user add":            2,
		"user list extra":     2,
		"prepare --gpu":       1,
		"prepare --port x":    1,
		"prepare --port":      1,
		"user add --purge":    2,
		"remove --all --user": 1,
		"seats list":          0,
		"seats":               2,
		"seats add alice":     2,
		"seats add alice x":   1,
		"seats remove":        2,
		"uninstall --bogus":   1,
	} {
		out.Reset()
		errs.Reset()
		if code := run(context.Background(), strings.Fields(argv), &out, &errs); code != want {
			t.Errorf("%q: %d, want %d\n%s%s", argv, code, want, out.String(), errs.String())
		}
	}
}
