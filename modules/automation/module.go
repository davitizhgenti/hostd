// Package automation is the automation module: scenes (a state to reach,
// applied as the actions still needed), rules (when to apply them) and
// scripts. It changes nothing itself: every step is an action of the
// module that owns it, so priority holds, permissions and the audit
// trail apply. A scene applied by hand runs at manual priority, by a rule
// at automation.
package automation

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/davitizhgenti/hostd/internal/clock"
	"github.com/davitizhgenti/hostd/sdk"
)

// Events.
const (
	EventSceneApplied = "scene.applied"
)

// Options configure the automation module.
type Options struct {
	ScenesDir string // default ~/.config/hostd/scenes
	Clock     clock.Clock
	Logger    *slog.Logger
}

// DefaultConfigDir is $XDG_CONFIG_HOME/hostd.
func DefaultConfigDir() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "hostd")
}

// Module is the automation module.
type Module struct {
	opts Options
	log  *slog.Logger

	mu   sync.Mutex
	core sdk.Core
}

// New returns the automation module.
func New(opts Options) *Module {
	if opts.ScenesDir == "" {
		opts.ScenesDir = filepath.Join(DefaultConfigDir(), "scenes")
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Module{opts: opts, log: opts.Logger}
}

var sceneSchema = json.RawMessage(`{"type":"object","properties":{
	"id":{"type":"string","description":"the scene (its file name in the scenes folder)"}},"required":["id"],"additionalProperties":false}`)

func (m *Module) Manifest() sdk.Manifest {
	return sdk.Manifest{
		Name: "automation", Version: "0.1.0",
		Owns:   []string{"scene.*"},
		Scopes: []sdk.ScopeSpec{{Name: "scenes", Description: "Apply scenes"}},
		Actions: []sdk.ActionSpec{
			{Type: "scene.apply", Description: "Reach a scene: send only the actions still needed, in a fixed order",
				Schema: sceneSchema, Keys: []sdk.KeyTemplate{"scene:{id}"}, Scope: "scenes",
				Timeout: sdk.Duration(10 * time.Minute), // starting apps can take a while
				Route:   &sdk.Route{Method: "POST", Path: "/v1/scenes/{id}/apply"}},
		},
		Events: []sdk.EventSpec{
			{Type: EventSceneApplied, Description: "A scene was applied, with each step's result"},
		},
		Reads: []sdk.ReadSpec{
			{Name: "scenes", Description: "The scenes, and scene files that cannot be used", Path: "/v1/scenes"},
			{Name: "scene", Description: "One scene", Path: "/v1/scenes/{id}"},
		},
	}
}

func (m *Module) Start(_ context.Context, core sdk.Core) error {
	m.mu.Lock()
	m.core = core
	m.mu.Unlock()
	if err := os.MkdirAll(m.opts.ScenesDir, 0o755); err != nil {
		m.log.Warn("cannot create the scenes folder", "dir", m.opts.ScenesDir, "err", err)
	}
	return nil
}

func (m *Module) Stop(context.Context) error { return nil }

func (m *Module) Validate(context.Context, sdk.Action) error { return nil }

func (m *Module) Handle(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	if a.Type != "scene.apply" {
		return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "automation module has no action %q", a.Type)
	}
	var args struct{ ID string }
	if err := a.DecodeArgs(&args); err != nil {
		return sdk.Result{}, err
	}
	s, err := m.scene(args.ID)
	if err != nil {
		return sdk.Result{}, err
	}
	res := m.apply(ctx, s)
	m.core.Emit(sdk.Event{Type: EventSceneApplied, Data: sdk.MustJSON(res)})
	return sdk.Result{Data: sdk.MustJSON(res)}, nil
}

// scene reads one scene file, reporting its problem if it has one.
func (m *Module) scene(id string) (Scene, error) {
	scenes, problems := loadScenes(m.opts.ScenesDir)
	if s, ok := scenes[id]; ok {
		return s, nil
	}
	for _, p := range problems {
		if filepath.Base(p.File) == id+".toml" {
			return Scene{}, sdk.Errorf(sdk.CodeInvalidArgs, "scene %q cannot be used: %s", id, p.Error)
		}
	}
	return Scene{}, sdk.Errorf(sdk.CodeNotFound, "no scene %q (%s)", id, filepath.Join(m.opts.ScenesDir, id+".toml"))
}

// SceneList is GET /v1/scenes.
type SceneList struct {
	Scenes   []Scene   `json:"scenes"`
	Problems []Problem `json:"problems"`
}

func (m *Module) Read(_ context.Context, name string, params map[string]string) (any, error) {
	switch name {
	case "scenes":
		scenes, problems := loadScenes(m.opts.ScenesDir)
		list := SceneList{Scenes: []Scene{}, Problems: problems}
		if list.Problems == nil {
			list.Problems = []Problem{}
		}
		for _, s := range scenes {
			list.Scenes = append(list.Scenes, s)
		}
		sort.Slice(list.Scenes, func(i, j int) bool { return list.Scenes[i].ID < list.Scenes[j].ID })
		return list, nil
	case "scene":
		return m.scene(params["id"])
	}
	return nil, sdk.Errorf(sdk.CodeNotFound, "automation module has no read %q", name)
}
