package apps

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/modules/apps/catalog"
	"github.com/davitizhgenti/hostd/modules/apps/spec"
	"github.com/davitizhgenti/hostd/sdk"
)

// Commands from the API, for a token with scope run: what someone at the
// machine could do in a terminal, done from elsewhere.
//
//   app.run       runs a command now, as an instance without an app file:
//                 on the screen, or in the background
//
// (An app file is written like any resource: hostctl apply, PUT /v1/apps/{name}.)

const (
	ActionRun = "app.run"
)

type runArgs struct {
	Command    []string          `json:"command" min:"1" desc:"the program and its arguments"`
	Name       string            `json:"name,omitempty" pattern:"^[a-z0-9][a-z0-9-]{0,31}$" desc:"names the instance: run-<name> (default: run)"`
	Background bool              `json:"background,omitempty" desc:"no window: a service in the background (default: a window on the screen)"`
	Fullscreen *bool             `json:"fullscreen,omitempty" desc:"a window: full screen (default true)"`
	Front      bool              `json:"front,omitempty" desc:"open in front even while someone is using the screen (needs scope display.front)"`
	Dir        string            `json:"dir,omitempty" desc:"the directory it runs in (default: the screen user's home)"`
	Env        map[string]string `json:"env,omitempty" desc:"extra environment"`
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

func (m *Module) handleRun(ctx context.Context, a sdk.Action, args runArgs) (sdk.Result, error) {
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
