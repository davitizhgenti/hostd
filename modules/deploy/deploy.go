// Package deploy is the deploy module: a service's new versions arrive by
// git push, are built, started next to the running release, checked, and
// switched to with no downtime; a release that fails anywhere is removed
// and the running one keeps serving.
//
// A service is an app (runner process) with a [source] in its file. Each
// release runs as an instance of it, from its own directory
// (~/hostd/releases/<app>/<rev>-<time>), with PORT set to an internal
// port; hostd's proxy owns the public port and sends requests to the live
// release.
package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/internal/clock"
	"github.com/davitizhgenti/hostd/sdk"
)

// Events.
const (
	EventStarted    = "deploy.started"
	EventDone       = "deploy.done"
	EventFailed     = "deploy.failed"
	EventRolledBack = "deploy.rolled_back"
)

// Options configure the deploy module.
type Options struct {
	Root   string // default ~/hostd: git/ and releases/ are there
	Clock  clock.Clock
	Logger *slog.Logger
	// ListenHost is where public ports listen ("" = every interface).
	ListenHost string
	// HealthWait is how long a release may take to become healthy
	// (default 3m).
	HealthWait time.Duration
	// Drain is how long requests on a replaced release may take (default 30s).
	Drain time.Duration
	// AuthorizedKeys is the SSH authorized_keys file push keys go in
	// (default ~/.ssh/authorized_keys), GitShell their forced command
	// (default /usr/local/bin/hostctl git-shell).
	AuthorizedKeys string
	GitShell       string
	// PollTick is how often remote sources are checked for being due
	// (default 10s; each has its own poll interval).
	PollTick time.Duration
}

// Module is the deploy module.
type Module struct {
	opts Options
	log  *slog.Logger

	mu      sync.Mutex
	core    sdk.Core
	proxies map[string]*proxy // by app
	polling map[string]bool   // apps with a deploy started by polling

	stop context.CancelFunc
	done sync.WaitGroup
}

