// Package apps is the apps module: the catalog of apps (discovered from
// what is installed, merged with hand-written files) and, from M1 step 3.8,
// their running instances.
package apps

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/davitizhgenti/hostd/contract"
)

// Surfaces: a window app shows on the screen; a background app is a
// service with no window.
const (
	SurfaceWindow     = "window"
	SurfaceBackground = "background"
)

// Runner types.
const (
	RunnerExec    = "exec"
	RunnerFlatpak = "flatpak"
	RunnerURL     = "url"
	RunnerDocker  = "docker"
	RunnerCompose = "compose"
	RunnerProcess = "process"
)

var runnerTypes = map[string]string{ // runner type -> default surface
	RunnerExec: SurfaceWindow, RunnerFlatpak: SurfaceWindow, RunnerURL: SurfaceWindow,
	RunnerDocker: SurfaceBackground, RunnerCompose: SurfaceBackground, RunnerProcess: SurfaceBackground,
}

// App is one entry of the catalog: a runner plus a surface, and options.
type App struct {
	ID      string   `json:"id"`
	Aliases []string `json:"aliases,omitempty"` // other IDs that find this app, e.g. the discovered one it extends
	Name    string   `json:"name"`
	Icon    string   `json:"icon,omitempty"`

	// Source is where the app came from: desktop (a .desktop file), or
	// file (a hand-written TOML file). Files lists every file involved.
	Source string   `json:"source"`
	Files  []string `json:"files"`

	Runner   Runner         `json:"runner"`
	Surface  string         `json:"surface"`
	Window   Window         `json:"window"`
	Instance InstancePolicy `json:"instance"`
	Audio    Audio          `json:"audio,omitzero"`
	Requires []string       `json:"requires,omitempty"`
	Hidden   bool           `json:"hidden"`
	// Under is the app this one is listed under rather than on its own: a
	// Steam game under Steam. Menus show it with that app's actions.
	Under   string            `json:"under,omitempty"`
	Match   Match             `json:"match,omitzero"`
	Restart string            `json:"restart,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Health  Health            `json:"health,omitzero"`
	// Actions are extra ways to start the app ("New private window"):
	// from its desktop entry, and from app files.
	Actions []AppAction `json:"actions,omitempty"`
}

// AppAction is an extra way to start an app: app.start with action=<id>
// runs Command as a new instance.
type AppAction struct {
	ID      string   `json:"id" toml:"id"`
	Name    string   `json:"name" toml:"name"`
	Command []string `json:"command" toml:"command"`
}

// Action finds one of the app's actions.
func (a *App) Action(id string) (AppAction, bool) {
	for _, x := range a.Actions {
		if x.ID == id {
			return x, true
		}
	}
	return AppAction{}, false
}

// Runner says how to run the app. Which fields apply depends on Type.
type Runner struct {
	Type    string   `json:"type" toml:"type"`
	Command []string `json:"command,omitempty" toml:"command"` // exec, process: the argv
	URL     string   `json:"url,omitempty" toml:"url"`         // url
	Image   string   `json:"image,omitempty" toml:"image"`     // docker
	Build   string   `json:"build,omitempty" toml:"build"`     // docker: build context
	Ports   []string `json:"ports,omitempty" toml:"ports"`     // docker
	File    string   `json:"file,omitempty" toml:"file"`       // compose
	AppID   string   `json:"app_id,omitempty" toml:"app_id"`   // flatpak ID

	// Handoff is for commands that pass the app to another program and
	// exit, such as "steam steam://rungameid/620": the instance runs while
	// processes with this KEY=value in their environment exist.
	Handoff string `json:"handoff,omitempty" toml:"handoff"`
}

// Window options for window apps.
type Window struct {
	Fullscreen bool   `json:"fullscreen"`
	Wrap       string `json:"wrap,omitempty"` // "gamescope"
	// ShownBy is the app whose window shows this one, when it has none of
	// its own: a game drawn inside Steam's gamescope session.
	ShownBy string `json:"shown_by,omitempty"`
}

// InstancePolicy decides what starting a running app does.
type InstancePolicy struct {
	Policy    string `json:"policy"`     // single | multiple
	IfRunning string `json:"if_running"` // focus | new | restart
}

// Audio options.
type Audio struct {
	Volume *int `json:"volume,omitempty"` // per-app volume applied on start
}

// Match rules find an app's windows when cgroup matching cannot (see
// contract.Match). An app with a handoff and no rules matches by its
// handoff variable.
type Match = contract.Match

// matchRules returns the app's match rules for its instances, or nil.
func (a *App) matchRules() *Match {
	if a.Match.IsZero() {
		return nil
	}
	m := a.Match
	return &m
}

// Health check for background apps: an app with one counts as running
// once it passes, and as failed if it does not within Start.
type Health struct {
	HTTP  string `json:"http,omitempty" toml:"http"`   // a URL that answers 2xx
	TCP   string `json:"tcp,omitempty" toml:"tcp"`     // host:port that accepts connections
	Start string `json:"start,omitempty" toml:"start"` // how long the first pass may take (default 60s)
	Every string `json:"every,omitempty" toml:"every"` // between checks afterwards (default 10s)
}

// Set reports whether the app has a health check.
func (h Health) Set() bool { return h.HTTP != "" || h.TCP != "" }

// durations returns Start and Every, defaulted.
func (h Health) durations() (start, every time.Duration) {
	start, every = 60*time.Second, 10*time.Second
	if d, err := time.ParseDuration(h.Start); err == nil && d > 0 {
		start = d
	}
	if d, err := time.ParseDuration(h.Every); err == nil && d > 0 {
		every = d
	}
	return start, every
}

// Problem is a file the catalog could not use, and why.
type Problem struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

func (p Problem) String() string { return p.File + ": " + p.Error }

var reID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// ValidID reports whether id can name an app. IDs are lowercase so that
// "Firefox" and "firefox" are never two apps; '#' is kept for instances.
func ValidID(id string) bool { return len(id) <= 64 && reID.MatchString(id) }

// applyDefaults fills what a source or file left unset.
func (a *App) applyDefaults() {
	if a.Surface == "" {
		a.Surface = runnerTypes[a.Runner.Type]
		if a.Surface == "" {
			a.Surface = SurfaceWindow
		}
	}
	if a.Instance.Policy == "" {
		a.Instance.Policy = "single"
	}
	if a.Runner.Type == RunnerFlatpak && a.Runner.Handoff == "" && a.Instance.Policy == "single" {
		// Flatpak moves the app out of hostd's unit into a scope of its
		// own: follow the sandbox's processes instead, which all have
		// FLATPAK_ID. Stopping the instance then ends the app itself.
		a.Runner.Handoff = "FLATPAK_ID=" + a.Runner.AppID
	}
	if a.Instance.IfRunning == "" {
		// For a background app, focus means "already running, do nothing".
		a.Instance.IfRunning = "focus"
	}
	if a.Restart == "" {
		if a.Surface == SurfaceBackground {
			a.Restart = "on-failure"
		} else {
			a.Restart = "never"
		}
	}
	if a.Name == "" {
		a.Name = a.ID
	}
	if a.Match.IsZero() {
		switch a.Runner.Type {
		case RunnerFlatpak:
			a.Match = Match{AppID: a.Runner.AppID, Env: "FLATPAK_ID=" + a.Runner.AppID}
		default:
			if a.Runner.Handoff != "" {
				a.Match = Match{Env: a.Runner.Handoff}
			}
		}
	}
}

// validate checks a finished app on its own.
func (a *App) validate() error {
	var errs []string
	bad := func(f string, args ...any) { errs = append(errs, fmt.Sprintf(f, args...)) }
	if !ValidID(a.ID) {
		bad("id %q: use lowercase letters, digits, '.', '-' and '_'", a.ID)
	}
	if _, ok := runnerTypes[a.Runner.Type]; !ok {
		names := make([]string, 0, len(runnerTypes))
		for n := range runnerTypes {
			names = append(names, n)
		}
		sort.Strings(names)
		bad("runner type %q: use one of %s", a.Runner.Type, strings.Join(names, ", "))
	}
	switch a.Runner.Type {
	case RunnerExec, RunnerProcess:
		if len(a.Runner.Command) == 0 {
			bad("runner %s needs a command", a.Runner.Type)
		}
	case RunnerURL:
		if !strings.HasPrefix(a.Runner.URL, "http://") && !strings.HasPrefix(a.Runner.URL, "https://") {
			bad("runner url needs an http:// or https:// url")
		}
	case RunnerDocker:
		if a.Runner.Image == "" && a.Runner.Build == "" {
			bad("runner docker needs an image or a build context")
		}
	case RunnerCompose:
		if a.Runner.File == "" {
			bad("runner compose needs a file")
		}
	case RunnerFlatpak:
		if a.Runner.AppID == "" {
			bad("runner %s needs an app_id", a.Runner.Type)
		}
	}
	if h := a.Runner.Handoff; h != "" {
		if k, _, ok := strings.Cut(h, "="); !ok || k == "" || strings.ContainsAny(h, " \t\n") {
			bad("runner.handoff %q: use KEY=value, a variable the app's real processes have", h)
		}
		if a.Runner.Type != RunnerExec && a.Runner.Type != RunnerFlatpak && a.Runner.Type != RunnerURL {
			bad("runner.handoff applies to exec, flatpak and url apps")
		}
	}
	if a.Surface != SurfaceWindow && a.Surface != SurfaceBackground {
		bad("surface %q: use window or background", a.Surface)
	}
	if a.Instance.Policy != "single" && a.Instance.Policy != "multiple" {
		bad("instance.policy %q: use single or multiple", a.Instance.Policy)
	}
	switch a.Instance.IfRunning {
	case "focus", "new", "restart":
	default:
		bad("instance.if_running %q: use focus, new or restart", a.Instance.IfRunning)
	}
	switch a.Restart {
	case "always", "on-failure", "never":
	default:
		bad("restart %q: use always, on-failure or never", a.Restart)
	}
	if h := a.Health; h.HTTP != "" && !strings.HasPrefix(h.HTTP, "http://") && !strings.HasPrefix(h.HTTP, "https://") {
		bad("health.http %q: an http:// or https:// URL", h.HTTP)
	}
	if h := a.Health; h.TCP != "" {
		if _, port, err := net.SplitHostPort(h.TCP); err != nil || port == "" {
			bad("health.tcp %q: host:port", h.TCP)
		}
	}
	for _, d := range []struct{ name, v string }{{"start", a.Health.Start}, {"every", a.Health.Every}} {
		if d.v != "" {
			if v, err := time.ParseDuration(d.v); err != nil || v <= 0 {
				bad("health.%s %q: a duration such as 30s", d.name, d.v)
			}
		}
	}
	if u := a.Under; u != "" && (!ValidID(u) || u == a.ID) {
		bad("under %q: another app's id", u)
	}
	if s := a.Window.ShownBy; s != "" && (!ValidID(s) || s == a.ID) {
		bad("window.shown_by %q: another app's id", s)
	}
	if a.Window.Wrap != "" && a.Window.Wrap != "gamescope" {
		bad("window.wrap %q: only gamescope is supported", a.Window.Wrap)
	}
	seen := map[string]bool{}
	for _, x := range a.Actions {
		switch {
		case !ValidID(x.ID):
			bad("action id %q: use lowercase letters, digits, '.', '-' and '_'", x.ID)
		case seen[x.ID]:
			bad("action %q is defined twice", x.ID)
		case x.Name == "" || len(x.Command) == 0:
			bad("action %q needs a name and a command", x.ID)
		}
		seen[x.ID] = true
	}
	if len(a.Actions) > 0 && a.Surface != SurfaceWindow {
		bad("actions are for window apps")
	}
	if v := a.Audio.Volume; v != nil && (*v < 0 || *v > 150) {
		bad("audio.volume %d: use 0-150", *v)
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// Catalog is a built, immutable set of apps.
type Catalog struct {
	apps     map[string]*App
	alias    map[string]string // alias -> id
	Problems []Problem
}

// Get finds an app by ID or alias.
func (c *Catalog) Get(id string) (*App, bool) {
	if a, ok := c.apps[id]; ok {
		return a, true
	}
	if real, ok := c.alias[id]; ok {
		return c.apps[real], true
	}
	return nil, false
}

// List returns the apps sorted by ID, without hidden ones unless all.
func (c *Catalog) List(all bool) []*App {
	out := make([]*App, 0, len(c.apps))
	for _, a := range c.apps {
		if all || !a.Hidden {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Len is the number of apps, hidden ones included.
func (c *Catalog) Len() int { return len(c.apps) }

// Build merges discovered apps with app files. Discovered apps come first
// (a later one with the same ID replaces an earlier one, so user
// directories should come last); files then extend them, rename them, or
// add new apps. A file that cannot be used is reported in Problems and
// skipped: one bad file never breaks the rest of the catalog.
func Build(discovered []App, files []AppFile, problems []Problem) *Catalog {
	c := &Catalog{apps: map[string]*App{}, alias: map[string]string{}, Problems: problems}
	for i := range discovered {
		a := discovered[i]
		a.applyDefaults()
		c.apps[a.ID] = &a
		for _, al := range a.Aliases {
			c.alias[al] = a.ID
		}
	}

	// Files in name order, so which of two clashing files wins is stable.
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	fromFile := map[string]string{} // id -> file that defined it
	for _, f := range files {
		app, err := c.applyFile(f, fromFile)
		if err != nil {
			c.Problems = append(c.Problems, Problem{File: f.Path, Error: err.Error()})
			continue
		}
		fromFile[app.ID] = f.Path
	}

	// Requirements must name apps that exist.
	for _, a := range c.List(true) {
		for _, r := range a.Requires {
			if _, ok := c.Get(r); !ok {
				c.Problems = append(c.Problems, Problem{File: a.Files[len(a.Files)-1],
					Error: fmt.Sprintf("app %q requires %q, which is not in the catalog", a.ID, r)})
			}
		}
	}
	sort.Slice(c.Problems, func(i, j int) bool { return c.Problems[i].File < c.Problems[j].File })
	return c
}

func (c *Catalog) applyFile(f AppFile, fromFile map[string]string) (*App, error) {
	id := f.ID
	var app App
	if f.Extends != "" {
		base, ok := c.Get(f.Extends)
		if !ok {
			return nil, fmt.Errorf("extends %q, which is not in the catalog", f.Extends)
		}
		app = *base
		app.Files = append(append([]string(nil), base.Files...), f.Path)
		app.Aliases = append([]string(nil), base.Aliases...)
		if id == "" {
			id = base.ID
		}
		if id != base.ID {
			app.Aliases = append(app.Aliases, base.ID)
		}
	} else {
		if id == "" {
			return nil, fmt.Errorf("id is required (or extends, to change a discovered app)")
		}
		if prev, ok := c.apps[id]; ok && prev.Source != "file" {
			return nil, fmt.Errorf("id %q is already a discovered app; use extends = %q to change it", id, id)
		}
		app = App{Source: "file", Files: []string{f.Path}}
	}
	if prev, ok := fromFile[id]; ok {
		return nil, fmt.Errorf("id %q is already defined by %s", id, prev)
	}
	if real, ok := c.alias[id]; ok && real != id {
		return nil, fmt.Errorf("id %q is already an alias of %q", id, real)
	}
	app.ID = id
	ids := map[string]bool{}
	for _, x := range f.Actions {
		if ids[x.ID] {
			return nil, fmt.Errorf("action %q is defined twice", x.ID)
		}
		ids[x.ID] = true
	}
	f.applyTo(&app)
	app.applyDefaults()
	if err := app.validate(); err != nil {
		return nil, err
	}
	if f.Extends != "" && id != f.Extends {
		base, _ := c.Get(f.Extends)
		delete(c.apps, base.ID) // renamed: the old ID lives on as an alias
		for _, al := range app.Aliases {
			c.alias[al] = id
		}
	}
	c.apps[id] = &app
	return &app, nil
}
