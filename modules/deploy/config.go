package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/davitizhgenti/hostd/sdk"
)

// The config folder can follow a git repository: its apps/ folder becomes
// the app files. Every new commit is checked first (app.check); a valid
// one is applied in one step (the app files' path is a symlink to the
// commit's tree, replaced atomically), an invalid one is rejected with a
// config.rejected event and the previous config stays.

// Config events.
const (
	EventConfigApplied  = "config.applied"
	EventConfigRejected = "config.rejected"
)

// configKey is the deploy key name and repository name of the followed
// config (not a valid app ID, so never a service's).
const configKey = "_config"

// Follow is the followed config repository and its state.
type Follow struct {
	URL      string            `json:"url"`
	Branch   string            `json:"branch"`
	Poll     string            `json:"poll,omitempty"`
	Applied  string            `json:"applied,omitempty"`  // commit in use
	Rejected string            `json:"rejected,omitempty"` // newest commit refused
	Error    string            `json:"error,omitempty"`
	Problems []json.RawMessage `json:"problems,omitempty"`
	At       time.Time         `json:"at,omitzero"` // last change
}

func configActions() []sdk.ActionSpec {
	keys := []sdk.KeyTemplate{"config"}
	return []sdk.ActionSpec{
		{Type: "config.follow", Description: "Take the app files from a git repository's apps/ folder, following its branch",
			Scope: sdk.ScopeAdmin, Keys: keys, Timeout: sdk.Duration(5 * time.Minute),
			Schema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"the repository"},"branch":{"type":"string","description":"default main"},"poll":{"type":"string","description":"how often to look, default 60s"}},"required":["url"],"additionalProperties":false}`)},
		{Type: "config.unfollow", Description: "Stop following; the app files in use stay, as ordinary files",
			Scope: sdk.ScopeAdmin, Keys: keys},
		{Type: "config.sync", Description: "Look at the followed repository now",
			Scope: sdk.ScopeAdmin, Keys: keys, Timeout: sdk.Duration(5 * time.Minute)},
		{Type: "config.key", Description: "The followed repository's deploy key (made once): add its public key to the repository, read-only",
			Scope: sdk.ScopeAdmin, Keys: keys},
	}
}

func (m *Module) followPath() string { return filepath.Join(m.opts.Root, "config", "follow.json") }

func (m *Module) loadFollow() (*Follow, error) {
	b, err := os.ReadFile(m.followPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f Follow
	return &f, json.Unmarshal(b, &f)
}

func (m *Module) saveFollow(f *Follow) error {
	if err := os.MkdirAll(filepath.Dir(m.followPath()), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.followPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.followPath())
}

func (f *Follow) pollEvery() time.Duration {
	if d, err := time.ParseDuration(f.Poll); err == nil && d > 0 {
		return d
	}
	return 60 * time.Second
}

func (m *Module) handleConfig(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	switch a.Type {
	case "config.follow":
		var args struct{ URL, Branch, Poll string }
		if err := a.DecodeArgs(&args); err != nil {
			return sdk.Result{}, err
		}
		if args.URL == "" || strings.HasPrefix(args.URL, "-") {
			return sdk.Result{}, sdk.Errorf(sdk.CodeInvalidArgs, "url %q: a git repository", args.URL)
		}
		if args.Branch == "" {
			args.Branch = "main"
		}
		if args.Poll != "" {
			if d, err := time.ParseDuration(args.Poll); err != nil || d < 10*time.Second {
				return sdk.Result{}, sdk.Errorf(sdk.CodeInvalidArgs, "poll %q: a duration of at least 10s", args.Poll)
			}
		}
		f := &Follow{URL: args.URL, Branch: args.Branch, Poll: args.Poll}
		if old, _ := m.loadFollow(); old != nil && old.URL == f.URL && old.Branch == f.Branch {
			f.Applied = old.Applied
		}
		if err := m.saveFollow(f); err != nil {
			return sdk.Result{}, err
		}
		return m.configSync(ctx)
	case "config.sync":
		return m.configSync(ctx)
	case "config.unfollow":
		return sdk.Result{}, m.unfollow()
	case "config.key":
		pub, err := m.makeKey(ctx, configKey)
		if err != nil {
			return sdk.Result{}, err
		}
		return sdk.Result{Data: sdk.MustJSON(map[string]string{"public_key": pub, "path": m.keyPath(configKey)})}, nil
	}
	return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "deploy module has no action %q", a.Type)
}

func (m *Module) configHead(ctx context.Context, f *Follow) (string, error) {
	out, err := m.gitWith(ctx, m.gitEnv(configKey), "ls-remote", f.URL, "refs/heads/"+f.Branch)
	if err != nil {
		return "", err
	}
	sha, _, _ := strings.Cut(out, "\t")
	if sha == "" {
		return "", fmt.Errorf("%s has no branch %s", f.URL, f.Branch)
	}
	return sha, nil
}

// pollConfig syncs the followed config when its branch has a commit that
// was neither applied nor rejected.
func (m *Module) pollConfig(ctx context.Context) {
	f, err := m.loadFollow()
	if err != nil || f == nil {
		return
	}
	head, err := m.configHead(ctx, f)
	if err != nil {
		m.log.Warn("looking at the followed config", "url", f.URL, "err", err)
		return
	}
	if head == f.Applied || head == f.Rejected {
		return
	}
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	if _, err := core.Do(ctx, sdk.Action{Type: "config.sync"}); err != nil {
		m.log.Warn("the followed config's new commit was not applied", "rev", head, "err", err)
	}
}

// configSync fetches the branch, checks its apps/ folder and applies it.
func (m *Module) configSync(ctx context.Context) (sdk.Result, error) {
	f, err := m.loadFollow()
	if err != nil {
		return sdk.Result{}, err
	}
	if f == nil {
		return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "no config is followed: hostctl config follow <repository>")
	}
	repo := filepath.Join(m.opts.Root, "git", configKey+".git")
	if _, err := os.Stat(filepath.Join(repo, "HEAD")); err != nil {
		if _, err := m.git(ctx, "init", "--quiet", "--bare", repo); err != nil {
			return sdk.Result{}, err
		}
	}
	ref := "+refs/heads/" + f.Branch + ":refs/heads/" + f.Branch
	if _, err := m.gitWith(ctx, m.gitEnv(configKey), "--git-dir", repo, "fetch", "--quiet", "--no-tags", f.URL, ref); err != nil {
		return sdk.Result{}, fmt.Errorf("fetching %s: %w", f.URL, err)
	}
	sha, err := m.git(ctx, "--git-dir", repo, "rev-parse", "--verify", f.Branch+"^{commit}")
	if err != nil {
		return sdk.Result{}, err
	}
	dir := filepath.Join(m.opts.Root, "config", sha[:12])
	if sha == f.Applied {
		if _, err := os.Stat(dir); err == nil {
			return sdk.Result{Data: sdk.MustJSON(f)}, nil // in use already
		}
	}
	_ = os.RemoveAll(dir)
	if err := m.exportTree(ctx, repo, sha, dir); err != nil {
		return sdk.Result{}, err
	}
	reject := func(msg string, problems []json.RawMessage) (sdk.Result, error) {
		_ = os.RemoveAll(dir)
		f.Rejected, f.Error, f.Problems, f.At = sha, msg, problems, m.opts.Clock.Now().UTC()
		_ = m.saveFollow(f)
		m.emit(EventConfigRejected, f)
		return sdk.Result{}, sdk.Errorf(sdk.CodePreconditionFailed, "config %s rejected, the previous config stays: %s", sha[:12], msg)
	}
	apps := filepath.Join(dir, "apps")
	if fi, err := os.Stat(apps); err != nil || !fi.IsDir() {
		return reject("the repository has no apps/ folder", nil)
	}
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	res, err := core.Do(ctx, sdk.Action{Type: "app.check", Args: sdk.MustJSON(map[string]string{"dir": apps})})
	if err != nil {
		return sdk.Result{}, err
	}
	var check struct {
		Problems []json.RawMessage `json:"problems"`
	}
	_ = json.Unmarshal(res.Data, &check)
	if len(check.Problems) > 0 {
		return reject(fmt.Sprintf("%d app file(s) have problems", len(check.Problems)), check.Problems)
	}
	if err := m.pointAppsAt(apps); err != nil {
		return sdk.Result{}, err
	}
	if _, err := core.Do(ctx, sdk.Action{Type: "app.rescan"}); err != nil {
		m.log.Warn("reading the new config", "err", err)
	}
	f.Applied, f.Rejected, f.Error, f.Problems, f.At = sha, "", "", nil, m.opts.Clock.Now().UTC()
	if err := m.saveFollow(f); err != nil {
		return sdk.Result{}, err
	}
	m.pruneConfigs(sha[:12])
	m.emit(EventConfigApplied, f)
	return sdk.Result{Data: sdk.MustJSON(f)}, nil
}

// pointAppsAt makes the app files' path a symlink to dir, in one step. A
// real folder there (hand-written app files) is kept beside it.
func (m *Module) pointAppsAt(dir string) error {
	path := m.opts.AppsDir
	if path == "" {
		return sdk.Errorf(sdk.CodeModuleUnavailable, "no app files folder configured")
	}
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink == 0 {
		backup := path + ".before-follow"
		if _, err := os.Lstat(backup); err == nil {
			backup += "-" + m.opts.Clock.Now().UTC().Format("20060102T150405")
		}
		if err := os.Rename(path, backup); err != nil {
			return err
		}
		m.log.Info("the app files folder now follows a repository; the old one is kept", "kept", backup)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".new"
	_ = os.Remove(tmp)
	if err := os.Symlink(dir, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// unfollow stops following: the app files in use become an ordinary
// folder again.
func (m *Module) unfollow() error {
	f, err := m.loadFollow()
	if err != nil {
		return err
	}
	if f == nil {
		return sdk.Errorf(sdk.CodeNotFound, "no config is followed")
	}
	path := m.opts.AppsDir
	if target, err := os.Readlink(path); err == nil {
		tmp := path + ".copy"
		_ = os.RemoveAll(tmp)
		if err := os.CopyFS(tmp, os.DirFS(target)); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
	}
	return os.Remove(m.followPath())
}

// pruneConfigs keeps the config in use and the two before it.
func (m *Module) pruneConfigs(current string) {
	root := filepath.Join(m.opts.Root, "config")
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	type dir struct {
		name string
		at   time.Time
	}
	var dirs []dir
	for _, e := range entries {
		if !e.IsDir() || e.Name() == current {
			continue
		}
		if info, err := e.Info(); err == nil {
			dirs = append(dirs, dir{e.Name(), info.ModTime()})
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].at.After(dirs[j].at) })
	for i, d := range dirs {
		if i >= 2 {
			_ = os.RemoveAll(filepath.Join(root, d.name))
		}
	}
}

// readFollow is GET /v1/config/follow.
func (m *Module) readFollow() (any, error) {
	f, err := m.loadFollow()
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, sdk.Errorf(sdk.CodeNotFound, "no config is followed")
	}
	return f, nil
}
