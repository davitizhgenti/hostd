package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/davitizhgenti/hostd/internal/daemon"
)

// The scripts in testdata/script run real hostd and hostctl processes
// against each other over a unix socket. They double as documentation of
// how hostctl is used.
func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"hostctl": func() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) },
		"hostd":   func() { os.Exit(daemon.Run(context.Background(), os.Args[1:], os.Stderr)) },
	})
}

func TestScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir:                 "testdata/script",
		RequireExplicitExec: true,
		Setup: func(env *testscript.Env) error {
			// Short paths: unix socket paths are limited to 108 bytes.
			run, err := os.MkdirTemp("", "hostd-run-")
			if err != nil {
				return err
			}
			env.Defer(func() { os.RemoveAll(run) })
			env.Setenv("HOME", env.WorkDir)
			env.Setenv("XDG_CONFIG_HOME", filepath.Join(env.WorkDir, "config"))
			env.Setenv("XDG_STATE_HOME", filepath.Join(env.WorkDir, "state"))
			env.Setenv("XDG_RUNTIME_DIR", run)
			return nil
		},
		Cmds: map[string]func(ts *testscript.TestScript, neg bool, args []string){
			"waitfile": waitFile,
			"exits":    exits,
			"envfrom":  envFrom,
		},
	})
}

// waitfile path: wait up to 10s for path to exist.
func waitFile(ts *testscript.TestScript, neg bool, args []string) {
	if neg || len(args) != 1 {
		ts.Fatalf("usage: waitfile path")
	}
	path := ts.MkAbs(args[0])
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			ts.Fatalf("%s did not appear", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// exits N command args...: run command and require exit code N.
func exits(ts *testscript.TestScript, neg bool, args []string) {
	if neg || len(args) < 2 {
		ts.Fatalf("usage: exits code command [args...]")
	}
	want, err := strconv.Atoi(args[0])
	if err != nil {
		ts.Fatalf("bad exit code %q", args[0])
	}
	err = ts.Exec(args[1], args[2:]...)
	got := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		got = ee.ExitCode()
	} else if err != nil {
		ts.Fatalf("%v", err)
	}
	if got != want {
		ts.Fatalf("exit code %d, want %d", got, want)
	}
}

// envfrom VAR regexp: set VAR to the first group of regexp in stdout.
func envFrom(ts *testscript.TestScript, neg bool, args []string) {
	if neg || len(args) != 2 {
		ts.Fatalf("usage: envfrom VAR regexp")
	}
	m := regexp.MustCompile(args[1]).FindStringSubmatch(ts.ReadFile("stdout"))
	if len(m) < 2 {
		ts.Fatalf("%q not found in stdout", args[1])
	}
	ts.Setenv(args[0], m[1])
}
