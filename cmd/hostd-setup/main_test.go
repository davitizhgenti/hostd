package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/davitizhgenti/hostd/deploy"
)

// The command line is the one the shell installer had: scripts and the
// update timer call it so.
func TestParse(t *testing.T) {
	for _, c := range []struct {
		argv []string
		want args
	}{
		{nil, args{cmd: "install"}},
		{[]string{"--gpu", "other", "--from", "/d"}, args{cmd: "install", gpu: "other", from: "/d", pass: []string{"--gpu", "other", "--from", "/d"}}},
		{[]string{"update", "steam", "--verbose", "--source", "/s", "dolphin"},
			args{cmd: "update", source: "/s", verbose: true, names: []string{"steam", "dolphin"}}},
		{[]string{"update", "--all"}, args{cmd: "update", all: true}},
		{[]string{"uninstall", "--purge", "--all"}, args{cmd: "uninstall", purge: true, all: true}},
		{[]string{"add", "controllers", "--user", "tv"}, args{cmd: "add", user: "tv", names: []string{"controllers"}, pass: []string{"--user", "tv"}}},
		{[]string{"auto-update"}, args{cmd: "auto-update"}},
		{[]string{"update", "--ssh-passwords", "keep"}, args{cmd: "update", sshPasswords: "keep", pass: []string{"--ssh-passwords", "keep"}}},
		{[]string{"-h"}, args{cmd: "help"}},
	} {
		got, err := parse(c.argv)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v: %+v %v, want %+v", c.argv, got, err, c.want)
		}
	}
	for argv, want := range map[string]string{
		"--gpu amd":             "--gpu must be",
		"--from":                "needs a value",
		"install extra":         "unexpected argument",
		"--purge":               "goes with uninstall",
		"install --all":         "goes with update or uninstall",
		"update --bogus":        "unknown option",
		"--ssh-passwords maybe": "keep or off",
	} {
		if _, err := parse(strings.Fields(argv)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", argv, err, want)
		}
	}
}

func TestRunWithoutRoot(t *testing.T) {
	var out, errs bytes.Buffer
	if code := run(context.Background(), []string{"--help"}, &out, &errs); code != 0 || !strings.Contains(out.String(), "sudo hostd-setup update") {
		t.Fatalf("help: %d\n%s", code, out.String())
	}
	if code := run(context.Background(), []string{"--gpu", "amd"}, &out, &errs); code != 2 || !strings.Contains(errs.String(), "--gpu must be") {
		t.Fatalf("usage: %d %s", code, errs.String())
	}
	if os.Geteuid() == 0 {
		t.Skip("the test runs as root")
	}
	errs.Reset()
	if code := run(context.Background(), []string{"update"}, &out, &errs); code != 1 || !strings.Contains(errs.String(), "run as root: sudo hostd-setup update") {
		t.Fatalf("not root: %d %s", code, errs.String())
	}
}

// okRunner answers every command as a Debian machine with a screen user.
type okRunner struct{ ran []string }

func (r *okRunner) Run(_ context.Context, c deploy.Cmd) ([]byte, error) {
	line := strings.Join(c.Argv, " ")
	r.ran = append(r.ran, line)
	switch line {
	case "dpkg --print-architecture":
		return []byte("amd64\n"), nil
	case "id -u screen":
		return []byte("1001\n"), nil
	case "getent passwd screen":
		return []byte("screen:x:1001:1001::/home/screen:/bin/bash\n"), nil
	}
	return nil, nil
}

func TestCommands(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "etc/hostd"), 0o755)
	var out bytes.Buffer
	s := &deploy.Setup{Root: root, Run: &okRunner{}, Out: deploy.NewOutput(&out, true), Chown: func(string, string) error { return nil }}
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "hostd-setup-linux-amd64"), []byte("next"), 0o755)
	var calls [][]string
	runNext := func(_ context.Context, path string, args, env []string) error {
		calls = append(calls, append(append([]string{path}, args...), env...))
		return nil
	}
	ctx := context.Background()
	cmd := func(argv ...string) error {
		a, err := parse(argv)
		if err != nil {
			t.Fatal(err)
		}
		up := deploy.UpdateArgs{Source: a.source, Verbose: a.verbose, Pass: a.pass}
		return do(ctx, a, s, deploy.Options{}, up, runNext, &out)
	}

	// add: recorded, then installed by an update that shows everything.
	if err := cmd("add", "controllers", "--source", src); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "etc/hostd/addons")); string(b) != "controllers\n" {
		t.Fatalf("installed %q", b)
	}
	last := strings.Join(calls[len(calls)-1], " ")
	if !strings.Contains(last, "install --verbose") || !strings.Contains(last, "HOSTD_ADDING=controllers") {
		t.Fatalf("add ran %q", last)
	}
	// update --all, update NAME.
	if err := cmd("update", "--all", "--source", src); err != nil || !strings.Contains(strings.Join(calls[len(calls)-1], " "), "HOSTD_UPDATE_MODE=all") {
		t.Fatalf("update --all: %v %q", err, calls[len(calls)-1])
	}
	if err := cmd("update", "steam", "--source", src); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("update of an add-on not installed: %v", err)
	}
	// addons lists them; remove takes one away.
	out.Reset()
	if err := cmd("addons"); err != nil || !strings.Contains(out.String(), "* controllers") {
		t.Fatalf("addons: %v\n%s", err, out.String())
	}
	if err := cmd("remove", "controllers"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "etc/hostd/addons")); strings.TrimSpace(string(b)) != "" {
		t.Fatalf("after remove %q", b)
	}
	if err := cmd("uninstall"); err != nil {
		t.Fatal(err)
	}
}

func TestExitCode(t *testing.T) {
	var errs bytes.Buffer
	if exitCode(nil, &errs) != 0 {
		t.Fatal("no error, not 0")
	}
	if exitCode(errors.New("broken"), &errs) != 1 || !strings.Contains(errs.String(), "broken") {
		t.Fatalf("an error: %s", errs.String())
	}
	// The newer installer failed and said why: its status, nothing more.
	errs.Reset()
	err := exec.Command("sh", "-c", "exit 7").Run()
	if code := exitCode(err, &errs); code != 7 || errs.Len() != 0 {
		t.Fatalf("exit %d %q", code, errs.String())
	}
}

// As root, a command that changes nothing (addons) goes through the whole
// of run: the environment read, the machine's setup built.
func TestRunAsRoot(t *testing.T) {
	geteuid = func() int { return 0 }
	t.Cleanup(func() { geteuid = os.Geteuid })
	t.Setenv("HOSTD_RELEASE_URL", "file:///nowhere")
	t.Setenv("HOSTD_RELEASE_KEY", "a key")
	t.Setenv("HOSTD_UPDATE_MODE", "quick")
	var out, errs bytes.Buffer
	if code := run(context.Background(), []string{"addons"}, &out, &errs); code != 0 || !strings.Contains(out.String(), "controllers") {
		t.Fatalf("addons: %d\n%s%s", code, out.String(), errs.String())
	}
}

func TestExecInstaller(t *testing.T) {
	if err := execInstaller(context.Background(), "true", nil, []string{"X=1"}); err != nil {
		t.Fatal(err)
	}
	if err := execInstaller(context.Background(), "false", nil, nil); err == nil {
		t.Fatal("false succeeded")
	}
}