// New returns the deploy module.
func New(opts Options) *Module {
	if opts.Root == "" {
		home, _ := os.UserHomeDir()
		opts.Root = filepath.Join(home, "hostd")
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.HealthWait == 0 {
		opts.HealthWait = 3 * time.Minute
	}
	if opts.Drain == 0 {
		opts.Drain = 30 * time.Second
	}
	if opts.AuthorizedKeys == "" {
		home, _ := os.UserHomeDir()
		opts.AuthorizedKeys = filepath.Join(home, ".ssh", "authorized_keys")
	}
	if opts.GitShell == "" {
		opts.GitShell = "/usr/local/bin/hostctl git-shell"
	}
	if opts.PollTick == 0 {
		opts.PollTick = 10 * time.Second
	}
	return &Module{opts: opts, log: opts.Logger, proxies: map[string]*proxy{}, polling: map[string]bool{}}
}

var appSchema = json.RawMessage(`{"type":"object","properties":{
	"app":{"type":"string","description":"the service (an app with a [source])"},
	"rev":{"type":"string","description":"commit, tag or branch (default: the source's branch)"}},"required":["app"]}`)

func (m *Module) Manifest() sdk.Manifest {
	keys := []sdk.KeyTemplate{"deploy:{app}"}
	return sdk.Manifest{
		Name: "deploy", Version: "0.1.0", Requires: []string{contract.AppsModule},
		Owns: []string{"deploy.*"},
		Actions: append([]sdk.ActionSpec{
			{Type: "deploy.init", Description: "Create the service's git repository: git push to it deploys",
				Schema: appSchema, Keys: keys, Scope: contract.ScopeDeploy, Timeout: sdk.Duration(30 * time.Second),
				Route: &sdk.Route{Method: "POST", Path: "/v1/deploys/{app}/init"}},
			{Type: "deploy.run", Description: "Build a commit and switch to it with no downtime; on any failure the running release stays",
				Schema: appSchema, Keys: keys, Scope: contract.ScopeDeploy, Timeout: sdk.Duration(20 * time.Minute),
				Route: &sdk.Route{Method: "POST", Path: "/v1/deploys/{app}/run"}},
			{Type: "deploy.key", Description: "The service's deploy key (made once): add its public key to the repository, read-only",
				Schema: appSchema, Keys: keys, Scope: contract.ScopeDeploy, Timeout: sdk.Duration(30 * time.Second),
				Route: &sdk.Route{Method: "POST", Path: "/v1/deploys/{app}/key"}},
			{Type: "deploy.rollback", Description: "Switch back to the release before the live one",
				Schema: appSchema, Keys: keys, Scope: contract.ScopeDeploy, Timeout: sdk.Duration(10 * time.Minute),
				Route: &sdk.Route{Method: "POST", Path: "/v1/deploys/{app}/rollback"}},
		}, pushKeyActions()...),
		Events: []sdk.EventSpec{
			{Type: EventStarted, Description: "A deploy started"},
			{Type: EventDone, Description: "A release is live"},
			{Type: EventFailed, Description: "A deploy failed; the running release stays"},
			{Type: EventRolledBack, Description: "A service went back to its previous release"},
		},
		Reads: []sdk.ReadSpec{
			{Name: "deploys", Description: "Every service: its live release and recent ones", Path: "/v1/deploys"},
			{Name: "deploy", Description: "One service's releases", Path: "/v1/deploys/{app}"},
			{Name: "push_keys", Description: "SSH keys that may only git push", Path: "/v1/push-keys"},
		},
	}
}

// service is what the deploy module needs of an app.
type service struct {
	ID     string `json:"id"`
	Deploy *struct {
		Type   string   `json:"type"`
		URL    string   `json:"url"`
		Poll   string   `json:"poll"`
		Build  []string `json:"build"`
		Port   int      `json:"port"`
		Branch string   `json:"branch"`
		Keep   int      `json:"keep"`
	} `json:"deploy"`
}

func (s service) branch() string {
	if s.Deploy.Branch != "" {
		return s.Deploy.Branch
	}
	return "main"
}

// services reads the apps with a [source].
func (m *Module) services(ctx context.Context) (map[string]service, error) {
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	raw, err := core.Read(ctx, contract.AppsModule, "apps", map[string]string{"all": "true"})
	if err != nil {
		return nil, err
	}
	var list struct {
		Apps []service `json:"apps"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	out := map[string]service{}
	for _, a := range list.Apps {
		if a.Deploy != nil {
			out[a.ID] = a
		}
	}
	return out, nil
}

func (m *Module) service(ctx context.Context, app string) (service, error) {
	all, err := m.services(ctx)
	if err != nil {
		return service{}, err
	}
	s, ok := all[app]
	if !ok {
		return service{}, sdk.Errorf(sdk.CodeNotFound, "%q is not a service: its app file needs a [source]", app)
	}
	return s, nil
}

// Start opens the services' public ports, and brings their live releases
// back after a reboot (transient units do not survive one).
func (m *Module) Start(_ context.Context, core sdk.Core) error {
	m.mu.Lock()
	m.core = core
	m.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	m.stop = cancel
	events := core.Subscribe(ctx, "app.catalog.changed")
	m.done.Add(1)
	go func() {
		defer m.done.Done()
		m.sync(ctx)
		for range events {
			m.sync(ctx)
		}
	}()
	m.done.Add(1)
	go func() {
		defer m.done.Done()
		m.pollLoop(ctx)
	}()
	return nil
}

func (m *Module) Stop(ctx context.Context) error {
	if m.stop != nil {
		m.stop()
	}
	m.done.Wait()
	m.mu.Lock()
	proxies := m.proxies
	m.proxies = map[string]*proxy{}
	m.mu.Unlock()
	for _, p := range proxies {
		p.stop(ctx)
	}
	return nil
}

// sync opens a public port for each service, points it at the live
// release, and starts that release again if it is not running.
func (m *Module) sync(ctx context.Context) {
	svcs, err := m.services(ctx)
	if err != nil {
		return
	}
	for id, s := range svcs {
		if s.Deploy.Port == 0 {
			continue
		}
		m.mu.Lock()
		p := m.proxies[id]
		m.mu.Unlock()
		if p == nil {
			np, err := startProxy(id, m.opts.ListenHost+":"+strconv.Itoa(s.Deploy.Port))
			if err != nil {
				m.log.Warn("opening a service's port", "app", id, "err", err)
				continue
			}
			m.mu.Lock()
			m.proxies[id] = np
			m.mu.Unlock()
			p = np
		}
		st, err := m.load(id)
		if err != nil {
			continue
		}
		cur, ok := st.current()
		if !ok {
			continue
		}
		if m.running(ctx, cur.Instance) {
			if up := p.upstream.Load(); up == nil || up.port != cur.Port {
				p.switchTo(cur.Port)
			}
			continue
		}
		// After a reboot: start the live release again.
		if err := m.revive(ctx, s, st, cur); err != nil {
			m.log.Warn("starting a service's release again", "app", id, "err", err)
		}
	}
}

// running reports whether an instance runs.
func (m *Module) running(ctx context.Context, id string) bool {
	if id == "" {
		return false
	}
	st, _ := m.instanceState(ctx, id)
	return st == "running"
}

func (m *Module) instanceState(ctx context.Context, id string) (state, errText string) {
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	raw, err := core.Read(ctx, contract.AppsModule, "instance", map[string]string{"id": id})
	if err != nil {
		return "", err.Error()
	}
	var in struct {
		State string `json:"state"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &in)
	return in.State, in.Error
}

// launch starts a release and waits until it is healthy (running).
func (m *Module) launch(ctx context.Context, app, dir string) (instance string, port int, err error) {
	port, err = freePort()
	if err != nil {
		return "", 0, err
	}
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	res, err := core.Do(ctx, sdk.Action{Type: contract.ActionAppStart, Args: sdk.MustJSON(contract.AppStart{
		ID: app, New: true, Dir: dir, Env: map[string]string{"PORT": strconv.Itoa(port)}})})
	if err != nil {
		return "", 0, fmt.Errorf("starting the release: %w", err)
	}
	var started struct {
		Instance string `json:"instance"`
	}
	_ = json.Unmarshal(res.Data, &started)
	deadline := m.opts.Clock.Now().Add(m.opts.HealthWait)
	for {
		state, errText := m.instanceState(ctx, started.Instance)
		switch state {
		case "running":
			return started.Instance, port, nil
		case "failed", "exited":
			return started.Instance, port, fmt.Errorf("the release ended: %s", errText)
		}
		if m.opts.Clock.Now().After(deadline) {
			return started.Instance, port, fmt.Errorf("the release did not become healthy within %s", m.opts.HealthWait)
		}
		select {
		case <-ctx.Done():
			return started.Instance, port, ctx.Err()
		case <-m.opts.Clock.After(500 * time.Millisecond):
		}
	}
}

func (m *Module) stopInstance(ctx context.Context, id string) {
	if id == "" {
		return
	}
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	if _, err := core.Do(ctx, sdk.Action{Type: contract.ActionInstanceStop, Args: sdk.MustJSON(map[string]string{"id": id})}); err != nil &&
		sdk.CodeOf(err) != sdk.CodeInstanceNotRunning && sdk.CodeOf(err) != sdk.CodeNotFound {
		m.log.Warn("stopping a release", "instance", id, "err", err)
	}
}

// switchTo makes a started release the live one: the proxy sends new
// requests to it, the old release finishes its requests and stops.
func (m *Module) switchTo(ctx context.Context, s service, st *State, r Release) error {
	old, hadOld := st.current()
	m.mu.Lock()
	p := m.proxies[s.ID]
	m.mu.Unlock()
	var prev *upstream
	if p != nil {
		prev = p.switchTo(r.Port)
	}
	st.Current = r.Rev
	st.set(r.Rev, r.At, func(x *Release) { x.Result = "live" })
	if hadOld && old.Instance != r.Instance {
		st.set(old.Rev, old.At, func(x *Release) { x.Result = "stopped" })
	}
	if err := m.save(st); err != nil {
		return err
	}
	if hadOld && old.Instance != r.Instance {
		drain(prev, m.opts.Drain)
		m.stopInstance(ctx, old.Instance)
	}
	return nil
}

func (m *Module) emit(typ string, data any) {
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	if core != nil {
		core.Emit(sdk.Event{Type: typ, Data: sdk.MustJSON(data)})
	}
}

func (m *Module) Validate(context.Context, sdk.Action) error { return nil }

func (m *Module) Handle(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	switch a.Type {
	case "deploy.authorize", "deploy.revoke":
		var args struct{ Name, Key string }
		if err := a.DecodeArgs(&args); err != nil {
			return sdk.Result{}, err
		}
		if a.Type == "deploy.revoke" {
			return sdk.Result{}, m.revoke(args.Name)
		}
		k, err := m.authorize(args.Name, args.Key)
		if err != nil {
			return sdk.Result{}, err
		}
		return sdk.Result{Data: sdk.MustJSON(k)}, nil
	}
	var args struct {
		App string `json:"app"`
		Rev string `json:"rev"`
	}
	if err := a.DecodeArgs(&args); err != nil {
		return sdk.Result{}, err
	}
	s, err := m.service(ctx, args.App)
	if err != nil {
		return sdk.Result{}, err
	}
	switch a.Type {
	case "deploy.key":
		pub, err := m.makeKey(ctx, s.ID)
		if err != nil {
			return sdk.Result{}, err
		}
		return sdk.Result{Data: sdk.MustJSON(map[string]string{"public_key": pub, "path": m.keyPath(s.ID)})}, nil
	case "deploy.init":
		dir, err := m.initRepo(ctx, s.ID, s.branch())
		if err != nil {
			return sdk.Result{}, err
		}
		m.sync(ctx) // the public port opens now, before the first push
		if s.Deploy.Type == "remote" {
			return sdk.Result{Data: sdk.MustJSON(map[string]string{"repo": dir, "branch": s.branch(), "url": s.Deploy.URL})}, nil
		}
		host, _ := os.Hostname()
		return sdk.Result{Data: sdk.MustJSON(map[string]string{"repo": dir, "branch": s.branch(),
			"remote": "ssh://" + os.Getenv("USER") + "@" + host + dir})}, nil
	case "deploy.run":
		return m.run(ctx, s, args.Rev)
	case "deploy.rollback":
		return m.rollback(ctx, s)
	}
	return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "deploy module has no action %q", a.Type)
}

