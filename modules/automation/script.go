package automation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/davitizhgenti/hostd/internal/tomlx"

	"github.com/davitizhgenti/hostd/sdk"
)

// Scripts are executables in the scripts folder that a rule runs. Each
// run gets a token of its own, limited to the scopes the script declares
// ("# hostd-scopes: audio display" near the top, or scopes in a
// <script>.toml beside it), which expires with the run; HOSTD_URL,
// HOSTD_TOKEN and HOSTD_EVENT are set, so hostctl and plain HTTP work
// with no setup.

// Tokens mints the short-lived token of a script run.
type Tokens interface {
	Mint(ctx context.Context, name string, scopes []string, ttl time.Duration) (secret string, revoke func(), err error)
}

// scriptSpec is a script file: what to run, with which scopes. A script is
// either a command (argv, started in the scripts folder) or inline shell.
type scriptSpec struct {
	Command []string `toml:"command" json:"command,omitempty"`
	Run     string   `toml:"run" json:"run,omitempty"`
	Scopes  []string `toml:"scopes" json:"scopes,omitempty"`
	Timeout string   `toml:"timeout" json:"timeout,omitempty"`
	timeout time.Duration
}

// ParseScript decodes and checks one script file.
func ParseScript(raw []byte) (scriptSpec, error) {
	var s scriptSpec
	if _, err := tomlx.Decode(raw, &s); err != nil {
		return s, err
	}
	switch {
	case len(s.Command) > 0 && s.Run != "":
		return s, errors.New("command and run are two ways to say what to run: give one")
	case len(s.Command) == 0 && strings.TrimSpace(s.Run) == "":
		return s, errors.New(`needs command (["python3", "evening.py"]) or run (shell commands)`)
	case len(s.Command) > 0 && s.Command[0] == "":
		return s, errors.New("command: the first word is empty")
	}
	s.timeout = 60 * time.Second
	if s.Timeout != "" {
		d, err := time.ParseDuration(s.Timeout)
		if err != nil || d <= 0 {
			return s, fmt.Errorf("timeout %q: a duration such as 60s", s.Timeout)
		}
		s.timeout = d
	}
	if slices.Contains(s.Scopes, sdk.ScopeAdmin) {
		return s, errors.New("scripts cannot have scope admin")
	}
	return s, nil
}

// runScript runs a rule's script and returns how it went.
func (m *Module) runScript(ctx context.Context, r Rule, ev sdk.Event) string {
	result, err := m.script(ctx, r.Do.Script, r.Name, r.Name, sdk.MustJSON(ev))
	if err != nil {
		m.log.Warn("a rule's script failed", "rule", r.Name, "script", r.Do.Script, "err", err)
		return "failed: " + err.Error()
	}
	return result
}

// script runs the script a name stands for. label names the run in its
// token and the log (the rule's name, or the script's); rule is what the
// script sees as HOSTD_RULE, empty when someone runs it by hand.
func (m *Module) script(ctx context.Context, name, label, rule string, event json.RawMessage) (string, error) {
	m.mu.Lock()
	set, ok := m.scripts[name]
	m.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("no script %q (hostctl get scripts)", name)
	}
	if m.opts.Tokens == nil {
		return "", errors.New("scripts cannot run here (no token service)")
	}
	m.mu.Lock()
	m.scriptRuns++
	token := fmt.Sprintf("script %s #%d", label, m.scriptRuns)
	m.mu.Unlock()
	secret, revoke, err := m.opts.Tokens.Mint(ctx, token, set.Scopes, set.timeout+30*time.Second)
	if err != nil {
		return "", fmt.Errorf("a token for the script: %w", err)
	}
	defer revoke()

	// The actions the script sends: their events carry the token's name.
	watch, stopWatch := context.WithCancel(ctx)
	var mu sync.Mutex
	var actions []string
	events := m.core.Subscribe(watch, "action.*")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range events {
			if e.Source != nil && e.Source.Name == token && e.Action != "" {
				mu.Lock()
				if !slices.Contains(actions, e.Action) {
					actions = append(actions, e.Action)
				}
				mu.Unlock()
			}
		}
	}()

	run, cancel := context.WithTimeout(ctx, set.timeout)
	defer cancel()
	var cmd *exec.Cmd
	if len(set.Command) > 0 {
		cmd = exec.CommandContext(run, set.Command[0], set.Command[1:]...)
	} else {
		cmd = exec.CommandContext(run, "sh", "-c", set.Run)
	}
	cmd.Dir = m.opts.ScriptsDir
	cmd.Env = append(os.Environ(), "HOSTD_URL="+m.opts.URL, "HOSTD_TOKEN="+secret,
		"HOSTD_EVENT="+string(event), "HOSTD_RULE="+rule)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) } // the whole group
	cmd.WaitDelay = 5 * time.Second
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // anything it left behind
	}
	time.Sleep(100 * time.Millisecond) // the last action's events are on their way
	stopWatch()
	<-done
	mu.Lock()
	sent := append([]string(nil), actions...)
	mu.Unlock()

	code := 0
	var exit *exec.ExitError
	switch {
	case run.Err() == context.DeadlineExceeded:
		m.log.Warn("a script ran out of time and was stopped", "script", name, "rule", rule, "timeout", set.timeout, "actions", sent)
		return "", fmt.Errorf("timed out after %s (%d action(s) sent)", set.timeout, len(sent))
	case errors.As(err, &exit):
		code = exit.ExitCode()
	case err != nil:
		return "", err
	}
	tail := strings.TrimSpace(out.String())
	if len(tail) > 2000 {
		tail = tail[len(tail)-2000:]
	}
	m.log.Info("a script ran", "script", name, "rule", rule, "exit", code, "actions", sent, "output", tail)
	if code != 0 {
		return "", fmt.Errorf("exit status %d (%d action(s) sent): %s", code, len(sent), lastLine(tail))
	}
	return fmt.Sprintf("ran: exit 0, %d action(s) sent", len(sent)), nil
}

func lastLine(s string) string {
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}
