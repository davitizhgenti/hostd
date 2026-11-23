// Package execx runs the programs hostd and its installer use (git, wpctl,
// pw-dump, journalctl, ssh-keygen, compose, apt...). It is the one place
// that starts a process and turns its failure into an error that names the
// command and keeps the end of what it said. Code takes a Runner, so tests
// answer for the programs instead of needing them installed.
package execx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
)

// A Cmd is a command to run.
type Cmd struct {
	Argv []string
	// Env is added to the environment the command inherits.
	Env []string
	// Environ, when not nil, is the command's whole environment instead of
	// the inherited one (Env is still added).
	Environ []string
	Dir     string
	Stdin   io.Reader // nil: none
	// Stream sends the output to the Runner's Out (apt, a download): nothing
	// is captured.
	Stream bool
	// Stdout, if set, gets stdout and stderr together (a build log); the
	// end of it is still kept for the error.
	Stdout io.Writer
	// Tail is how many lines of what the command said the error keeps
	// (default 1: the last, which is usually the reason).
	Tail int
	// Group runs the command in its own process group, and kills the whole
	// group when the context ends and once the command has exited: nothing
	// it started outlives it (scripts).
	Group bool
}

func (c Cmd) String() string { return strings.Join(c.Argv, " ") }

// A Runner runs commands: on the machine (OS), or in a test, where it
// records them and answers as the test says (Func).
type Runner interface {
	Run(ctx context.Context, c Cmd) ([]byte, error)
}

// Func is a Runner made from a function, for tests.
type Func func(ctx context.Context, c Cmd) ([]byte, error)

func (f Func) Run(ctx context.Context, c Cmd) ([]byte, error) { return f(ctx, c) }

// OS runs commands for real. Streamed output goes to Out (nil: dropped).
type OS struct{ Out io.Writer }

// keep is how much of a command's output is held for its error.
const keep = 64 << 10

func (r OS) Run(ctx context.Context, c Cmd) ([]byte, error) {
	if len(c.Argv) == 0 {
		return nil, errors.New("execx: empty command")
	}
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	env := c.Environ
	if env == nil {
		env = cmd.Environ()
	}
	if c.Environ != nil || len(c.Env) > 0 {
		cmd.Env = slices.Concat(env, c.Env)
	}
	cmd.Dir = c.Dir
	cmd.Stdin = c.Stdin
	run := cmd.Run
	if c.Group {
		run = func() error { return runGroup(cmd) }
	}
	if c.Stream {
		out := r.Out
		if out == nil {
			out = io.Discard
		}
		cmd.Stdout, cmd.Stderr = out, out
		return nil, wrap(c, run(), nil)
	}
	var out bytes.Buffer
	said := &tail{}
	cmd.Stdout, cmd.Stderr = &out, said
	if c.Stdout != nil {
		w := io.MultiWriter(c.Stdout, said)
		cmd.Stdout, cmd.Stderr = w, w
	}
	err := run()
	return out.Bytes(), wrap(c, err, said.Bytes())
}

// runGroup runs cmd in its own process group and kills the group as soon
// as cmd exits (or its context ends). The output goes through pipes of
// its own, so waiting ends with cmd, not with the last process that holds
// its output open; killing the group closes them.
func runGroup(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	var copies sync.WaitGroup
	var ends []*os.File // the command's ends, closed here once it has them
	pipe := func(w io.Writer) (io.Writer, error) {
		if _, ok := w.(*os.File); ok || w == nil {
			return w, nil
		}
		pr, pw, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		ends = append(ends, pw)
		copies.Go(func() {
			_, _ = io.Copy(w, pr)
			pr.Close()
		})
		return pw, nil
	}
	closeEnds := func() {
		for _, f := range ends {
			f.Close()
		}
	}
	same := cmd.Stdout == cmd.Stderr
	var err error
	if cmd.Stdout, err = pipe(cmd.Stdout); err == nil {
		if same {
			cmd.Stderr = cmd.Stdout
		} else {
			cmd.Stderr, err = pipe(cmd.Stderr)
		}
	}
	if err == nil {
		err = cmd.Start()
	}
	closeEnds()
	if err != nil {
		copies.Wait()
		return err
	}
	err = cmd.Wait()
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // anything it left behind
	copies.Wait()
	return err
}

// Run runs argv with OS and returns its output with surrounding space
// trimmed: the common case of asking a program something.
func Run(ctx context.Context, r Runner, argv ...string) (string, error) {
	out, err := r.Run(ctx, Cmd{Argv: argv})
	return strings.TrimSpace(string(out)), err
}

// An Error is a command that could not start or ended badly.
type Error struct {
	Cmd  string // the command line
	Err  error  // from os/exec: *exec.ExitError, exec.ErrNotFound...
	Said string // the end of what it wrote (Cmd.Tail lines)
}

func (e *Error) Error() string {
	if e.Said != "" {
		if strings.Contains(e.Said, "\n") {
			return fmt.Sprintf("%s: %v\n%s", e.Cmd, e.Err, e.Said)
		}
		return fmt.Sprintf("%s: %v: %s", e.Cmd, e.Err, e.Said)
	}
	return fmt.Sprintf("%s: %v", e.Cmd, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// NotFound reports whether err is a program that is not installed.
func NotFound(err error) bool {
	var ee *exec.Error
	return errors.As(err, &ee) || errors.Is(err, exec.ErrNotFound)
}

func wrap(c Cmd, err error, said []byte) error {
	if err == nil {
		return nil
	}
	n := c.Tail
	if n <= 0 {
		n = 1
	}
	return &Error{Cmd: c.String(), Err: err, Said: LastLines(string(said), n)}
}

// LastLines is the last n lines of s, without trailing space.
func LastLines(s string, n int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// tail keeps the last keep bytes written to it.
type tail struct{ b bytes.Buffer }

func (t *tail) Write(p []byte) (int, error) {
	t.b.Write(p)
	if t.b.Len() > keep {
		t.b.Next(t.b.Len() - keep)
	}
	return len(p), nil
}

func (t *tail) Bytes() []byte { return t.b.Bytes() }
