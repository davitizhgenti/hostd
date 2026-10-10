package deploy

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/davitizhgenti/hostd/sdk"
)

// A remote source is a repository hostd looks at (git ls-remote) every
// poll interval; a new commit on the branch is deployed. Private
// repositories are read with a per-service deploy key (deploy.key), which
// needs only read access.

func (m *Module) keyPath(app string) string {
	return filepath.Join(m.opts.Root, "keys", app)
}

// makeKey creates the service's deploy key once and returns its public
// half.
func (m *Module) makeKey(ctx context.Context, app string) (string, error) {
	path := m.keyPath(app)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", err
		}
		host, _ := os.Hostname()
		cmd := exec.CommandContext(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "hostd deploy key: "+app+"@"+host, "-f", path)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("ssh-keygen: %w: %s", err, strings.TrimSpace(string(out)))
		}
	} else if err != nil {
		return "", err
	}
	pub, err := os.ReadFile(path + ".pub")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(pub)), nil
}

// gitEnv is the environment for talking to a service's remote: its deploy
// key if it has one, and hostd's own known hosts (a host is trusted on
// first use). Never a prompt.
func (m *Module) gitEnv(app string) []string {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if _, err := os.Stat(m.keyPath(app)); err == nil {
		known := filepath.Join(m.opts.Root, "keys", "known_hosts")
		env = append(env, "GIT_SSH_COMMAND=ssh -i "+m.keyPath(app)+" -o IdentitiesOnly=yes -o BatchMode=yes"+
			" -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="+known)
	}
	return env
}

// fetch brings the remote's branch into the service's repository.
func (m *Module) fetch(ctx context.Context, s service) error {
	if _, err := m.initRepo(ctx, s.ID, s.branch()); err != nil {
		return err
	}
	ref := "+refs/heads/" + s.branch() + ":refs/heads/" + s.branch()
	if _, err := m.gitWith(ctx, m.gitEnv(s.ID), "--git-dir", m.repo(s.ID), "fetch", "--quiet", "--no-tags", s.Deploy.URL, ref); err != nil {
		return fmt.Errorf("fetching %s: %w", s.Deploy.URL, err)
	}
	return nil
}

// head is the commit at the tip of the remote's branch.
func (m *Module) head(ctx context.Context, s service) (string, error) {
	out, err := m.gitWith(ctx, m.gitEnv(s.ID), "ls-remote", s.Deploy.URL, "refs/heads/"+s.branch())
	if err != nil {
		return "", err
	}
	sha, _, _ := strings.Cut(out, "\t")
	if sha == "" {
		return "", fmt.Errorf("%s has no branch %s", s.Deploy.URL, s.branch())
	}
	return sha, nil
}

func (s service) pollEvery() time.Duration {
	if d, err := time.ParseDuration(s.Deploy.Poll); err == nil && d > 0 {
		return d
	}
	return 60 * time.Second
}

// pollLoop looks at every remote source when it is due.
func (m *Module) pollLoop(ctx context.Context) {
	last := map[string]time.Time{}
	var configAt time.Time
	for {
		if f, err := m.loadFollow(); err == nil && f != nil && m.opts.Clock.Now().Sub(configAt) >= f.pollEvery() {
			configAt = m.opts.Clock.Now()
			m.pollConfig(ctx)
		}
		if svcs, err := m.services(ctx); err == nil {
			now := m.opts.Clock.Now()
			for id, s := range svcs {
				if s.Deploy.Type != "remote" || now.Sub(last[id]) < s.pollEvery() {
					continue
				}
				last[id] = now
				m.poll(ctx, s)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-m.opts.Clock.After(m.opts.PollTick):
		}
	}
}

// poll deploys the remote's head if it is new: a commit already in the
// history (live, replaced, rolled back from or failed) is not deployed
// again by polling, only by hand.
func (m *Module) poll(ctx context.Context, s service) {
	sha, err := m.head(ctx, s)
	if err != nil {
		m.log.Warn("looking at a service's remote", "app", s.ID, "err", err)
		return
	}
	if m.inHistory(s.ID, sha) {
		return
	}
	m.deployInBackground(s, sha)
}

// inHistory reports whether rev was deployed before (live, replaced,
// rolled back from or failed): automatic sources do not deploy it again.
func (m *Module) inHistory(app, rev string) bool {
	st, err := m.load(app)
	if err != nil {
		return true
	}
	for _, r := range st.Releases {
		if r.Rev == rev {
			return true
		}
	}
	return false
}

// deployInBackground starts deploy.run for rev unless one started this
// way still runs; a deploy takes minutes.
func (m *Module) deployInBackground(s service, rev string) bool {
	m.mu.Lock()
	if m.polling[s.ID] {
		m.mu.Unlock()
		return false
	}
	m.polling[s.ID] = true
	core, ctx := m.core, m.life
	m.mu.Unlock()
	m.done.Add(1)
	go func() {
		defer m.done.Done()
		defer func() {
			m.mu.Lock()
			delete(m.polling, s.ID)
			m.mu.Unlock()
		}()
		if _, err := core.Do(ctx, sdk.Action{Type: "deploy.run", Args: sdk.MustJSON(map[string]string{"app": s.ID, "rev": rev})}); err != nil {
			m.log.Warn("deploying a new commit", "app", s.ID, "rev", rev, "err", err)
		}
	}()
	return true
}
