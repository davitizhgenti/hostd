package automation

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"

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

// scriptSettings are a script's scopes and timeout.
type scriptSettings struct {
	Scopes  []string `toml:"scopes"`
	Timeout string   `toml:"timeout"`
	timeout time.Duration
}

// scriptPath is a rule's script, which must be inside the scripts folder.
func (m *Module) scriptPath(name string) (string, error) {
	dir := m.opts.ScriptsDir
	p := filepath.Clean(filepath.Join(dir, name))
	if filepath.IsAbs(name) || !strings.HasPrefix(p, filepath.Clean(dir)+string(os.PathSeparator)) {
		return "", fmt.Errorf("script %q: a file in %s", name, dir)
	}
	fi, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("script %q: %w", name, err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("script %s is not an executable file (chmod +x)", p)
	}
	return p, nil
}

// readSettings reads the sidecar <script>.toml, else the header comment.
func readSettings(p string) (scriptSettings, error) {
	var s scriptSettings
	if b, err := os.ReadFile(p + ".toml"); err == nil {
		md, err := toml.Decode(string(b), &s)
		if err != nil {
			return s, fmt.Errorf("%s.toml: %w", p, err)
		}
		if und := md.Undecoded(); len(und) > 0 {
			return s, fmt.Errorf("%s.toml: unknown key %s", p, und[0])
		}
	} else if f, err := os.Open(p); err == nil {
		sc := bufio.NewScanner(f)
		for n := 0; n < 20 && sc.Scan(); n++ {
			if rest, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "# hostd-scopes:"); ok {
				s.Scopes = strings.Fields(rest)
				break
			}
		}
		f.Close()
	}
	s.timeout = 60 * time.Second
	if s.Timeout != "" {
		d, err := time.ParseDuration(s.Timeout)
		if err != nil || d <= 0 {
			return s, fmt.Errorf("%s.toml: timeout %q: a duration such as 60s", p, s.Timeout)
		}
		s.timeout = d
	}
	if slices.Contains(s.Scopes, sdk.ScopeAdmin) {
		return s, fmt.Errorf("%s: scripts cannot have scope admin", p)
	}
	return s, nil
}

// runScript runs a rule's script and returns how it went.
func (m *Module) runScript(ctx context.Context, r Rule, ev sdk.Event) string {
	result, err := m.script(ctx, r, ev)
	if err != nil {
		m.log.Warn("a rule's script failed", "rule", r.Name, "script", r.Do.Script, "err", err)
		return "failed: " + err.Error()
	}
	return result
}

func (m *Module) script(ctx context.Context, r Rule, ev sdk.Event) (string, error) {
	p, err := m.scriptPath(r.Do.Script)
	if err != nil {
		return "", err
	}
	set, err := readSettings(p)
	if err != nil {
		return "", err
	}
	if m.opts.Tokens == nil {
		return "", errors.New("scripts cannot run here (no token service)")
	}
	m.mu.Lock()
	m.scriptRuns++
	name := fmt.Sprintf("script %s #%d", r.Name, m.scriptRuns)
	m.mu.Unlock()
	secret, revoke, err := m.opts.Tokens.Mint(ctx, name, set.Scopes, set.timeout+30*time.Second)
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
			if e.Source != nil && e.Source.Name == name && e.Action != "" {
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
	cmd := exec.CommandContext(run, p)
	cmd.Dir = m.opts.ScriptsDir
	cmd.Env = append(os.Environ(), "HOSTD_URL="+m.opts.URL, "HOSTD_TOKEN="+secret,
		"HOSTD_EVENT="+string(sdk.MustJSON(ev)), "HOSTD_RULE="+r.Name)
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
		m.log.Warn("a rule's script ran out of time and was stopped", "rule", r.Name, "timeout", set.timeout, "actions", sent)
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
	m.log.Info("a rule's script ran", "rule", r.Name, "script", r.Do.Script, "exit", code, "actions", sent, "output", tail)
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
