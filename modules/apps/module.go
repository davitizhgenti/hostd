package apps

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/internal/clock"
	"github.com/davitizhgenti/hostd/sdk"
)

// Events the module emits.
const (
	EventCatalogChanged = "app.catalog.changed"
	EventFileRejected   = "app.file.rejected"
)

// Options configure the apps module.
type Options struct {
	DesktopDirs []string // .desktop directories, lowest priority first
	AppsDir     string   // hand-written app files
	Desktop     string   // for OnlyShowIn/NotShowIn; default "sway"
	LookPath    func(string) (string, error)

	// Backends run instances, by runner type (exec, docker...). Apps
	// whose runner has no backend are in the catalog but cannot start.
	Backends map[string]Backend

	Clock    clock.Clock
	Logger   *slog.Logger
	Debounce time.Duration // wait after the last file change before rescanning (default 500ms)
	NoWatch  bool          // do not watch the directories (tests)
	// Journal reads a unit's last lines (default: journalctl --user).
	Journal func(ctx context.Context, unit string, n int) ([]string, error)
}

// DefaultAppsDir is $XDG_CONFIG_HOME/hostd/apps.
func DefaultAppsDir() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "hostd", "apps")
}

// Module is the apps module.
type Module struct {
	opts Options
	log  *slog.Logger

	mu       sync.Mutex
	core     sdk.Core
	cat      *Catalog
	reported map[string]bool // problems already announced
	timer    clock.Timer
	changes  int // file changes seen by the watcher (for tests)

	watcher *fsnotify.Watcher
	done    chan struct{}

	instances   map[string]*Instance // live instances by ID
	ended       []Instance           // recently ended, newest first
	stopWatch   context.CancelFunc
	watchersRun sync.WaitGroup  // watchers and background hand-offs
	life        context.Context // ends when the module stops
}

// New returns the apps module.
func New(opts Options) *Module {
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Debounce == 0 {
		opts.Debounce = 500 * time.Millisecond
	}
	return &Module{opts: opts, log: opts.Logger, cat: Build(nil, nil, nil), reported: map[string]bool{},
		instances: map[string]*Instance{}}
}

func (m *Module) Manifest() sdk.Manifest {
	return sdk.Manifest{
		Name: contract.AppsModule, Version: "0.1.0",
		Owns: []string{"app.*", "instance.*"},
		Scopes: []sdk.ScopeSpec{
			{Name: contract.ScopeApps, Description: "Start and stop apps, focus and close their windows"},
			// Used by app.start and the display module's window.focus.
			{Name: contract.ScopeFront, Description: "Bring an app to the front even while someone is using the screen"},
			{Name: contract.ScopeDeploy, Description: "Start an app as a release of a service: its own directory and environment (deploys)"},
		},
		Actions: append([]sdk.ActionSpec{
			{Type: "app.rescan", Description: "Read installed apps and app files again", Scope: contract.ScopeApps,
				Route: &sdk.Route{Method: "POST", Path: "/v1/apps/rescan"}},
		}, instanceActions()...),
		Events: append([]sdk.EventSpec{
			{Type: EventCatalogChanged, Description: "Apps were added, removed or changed"},
			{Type: EventFileRejected, Description: "An app file could not be used"},
		}, instanceEvents()...),
		Reads: append([]sdk.ReadSpec{
			{Name: "apps", Description: "The catalog (?all=true includes hidden apps)", Path: "/v1/apps"},
			{Name: "app", Description: "One app, by ID or alias", Path: "/v1/apps/{id}"},
		}, instanceReads()...),
	}
}

