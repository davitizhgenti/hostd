// Package catalog builds the apps module's catalog: apps discovered from
// installed desktop entries, merged with hand-written app files, given
// their defaults and checked. It is pure: files in, apps and the problems
// found out; the module holds the catalog and runs what is in it.
package catalog

import (
	"fmt"
	"net"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/davitizhgenti/hostd/modules/apps/spec"
)

var runnerTypes = map[string]string{ // runner type -> default surface
	spec.RunnerExec: spec.SurfaceWindow, spec.RunnerFlatpak: spec.SurfaceWindow, spec.RunnerURL: spec.SurfaceWindow,
	spec.RunnerDocker: spec.SurfaceBackground, spec.RunnerCompose: spec.SurfaceBackground, spec.RunnerProcess: spec.SurfaceBackground,
}

// Problem is a file the catalog could not use, and why.
type Problem struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

func (p Problem) String() string { return p.File + ": " + p.Error }

var reID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

var reArg = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// usesArg reports whether {name} is in the app's command or URL.
func usesArg(a *spec.App, name string) bool {
	ph := "{" + name + "}"
	if strings.Contains(a.Runner.URL, ph) {
		return true
	}
	for _, c := range a.Runner.Command {
		if strings.Contains(c, ph) {
			return true
		}
	}
	return false
}

// sampleArgs fills s's placeholders with a value of each argument's kind,
// to check what s will be once app.start fills them in.
func sampleArgs(a *spec.App, s string) string {
	samples := map[string]string{spec.ArgURL: "http://example.invalid/", spec.ArgPath: "/x", spec.ArgText: "x"}
	for name, kind := range a.Args {
		s = strings.ReplaceAll(s, "{"+name+"}", samples[kind])
	}
	return s
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Reserved reports whether an ID belongs to app.run's instances (run,
// run-<name>), which no app file may take.
func Reserved(id string) bool { return id == "run" || strings.HasPrefix(id, "run-") }

// ValidID reports whether id can name an app. IDs are lowercase so that
// "Firefox" and "firefox" are never two apps; '#' is kept for instances.
func ValidID(id string) bool { return len(id) <= 64 && reID.MatchString(id) }

// Defaults fills what a source or file left unset: the surface, restart
// policy, instance policy and Flatpak handoff that follow from the runner.
func Defaults(a *spec.App) {
	if a.Surface == "" {
		a.Surface = runnerTypes[a.Runner.Type]
		if a.Surface == "" {
			a.Surface = spec.SurfaceWindow
		}
	}
	if a.Instance.Policy == "" {
		a.Instance.Policy = "single"
	}
	if a.Runner.Type == spec.RunnerFlatpak && a.Runner.Handoff == "" && a.Instance.Policy == "single" {
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
		if a.Surface == spec.SurfaceBackground {
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
		case spec.RunnerFlatpak:
			a.Match = spec.Match{AppID: a.Runner.AppID, Env: "FLATPAK_ID=" + a.Runner.AppID}
		default:
			if a.Runner.Handoff != "" {
				a.Match = spec.Match{Env: a.Runner.Handoff}
			}
		}
	}
}

// validate checks a finished app on its own.
func validate(a *spec.App) error {
	var errs []string
	bad := func(f string, args ...any) { errs = append(errs, fmt.Sprintf(f, args...)) }
	if !ValidID(a.ID) {
		bad("id %q: use lowercase letters, digits, '.', '-' and '_'", a.ID)
	}
	if Reserved(a.ID) {
		bad("id %q: run and run-* are the commands of app.run", a.ID)
	}
	for _, name := range sortedKeys(a.Args) {
		kind := a.Args[name]
		switch {
		case !reArg.MatchString(name):
			bad("args: %q: use lowercase letters, digits and _", name)
		case kind != spec.ArgURL && kind != spec.ArgPath && kind != spec.ArgText:
			bad("args: %s = %q: use \"url\", \"path\" or \"text\"", name, kind)
		case !usesArg(a, name):
			bad("args: %s is not used: put {%s} in the runner's command or url", name, name)
		}
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
	case spec.RunnerExec, spec.RunnerProcess:
		if len(a.Runner.Command) == 0 {
			bad("runner %s needs a command", a.Runner.Type)
		}
	case spec.RunnerURL:
		if u := sampleArgs(a, a.Runner.URL); !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			bad("runner url needs an http:// or https:// url")
		}
	case spec.RunnerDocker:
		if a.Runner.Image == "" && a.Runner.Build == "" {
			bad("runner docker needs an image or a build context")
		}
	case spec.RunnerCompose:
		if a.Runner.File == "" {
			bad("runner compose needs a file")
		}
	case spec.RunnerFlatpak:
		if a.Runner.AppID == "" {
			bad("runner %s needs an app_id", a.Runner.Type)
		}
	}
	if h := a.Runner.Handoff; h != "" {
		if k, _, ok := strings.Cut(h, "="); !ok || k == "" || strings.ContainsAny(h, " \t\n") {
			bad("runner.handoff %q: use KEY=value, a variable the app's real processes have", h)
		}
		if a.Runner.Type != spec.RunnerExec && a.Runner.Type != spec.RunnerFlatpak && a.Runner.Type != spec.RunnerURL {
			bad("runner.handoff applies to exec, flatpak and url apps")
		}
	}
	if a.Surface != spec.SurfaceWindow && a.Surface != spec.SurfaceBackground {
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
			if a.Runner.Type != spec.RunnerDocker {
				bad("source: type image needs runner type docker")
			}
			if d.Image == "" || d.ContainerPort < 1 || d.ContainerPort > 65535 {
				bad("source: type image needs image and container_port (the port the container listens on)")
			}
		} else {
			if a.Runner.Type != spec.RunnerProcess {
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
	if len(a.Actions) > 0 && a.Surface != spec.SurfaceWindow {
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
	apps     map[string]*spec.App
	alias    map[string]string // alias -> id
	Problems []Problem
}

// Get finds an app by ID or alias.
func (c *Catalog) Get(id string) (*spec.App, bool) {
	if a, ok := c.apps[id]; ok {
		return a, true
	}
	if real, ok := c.alias[id]; ok {
		return c.apps[real], true
	}
	return nil, false
}

// List returns the apps sorted by ID, without hidden ones unless all.
func (c *Catalog) List(all bool) []*spec.App {
	out := make([]*spec.App, 0, len(c.apps))
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
func Build(discovered []spec.App, files []AppFile, problems []Problem) *Catalog {
	c := &Catalog{apps: map[string]*spec.App{}, alias: map[string]string{}, Problems: problems}
	for i := range discovered {
		a := discovered[i]
		Defaults(&a)
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

func (c *Catalog) applyFile(f AppFile, fromFile map[string]string) (*spec.App, error) {
	id := f.ID
	var app spec.App
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
		app = spec.App{Source: "file", Files: []string{f.Path}}
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
	Defaults(&app)
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

// Diff lists the apps added, removed and changed from old to cur, by ID.
func Diff(old, cur *Catalog) (added, removed, changed []string) {
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