// run deploys rev: export, build, start, health, switch, stop the old.
func (m *Module) run(ctx context.Context, s service, rev string) (sdk.Result, error) {
	m.sync(ctx)
	st, err := m.load(s.ID)
	if err != nil {
		return sdk.Result{}, err
	}
	m.emit(EventStarted, map[string]string{"app": s.ID, "rev": rev})
	fail := func(r Release, err error) (sdk.Result, error) {
		r.Result, r.Error = "failed", err.Error()
		if r.Rev != "" {
			st.record(r)
			m.prune(st, s.Deploy.Keep)
			_ = m.save(st)
		}
		m.emit(EventFailed, r)
		live, ok := st.current()
		still := "nothing was live"
		if ok {
			still = "release " + live.Rev[:min(12, len(live.Rev))] + " keeps serving"
		}
		return sdk.Result{}, sdk.Errorf(sdk.CodeInternal, "deploying %s failed (%s): %v", s.ID, still, err)
	}
	if s.Deploy.Type == "remote" {
		if err := m.fetch(ctx, s); err != nil {
			return fail(Release{}, err)
		}
	}
	sha, dir, err := m.export(ctx, s.ID, rev, s.branch())
	if err != nil {
		return fail(Release{}, err)
	}
	r := Release{Rev: sha, Dir: dir, At: m.opts.Clock.Now().UTC(), Result: "building"}
	if err := m.build(ctx, dir, s.Deploy.Build); err != nil {
		return fail(r, err)
	}
	inst, port, err := m.launch(ctx, s.ID, dir)
	r.Instance, r.Port = inst, port
	if err != nil {
		m.stopInstance(ctx, inst)
		return fail(r, err)
	}
	st.record(r)
	if err := m.switchTo(ctx, s, st, r); err != nil {
		m.stopInstance(ctx, inst)
		return fail(r, err)
	}
	m.prune(st, s.Deploy.Keep)
	_ = m.save(st)
	r.Result = "live"
	m.emit(EventDone, r)
	return sdk.Result{Data: sdk.MustJSON(r)}, nil
}

