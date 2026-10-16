package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/modules/apps/catalog"
	"github.com/davitizhgenti/hostd/modules/apps/spec"
	"github.com/davitizhgenti/hostd/sdk"
)

// Commands and app files from the API, for a token with scope run: what
// someone at the machine could do in a terminal, done from elsewhere.
//
//   app.run       runs a command now, as an instance without an app file:
//                 on the screen, or in the background
//   app.define    writes an app file (checked first), as if written by hand
//   app.undefine  removes one

const (
	ActionRun      = "app.run"
	ActionDefine   = "app.define"
	ActionUndefine = "app.undefine"
)

var (
	runSchema = json.RawMessage(`{"type":"object","properties":{
		"command":{"type":"array","items":{"type":"string"},"minItems":1,"description":"the program and its arguments"},
		"name":{"type":"string","pattern":"^[a-z0-9][a-z0-9-]{0,31}$","description":"names the instance: run-<name> (default: run)"},
		"background":{"type":"boolean","description":"no window: a service in the background (default: a window on the screen)"},
		"fullscreen":{"type":"boolean","description":"a window: full screen (default true)"},
		"front":{"type":"boolean","description":"open in front even while someone is using the screen (needs scope display.front)"},
		"dir":{"type":"string","description":"the directory it runs in (default: the screen user's home)"},
		"env":{"type":"object","additionalProperties":{"type":"string"},"description":"extra environment"}},
		"required":["command"],"additionalProperties":false}`)
	defineSchema = json.RawMessage(`{"type":"object","properties":{
		"id":{"type":"string","description":"the app's ID: the file is <id>.toml in the app files"},
		"file":{"type":"string","contentMediaType":"application/toml","description":"the app file, TOML, as written by hand (docs: Apps)"}},
		"required":["id","file"],"additionalProperties":false}`)
	undefineSchema = json.RawMessage(`{"type":"object","properties":{
		"id":{"type":"string","description":"an app defined by a file in the app files"}},"required":["id"],"additionalProperties":false}`)
)

func runActions() []sdk.ActionSpec {
	return []sdk.ActionSpec{
		{Type: ActionRun, Description: "Run a command now, on the screen or in the background (an instance with no app file)",
			Schema: runSchema, Keys: []sdk.KeyTemplate{"app:run"}, Scope: contract.ScopeRun,
			ArgScopes: map[string]string{"front": contract.ScopeFront},
			Timeout:   sdk.Duration(time.Minute),
			Route:     &sdk.Route{Method: "POST", Path: "/v1/run"}},
		{Type: ActionDefine, Description: "Add or replace an app file (checked first; the catalog follows at once)",
			Schema: defineSchema, Keys: []sdk.KeyTemplate{"appfile:{id}"}, Scope: contract.ScopeRun,
			Route: &sdk.Route{Method: "PUT", Path: "/v1/apps/{id}/file"}},
		{Type: ActionUndefine, Description: "Remove an app file written by hand or by app.define",
			Schema: undefineSchema, Keys: []sdk.KeyTemplate{"appfile:{id}"}, Scope: contract.ScopeRun,
			Route: &sdk.Route{Method: "DELETE", Path: "/v1/apps/{id}/file"}},
	}
}

type runArgs struct {
	Command    []string          `json:"command"`
	Name       string            `json:"name"`
	Background bool              `json:"background"`
	Fullscreen *bool             `json:"fullscreen"`
	Front      bool              `json:"front"`
	Dir        string            `json:"dir"`
	Env        map[string]string `json:"env"`
}

var reEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// runApp is the app an app.run instance is of: made for the call, not in
// the catalog. Its ID is run, or run-<name>; instances are numbered.
func runApp(args runArgs) (*App, error) {
	if args.Command[0] == "" {
		return nil, sdk.Errorf(sdk.CodeInvalidArgs, "command: the program is empty")
	}
	if args.Dir != "" && !filepath.IsAbs(args.Dir) {
		return nil, sdk.Errorf(sdk.CodeInvalidArgs, "dir must be an absolute path, not %q", args.Dir)
	}
	for k := range args.Env {
		if !reEnvName.MatchString(k) {
			return nil, sdk.Errorf(sdk.CodeInvalidArgs, "env: %q is not a variable name", k)
		}
	}
	id, name := "run", filepath.Base(args.Command[0])
	if args.Name != "" {
		id, name = "run-"+args.Name, args.Name
	}
	a := &App{ID: id, Name: name, Source: "run", Hidden: true, Restart: "never",
		Runner:   Runner{Type: RunnerExec, Command: args.Command},
		Instance: InstancePolicy{Policy: "multiple"}}
	if args.Background {
		a.Runner.Type, a.Surface = RunnerProcess, SurfaceBackground
	} else {
		a.Window.Fullscreen = args.Fullscreen == nil || *args.Fullscreen
	}
	catalog.Defaults(a)
	return a, nil
}

func (m *Module) handleRun(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	var args runArgs
	if err := a.DecodeArgs(&args); err != nil {
		return sdk.Result{}, err
	}
	app, err := runApp(args)
	if err != nil {
		return sdk.Result{}, err
	}
	backend := m.backend(app.Runner.Type)
	if backend == nil {
		return sdk.Result{}, sdk.Errorf(sdk.CodeModuleUnavailable, "hostd cannot run commands here (no exec runner)")
	}
	return m.launch(ctx, a.ID, app, backend, contract.AppStart{ID: app.ID, Front: args.Front, Dir: args.Dir, Env: args.Env})
}

