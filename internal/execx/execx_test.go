package execx

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestOS(t *testing.T) {
	ctx := context.Background()
	var streamed bytes.Buffer
	r := OS{Out: &streamed}

	out, err := r.Run(ctx, Cmd{Argv: []string{"sh", "-c", "echo out; echo err >&2; echo $X"}, Env: []string{"X=1"}})
	if err != nil || string(out) != "out\n1\n" {
		t.Fatalf("captured %q %v", out, err)
	}
	if _, err := r.Run(ctx, Cmd{Argv: []string{"sh", "-c", "echo $X"}, Env: []string{"X=streamed"}, Stream: true}); err != nil || streamed.String() != "streamed\n" {
		t.Fatalf("streamed %q %v", streamed.String(), err)
	}
	var w bytes.Buffer
	if _, err := r.Run(ctx, Cmd{Argv: []string{"sh", "-c", "echo to-w; echo also >&2"}, Stdout: &w}); err != nil || w.String() != "to-w\nalso\n" {
		t.Fatalf("to a writer %q %v", w.String(), err)
	}
	dir := t.TempDir()
	if out, err := Run(ctx, r, "sh", "-c", "pwd"); err != nil || out == dir {
		t.Fatalf("pwd %q %v", out, err)
	}
	if out, _ := r.Run(ctx, Cmd{Argv: []string{"pwd"}, Dir: dir}); strings.TrimSpace(string(out)) != dir {
		t.Fatalf("dir %q", out)
	}
	// Environ replaces the inherited environment.
	t.Setenv("HOSTD_EXECX", "inherited")
	if out, _ := Run(ctx, r, "sh", "-c", "echo ${HOSTD_EXECX:-gone}"); out != "inherited" {
		t.Fatalf("inherited %q", out)
	}
	if out, _ := r.Run(ctx, Cmd{Argv: []string{"sh", "-c", "echo ${HOSTD_EXECX:-gone} $Y"}, Environ: []string{"PATH=/usr/bin:/bin"}, Env: []string{"Y=y"}}); string(out) != "gone y\n" {
		t.Fatalf("environ %q", out)
	}
	if _, err := r.Run(ctx, Cmd{}); err == nil {
		t.Fatal("an empty command ran")
	}
}

func TestErrors(t *testing.T) {
	ctx := context.Background()
	_, err := OS{}.Run(ctx, Cmd{Argv: []string{"sh", "-c", "echo first >&2; echo why it failed >&2; exit 3"}})
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 3 || err.Error() != "sh -c echo first >&2; echo why it failed >&2; exit 3: exit status 3: why it failed" {
		t.Fatalf("one line: %v", err)
	}
	_, err = OS{}.Run(ctx, Cmd{Argv: []string{"sh", "-c", "echo a; echo b; echo c; exit 1"}, Stdout: &bytes.Buffer{}, Tail: 2})
	if !strings.HasSuffix(err.Error(), "exit status 1\nb\nc") {
		t.Fatalf("two lines: %q", err)
	}
	_, err = OS{}.Run(ctx, Cmd{Argv: []string{"hostd-no-such-program"}})
	if !NotFound(err) || NotFound(errors.New("x")) {
		t.Fatalf("not found: %v", err)
	}
	if _, err := (OS{}).Run(ctx, Cmd{Argv: []string{"false"}, Stream: true}); err == nil || err.Error() != "false: exit status 1" {
		t.Fatalf("streamed failure: %v", err)
	}
}

func TestFunc(t *testing.T) {
	var got []string
	r := Func(func(_ context.Context, c Cmd) ([]byte, error) {
		got = append(got, c.String())
		return []byte(" answer \n"), nil
	})
	if out, err := Run(context.Background(), r, "wpctl", "status"); err != nil || out != "answer" || got[0] != "wpctl status" {
		t.Fatalf("%q %v %v", out, err, got)
	}
}

func TestTailKeepsTheEnd(t *testing.T) {
	var tl tail
	big := bytes.Repeat([]byte("x"), keep)
	_, _ = tl.Write(big)
	_, _ = tl.Write([]byte("\nthe end"))
	if len(tl.Bytes()) != keep || !bytes.HasSuffix(tl.Bytes(), []byte("the end")) {
		t.Fatalf("kept %d bytes", len(tl.Bytes()))
	}
	if LastLines("  ", 3) != "" || LastLines("a\nb\nc\n", 2) != "b\nc" {
		t.Fatal("LastLines")
	}
}

func TestGroupEndsWhatTheCommandLeftBehind(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	_, err := OS{}.Run(context.Background(), Cmd{Argv: []string{"sh", "-c", "sleep 60 & echo $! > " + pidFile}, Group: true})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(pidFile)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	for range 200 {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the background sleep (%d) outlived its script", pid)
}

func TestGroupStopsAtTheDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := OS{}.Run(ctx, Cmd{Argv: []string{"sh", "-c", "sleep 60; true"}, Group: true})
	if err == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
}