// rollback switches to the newest release before the live one that was
// live once (stopped, not failed).
func (m *Module) rollback(ctx context.Context, s service) (sdk.Result, error) {
	m.sync(ctx)
	st, err := m.load(s.ID)
	if err != nil {
		return sdk.Result{}, err
	}
	var prev *Release
	for i := range st.Releases {
		r := st.Releases[i]
		if r.Rev != st.Current && r.Result == "stopped" {
			if _, err := os.Stat(r.Dir); err == nil {
				prev = &r
				break
			}
		}
	}
	if prev == nil {
		return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "%s has no earlier release to go back to", s.ID)
	}
	inst, port, err := m.launch(ctx, s.ID, prev.Dir)
	if err != nil {
		m.stopInstance(ctx, inst)
		return sdk.Result{}, sdk.Errorf(sdk.CodeInternal, "rolling %s back failed: %v", s.ID, err)
	}
	r := *prev
	r.Instance, r.Port, r.At = inst, port, m.opts.Clock.Now().UTC()
	st.remove(prev.Rev, prev.At) // one entry per release: it moves to the top
	st.record(r)
	if err := m.switchTo(ctx, s, st, r); err != nil {
		return sdk.Result{}, err
	}
	m.emit(EventRolledBack, r)
	return sdk.Result{Data: sdk.MustJSON(r)}, nil
}

