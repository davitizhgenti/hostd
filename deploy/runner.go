package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// A Cmd is a command the installer runs.
type Cmd struct {
	Argv  []string
	Env   []string  // added to the installer's environment
	Stdin io.Reader // nil: none
	// Stream sends its output to the installer's output (apt, a download);
	// otherwise it is captured and returned, or written to Stdout if set.
	Stream bool
	Stdout io.Writer
}

// A Runner runs commands: on the machine (ExecRunner), or in a test, where
// it records them and answers as the test says.
type Runner interface {
	Run(ctx context.Context, c Cmd) ([]byte, error)
}

// ExecRunner runs commands for real. Streamed output goes to Out.
type ExecRunner struct{ Out io.Writer }

func (r ExecRunner) Run(ctx context.Context, c Cmd) ([]byte, error) {
	if len(c.Argv) == 0 {
		return nil, errors.New("deploy: empty command")
	}
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	cmd.Env = append(cmd.Environ(), c.Env...)
	cmd.Stdin = c.Stdin
	if c.Stream {
		cmd.Stdout, cmd.Stderr = r.Out, r.Out
		err := cmd.Run()
		return nil, commandError(c, err, nil)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if c.Stdout != nil {
		cmd.Stdout, cmd.Stderr = c.Stdout, c.Stdout
	}
	err := cmd.Run()
	return out.Bytes(), commandError(c, err, errOut.Bytes())
}

// commandError names the command and keeps the end of what it said.
func commandError(c Cmd, err error, stderr []byte) error {
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(stderr))
	if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
		msg = msg[i+1:]
	}
	if msg != "" {
		return fmt.Errorf("%s: %w: %s", strings.Join(c.Argv, " "), err, msg)
	}
	return fmt.Errorf("%s: %w", strings.Join(c.Argv, " "), err)
}
