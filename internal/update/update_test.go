package update

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakeHostd is a script that answers -version like hostd does.
func fakeHostd(version string) []byte {
	return []byte("#!/bin/sh\necho \"hostd " + version + "\" >&2\n")
}

func TestStageAndSwitch(t *testing.T) {
	l := Lib{Dir: t.TempDir()}
	ctx := context.Background()

	v1, err := l.Stage(ctx, bytes.NewReader(fakeHostd("v1")))
	if err != nil || v1 != "v1" {
		t.Fatalf("stage v1: %q %v", v1, err)
	}
	if prev, err := l.Switch("v1"); err != nil || prev != "" {
		t.Fatalf("first switch: %q %v", prev, err)
	}
	if cur, _ := l.Current(); cur != "versions/v1" {
		t.Fatalf("current = %q", cur)
	}

	if _, err := l.Stage(ctx, bytes.NewReader(fakeHostd("v2"))); err != nil {
		t.Fatal(err)
	}
	prev, err := l.Switch("v2")
	if err != nil || prev != "versions/v1" || l.Previous() != "versions/v1" {
		t.Fatalf("switch to v2: prev %q, file %q, %v", prev, l.Previous(), err)
	}
	if cur, _ := l.Current(); cur != "versions/v2" {
		t.Fatalf("current = %q", cur)
	}
	// Switching to the current version again changes nothing.
	if prev, err := l.Switch("v2"); err != nil || prev != "versions/v1" {
		t.Fatalf("switch to current: %q %v", prev, err)
	}
	if _, err := l.Switch("v9"); err == nil {
		t.Fatal("switched to a version that is not installed")
	}
}

func TestStageRefusals(t *testing.T) {
	l := Lib{Dir: t.TempDir()}
	ctx := context.Background()
	for name, data := range map[string][]byte{
		"empty":           {},
		"not runnable":    []byte("\x7fELF garbage for another machine"),
		"not hostd":       []byte("#!/bin/sh\necho hello\n"),
		"fails":           []byte("#!/bin/sh\nexit 3\n"),
		"version garbage": []byte("#!/bin/sh\necho 'hostd ../../etc'\n"),
	} {
		if v, err := l.Stage(ctx, bytes.NewReader(data)); err == nil {
			t.Errorf("%s: staged as %q", name, v)
		}
	}
	// Nothing half-written is left behind.
	entries, _ := os.ReadDir(filepath.Join(l.Dir, "versions"))
	if len(entries) != 0 {
		t.Fatalf("left behind: %v", entries)
	}
}

func TestPruneKeepsCurrentPreviousAndNewest(t *testing.T) {
	l := Lib{Dir: t.TempDir()}
	ctx := context.Background()
	for i, v := range []string{"v1", "v2", "v3", "v4", "v5"} {
		if _, err := l.Stage(ctx, bytes.NewReader(fakeHostd(v))); err != nil {
			t.Fatal(err)
		}
		// distinct ages, oldest first
		old := time.Now().Add(time.Duration(i-10) * time.Minute)
		_ = os.Chtimes(filepath.Join(l.Dir, "versions", v), old, old)
		if _, err := l.Switch(v); err != nil {
			t.Fatal(err)
		}
	}
	got := l.Versions()
	want := []string{"v5", "v4", "v3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("versions = %v, want %v", got, want)
	}
}

func TestRollbackScript(t *testing.T) {
	// The installer's rollback script, run against a real layout: it must
	// switch current back to previous without any hostd binary's help.
	script, err := os.ReadFile("../../deploy/files/rollback.sh")
	if err != nil {
		t.Fatal(err)
	}
	l := Lib{Dir: t.TempDir()}
	ctx := context.Background()
	for _, v := range []string{"v1", "v2"} {
		if _, err := l.Stage(ctx, bytes.NewReader(fakeHostd(v))); err != nil {
			t.Fatal(err)
		}
		if _, err := l.Switch(v); err != nil {
			t.Fatal(err)
		}
	}
	bin := t.TempDir() // a systemctl that only records its calls
	_ = os.WriteFile(filepath.Join(bin, "systemctl"), []byte("#!/bin/sh\necho \"$@\" >> \""+l.Dir+"/systemctl.log\"\n"), 0o755)
	_ = os.WriteFile(filepath.Join(l.Dir, "rollback.sh"), script, 0o755)

	run := func() {
		t.Helper()
		cmd := execCommand(filepath.Join(l.Dir, "rollback.sh"))
		cmd.Env = append(os.Environ(), "HOSTD_LIB="+l.Dir, "PATH="+bin+":"+os.Getenv("PATH"))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("rollback.sh: %v\n%s", err, out)
		}
	}
	run()
	if cur, _ := l.Current(); cur != "versions/v1" || l.RolledBackFrom() != "versions/v2" {
		t.Fatalf("after rollback: current %q, rolled back from %q", cur, l.RolledBackFrom())
	}
	log, _ := os.ReadFile(filepath.Join(l.Dir, "systemctl.log"))
	if !strings.Contains(string(log), "--user reset-failed hostd.service") || !strings.Contains(string(log), "--user start hostd.service") {
		t.Fatalf("systemctl calls:\n%s", log)
	}
	// Running it again (previous is now current) does nothing.
	run()
	if cur, _ := l.Current(); cur != "versions/v1" {
		t.Fatalf("second rollback moved current to %q", cur)
	}
	// A successful update clears the rollback mark.
	if _, err := l.Switch("v2"); err != nil || l.RolledBackFrom() != "" {
		t.Fatalf("mark not cleared: %q %v", l.RolledBackFrom(), err)
	}
}

var execCommand = exec.Command