// Start builds the catalog and starts watching its directories.
func (m *Module) Start(_ context.Context, core sdk.Core) error {
	m.mu.Lock()
	m.core = core
	m.mu.Unlock()
	if m.opts.AppsDir != "" {
		if err := os.MkdirAll(m.opts.AppsDir, 0o755); err != nil {
			return err
		}
	}
	m.rescan()

	// Instances first: find what already runs, then follow it.
	m.adopt(context.Background())
	wctx, cancel := context.WithCancel(context.Background())
	m.stopWatch = cancel
	m.life = wctx
	for name, b := range m.opts.Backends {
		m.watchersRun.Add(1)
		go func() { defer m.watchersRun.Done(); m.watchBackend(wctx, name, b) }()
	}

	if m.opts.NoWatch {
		return nil
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	for _, d := range append(append([]string(nil), m.opts.DesktopDirs...), m.opts.AppsDir) {
		if d == "" {
			continue
		}
		if err := w.Add(d); err != nil && !errors.Is(err, os.ErrNotExist) {
			m.log.Warn("cannot watch app directory; use app.rescan after changes", "dir", d, "err", err)
		}
	}
	m.watcher = w
	m.done = make(chan struct{})
	go m.watch()
	return nil
}

func (m *Module) Stop(context.Context) error {
	if m.stopWatch != nil {
		m.stopWatch()
		m.watchersRun.Wait()
	}
	if m.watcher != nil {
		_ = m.watcher.Close()
		<-m.done
	}
	m.mu.Lock()
	if m.timer != nil {
		m.timer.Stop()
	}
	m.mu.Unlock()
	return nil
}

// watch rescans once file changes have settled for the debounce time, so
// a package installing fifty .desktop files causes one rescan, not fifty.
func (m *Module) watch() {
	defer close(m.done)
	for {
		select {
		case _, ok := <-m.watcher.Events:
			if !ok {
				return
			}
			m.mu.Lock()
			m.changes++
			if m.timer == nil {
				m.timer = m.opts.Clock.AfterFunc(m.opts.Debounce, m.rescan)
			} else {
				m.timer.Reset(m.opts.Debounce)
			}
			m.mu.Unlock()
		case err, ok := <-m.watcher.Errors:
			if !ok {
				return
			}
			m.log.Warn("watching app directories", "err", err)
		}
	}
}

// rescan rebuilds the catalog and announces what changed.
func (m *Module) rescan() {
	src := DesktopSource{Dirs: m.opts.DesktopDirs, Desktop: m.opts.Desktop, LookPath: m.opts.LookPath}
	discovered, problems := src.Scan()
	files, fileProblems := LoadAppFiles(m.opts.AppsDir)
	cat := Build(discovered, files, append(problems, fileProblems...))

	m.mu.Lock()
	old := m.cat
	m.cat = cat
	core := m.core
	var fresh []Problem
	seen := map[string]bool{}
	for _, p := range cat.Problems {
		seen[p.String()] = true
		if !m.reported[p.String()] {
			fresh = append(fresh, p)
		}
	}
	m.reported = seen
	m.mu.Unlock()

	for _, p := range fresh {
		m.log.Warn("app file not used", "file", p.File, "err", p.Error)
		if core != nil {
			core.Emit(sdk.Event{Type: EventFileRejected, Data: sdk.MustJSON(p)})
		}
	}
	added, removed, changed := diff(old, cat)
	if core != nil && len(added)+len(removed)+len(changed) > 0 {
		core.Emit(sdk.Event{Type: EventCatalogChanged, Data: sdk.MustJSON(map[string]any{
			"apps": cat.Len(), "added": added, "removed": removed, "changed": changed,
		})})
	}
}

func diff(old, cur *Catalog) (added, removed, changed []string) {
	added, removed, changed = []string{}, []string{}, []string{}
	for id, a := range cur.apps {
		prev, ok := old.apps[id]
		switch {
		case !ok:
			added = append(added, id)
		case !reflect.DeepEqual(prev, a):
			changed = append(changed, id)
		}
	}
	for id := range old.apps {
		if _, ok := cur.apps[id]; !ok {
			removed = append(removed, id)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)
	return added, removed, changed
}

func (m *Module) Validate(_ context.Context, a sdk.Action) error {
	switch a.Type {
	case contract.ActionAppStart:
		return m.validateStart(a)
	case contract.ActionInstanceStop, contract.ActionInstanceClosing:
		return m.validateStop(a)
	}
	return nil
}

func (m *Module) Handle(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	switch a.Type {
	case contract.ActionAppStart:
		return m.handleStart(ctx, a)
	case contract.ActionInstanceStop:
		return m.handleStop(ctx, a)
	case contract.ActionInstanceClosing:
		return m.handleClosing(a)
	}
	if a.Type == "app.rescan" {
		m.rescan()
		cat := m.catalog()
		return sdk.Result{Data: sdk.MustJSON(map[string]int{"apps": cat.Len(), "problems": len(cat.Problems)})}, nil
	}
	return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "apps module has no action %q", a.Type)
}

func (m *Module) catalog() *Catalog {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cat
}

// Catalog returns the current catalog (for the instance code and tests).
func (m *Module) Catalog() *Catalog { return m.catalog() }

// AppList is the answer of GET /v1/apps.
type AppList struct {
	Apps     []*App    `json:"apps"`
	Problems []Problem `json:"problems"`
}

func (m *Module) Read(ctx context.Context, name string, params map[string]string) (any, error) {
	cat := m.catalog()
	switch name {
	case "apps":
		all := params["all"] == "true" || params["all"] == "1"
		problems := cat.Problems
		if problems == nil {
			problems = []Problem{}
		}
		return AppList{Apps: cat.List(all), Problems: problems}, nil
	case "app":
		if a, ok := cat.Get(params["id"]); ok {
			return a, nil
		}
		return nil, sdk.Errorf(sdk.CodeNotFound, "no app %q", params["id"])
	case contract.ReadInstances:
		return m.readInstances(params), nil
	case "instance":
		return m.readInstance(params["id"])
	case "logs":
		return m.readLogs(ctx, params)
	}
	return nil, sdk.Errorf(sdk.CodeNotFound, "apps module has no read %q", name)
}

// State is the whole catalog, hidden apps included, and the instances.
func (m *Module) State(ctx context.Context) (any, error) {
	apps, _ := m.Read(ctx, "apps", map[string]string{"all": "true"})
	return map[string]any{"catalog": apps, "instances": m.readInstances(map[string]string{"all": "true"})}, nil
}