// appFile is where app.define writes an app's file; an error if the app
// files cannot be written (none configured, or a followed repository's).
func (m *Module) appFile(id string) (string, error) {
	if !catalog.ValidID(id) {
		return "", sdk.Errorf(sdk.CodeInvalidArgs, "%q cannot name an app: lowercase letters, digits, '.', '_' and '-'", id)
	}
	dir := m.opts.AppsDir
	if dir == "" {
		return "", sdk.Errorf(sdk.CodeModuleUnavailable, "this hostd has no folder of app files")
	}
	if fi, err := os.Lstat(dir); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return "", sdk.Errorf(sdk.CodePreconditionFailed, "the app files come from a followed repository (hostctl config follow): change them there")
	}
	return filepath.Join(dir, id+".toml"), nil
}

func (m *Module) handleDefine(a sdk.Action) (sdk.Result, error) {
	var args struct{ ID, File string }
	if err := a.DecodeArgs(&args); err != nil {
		return sdk.Result{}, err
	}
	path, err := m.appFile(args.ID)
	if err != nil {
		return sdk.Result{}, err
	}
	f, err := catalog.ParseAppFile(path, []byte(args.File))
	if err != nil {
		return sdk.Result{}, sdk.Errorf(sdk.CodeInvalidArgs, "%s: %v", args.ID, err)
	}
	if f.Extends == "" && f.ID != args.ID {
		return sdk.Result{}, sdk.Errorf(sdk.CodeInvalidArgs, "the file defines app %q, not %q", f.ID, args.ID)
	}
	// The catalog as it would be: this file in place of the one it replaces.
	src := catalog.DesktopSource{Dirs: m.opts.DesktopDirs, Desktop: m.opts.Desktop, LookPath: m.opts.LookPath}
	discovered, _ := src.Scan()
	files, _ := catalog.LoadAppFiles(m.opts.AppsDir)
	others := files[:0]
	for _, o := range files {
		if o.Path != path {
			others = append(others, o)
		}
	}
	for _, p := range catalog.Build(discovered, append(others, f), nil).Problems {
		if p.File == path {
			return sdk.Result{}, sdk.Errorf(sdk.CodeInvalidArgs, "%s: %s", args.ID, p.Error)
		}
	}
	_, statErr := os.Stat(path)
	if err := writeAtomic(path, []byte(args.File)); err != nil {
		return sdk.Result{}, sdk.Errorf(sdk.CodeInternal, "writing %s: %v", path, err)
	}
	m.rescan()
	return sdk.Result{Data: sdk.MustJSON(map[string]any{"id": args.ID, "file": path, "replaced": statErr == nil})}, nil
}

func (m *Module) handleUndefine(a sdk.Action) (sdk.Result, error) {
	var args struct{ ID string }
	if err := a.DecodeArgs(&args); err != nil {
		return sdk.Result{}, err
	}
	path, err := m.appFile(args.ID)
	if err != nil {
		return sdk.Result{}, err
	}
	if err := os.Remove(path); errors.Is(err, fs.ErrNotExist) {
		return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "no app file %s (an installed app cannot be removed this way)", path)
	} else if err != nil {
		return sdk.Result{}, sdk.Errorf(sdk.CodeInternal, "%v", err)
	}
	m.rescan()
	return sdk.Result{Data: sdk.MustJSON(map[string]string{"id": args.ID, "removed": path})}, nil
}

// writeAtomic writes a file in one step: readers (the folder's watcher)
// see the old file or the new one, never half of one. The temporary name
// starts with a dot, which the catalog skips.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // after the rename, there is nothing to remove
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// withArgs is app with its {placeholders} filled in from values: every
// argument its file declares, each checked against its kind, and nothing
// else. An app without arguments takes none.
func withArgs(app *App, values map[string]string) (*App, error) {
	if len(app.Args) == 0 && len(values) == 0 {
		return app, nil
	}
	for name := range values {
		if _, ok := app.Args[name]; !ok {
			if len(app.Args) == 0 {
				return nil, sdk.Errorf(sdk.CodeInvalidArgs, "app %q takes no arguments", app.ID)
			}
			return nil, sdk.Errorf(sdk.CodeInvalidArgs, "app %q has no argument %q (it takes: %s)", app.ID, name, argList(app))
		}
	}
	out := *app
	out.Runner.Command = slices.Clone(app.Runner.Command)
	for _, name := range slices.Sorted(maps.Keys(app.Args)) {
		v, ok := values[name]
		if !ok {
			return nil, sdk.Errorf(sdk.CodeInvalidArgs, "app %q needs %s (args: {%q: ...})", app.ID, name, name)
		}
		if err := checkArg(app.Args[name], v); err != nil {
			return nil, sdk.Errorf(sdk.CodeInvalidArgs, "%s: %v", name, err)
		}
		ph := "{" + name + "}"
		for i, c := range out.Runner.Command {
			out.Runner.Command[i] = strings.ReplaceAll(c, ph, v)
		}
		out.Runner.URL = strings.ReplaceAll(out.Runner.URL, ph, v)
	}
	return &out, nil
}

func argList(app *App) string {
	var parts []string
	for _, name := range slices.Sorted(maps.Keys(app.Args)) {
		parts = append(parts, name+" ("+app.Args[name]+")")
	}
	return strings.Join(parts, ", ")
}

// checkArg checks one value. None may start with "-": in a command it
// would be read as an option.
func checkArg(kind, v string) error {
	switch {
	case v == "":
		return errors.New("empty")
	case len(v) > 4096:
		return errors.New("longer than 4096 bytes")
	case strings.HasPrefix(v, "-"):
		return errors.New("cannot start with '-'")
	case strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return errors.New("has a control character (a newline?)")
	}
	switch kind {
	case spec.ArgURL:
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%q is not an http or https URL", v)
		}
	case spec.ArgPath:
		if !filepath.IsAbs(v) {
			return fmt.Errorf("%q is not an absolute path", v)
		}
	}
	return nil
}
