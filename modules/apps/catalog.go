// Package apps is the apps module: the catalog of apps (discovered from
// what is installed, merged with hand-written files) and their running
// instances. It runs them through Backends, one per runner type, which live
// in packages of their own (runner/units, runner/podman) and share the
// types in spec; the daemon chooses which runners a machine has.
package apps

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"
)

// matchRules returns the app's match rules for its instances, or nil.
func matchRules(a *App) *Match {
	if a.Match.IsZero() {
		return nil
	}
	m := a.Match
	return &m
}

// durations returns Start and Every, defaulted.
func healthDurations(h Health) (start, every time.Duration) {
	start, every = 60*time.Second, 10*time.Second
	if d, err := time.ParseDuration(h.Start); err == nil && d > 0 {
		start = d
	}
	if d, err := time.ParseDuration(h.Every); err == nil && d > 0 {
		every = d
	}
	return start, every
}

var runnerTypes = map[string]string{ // runner type -> default surface
	RunnerExec: SurfaceWindow, RunnerFlatpak: SurfaceWindow, RunnerURL: SurfaceWindow,
	RunnerDocker: SurfaceBackground, RunnerCompose: SurfaceBackground, RunnerProcess: SurfaceBackground,
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
func applyDefaults(a *App) {
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
func validate(a *App) error {
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
	if d := a.Deploy; d != nil {
		if strings.HasPrefix(d.URL, "-") {
			bad("source.url %q: a git repository", d.URL)
		}
		switch d.Type {
		case "push":
			if d.URL != "" || d.Poll != "" || d.Secret != "" {
				bad("source: url, poll and secret are for types remote and webhook")
			}
		case "webhook":
			if d.URL == "" || d.Secret == "" {
				bad("source: type webhook needs url (to fetch from) and secret (the webhook's, set with hostctl secret set)")
			}
			if d.Poll != "" {
				bad("source: poll is for type remote")
			}
		case "image":
			if d.URL != "" || d.Secret != "" {
				bad("source: url and secret are not for type image")
			}
			if d.Poll != "" {
				if v, err := time.ParseDuration(d.Poll); err != nil || v < 10*time.Second {
					bad("source.poll %q: a duration of at least 10s", d.Poll)
				}
			}
		case "remote":
			if d.URL == "" {
				bad("source: type remote needs url")
			}
			if d.Secret != "" {
				bad("source: secret is for type webhook")
			}
			if d.Poll != "" {
				if v, err := time.ParseDuration(d.Poll); err != nil || v < 10*time.Second {
					bad("source.poll %q: a duration of at least 10s", d.Poll)
				}
			}
		default:
			bad("source.type %q: push, remote, webhook or image", d.Type)
		}
		if d.Port < 0 || d.Port > 65535 {
			bad("source.port %d: 1-65535", d.Port)
		}
		if d.Type == "image" {
			if a.Runner.Type != RunnerDocker {
				bad("source: type image needs runner type docker")
			}
			if d.Image == "" || d.ContainerPort < 1 || d.ContainerPort > 65535 {
				bad("source: type image needs image and container_port (the port the container listens on)")
			}
		} else {
			if a.Runner.Type != RunnerProcess {
				bad("source: deploys need runner type process")
			}
			if d.Image != "" || d.Tag != "" || d.ContainerPort != 0 || d.Auth != "" {
				bad("source: image, tag, container_port and auth are for type image")
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
		applyDefaults(&a)
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
	applyDefaults(&app)
	if err := validate(&app); err != nil {
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