// revive starts the live release again (after a reboot).
func (m *Module) revive(ctx context.Context, s service, st *State, cur Release) error {
	inst, port, err := m.launch(ctx, s.ID, cur.Dir)
	if err != nil {
		m.stopInstance(ctx, inst)
		return err
	}
	st.set(cur.Rev, cur.At, func(x *Release) { x.Instance, x.Port = inst, port })
	m.mu.Lock()
	p := m.proxies[s.ID]
	m.mu.Unlock()
	if p != nil {
		p.switchTo(port)
	}
	return m.save(st)
}

func (m *Module) Read(ctx context.Context, name string, params map[string]string) (any, error) {
	switch name {
	case "deploys":
		svcs, err := m.services(ctx)
		if err != nil {
			return nil, err
		}
		out := []*State{}
		for id := range svcs {
			if st, err := m.load(id); err == nil {
				out = append(out, st)
			}
		}
		return out, nil
	case "deploy":
		if _, err := m.service(ctx, params["app"]); err != nil {
			return nil, err
		}
		return m.load(params["app"])
	case "push_keys":
		return m.pushKeys()
	}
	return nil, sdk.Errorf(sdk.CodeNotFound, "deploy module has no read %q", name)
}

func (m *Module) State(ctx context.Context) (any, error) { return m.Read(ctx, "deploys", nil) }
