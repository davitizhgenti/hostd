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
	"strings"
	"sync"
	"time"

	"github.com/davitizhgenti/hostd/internal/execx"
	"github.com/davitizhgenti/hostd/internal/paths"
	"github.com/davitizhgenti/hostd/sdk/kit"

	"github.com/davitizhgenti/hostd/internal/clock"
	"github.com/davitizhgenti/hostd/sdk"
)

// Events.
const (
	EventSceneApplied = "scene.applied"
)

// Options configure the automation module.
type Options struct {
	ScriptsDir string // default ~/.config/hostd/scripts
	// URL is hostd's API for scripts (HOSTD_URL), e.g. unix:///run/user/1000/hostd.sock.
	URL      string
	Location *time.Location // for if.between (default local time)
	Clock    clock.Clock
	Logger   *slog.Logger
	// Limiter pauses rules that fire too often, and Tokens mints script
	// tokens; set here only by tests, the core offers both.
	Limiter Limiter
	Tokens  Tokens
	// Exec runs the scripts (default execx.OS).
	Exec execx.Runner
}

// Module is the automation module.
type Module struct {
	opts Options
	log  *slog.Logger

	mu         sync.Mutex
	core       sdk.Core
	scenes     map[string]Scene      // in use, from the scenes kind
	scripts    map[string]scriptSpec // in use, from the scripts kind
	rules      []Rule                // in use, from the rules kind, by name
	runs       map[string]*ruleRun
	scriptRuns int
	actions    *kit.Actions

	stop context.CancelFunc
	done sync.WaitGroup
}

// New returns the automation module.
func New(opts Options) *Module {
	if opts.Exec == nil {
		opts.Exec = execx.OS{}
	}
	if opts.ScriptsDir == "" {
		opts.ScriptsDir = paths.Config("scripts")
	}
	if opts.Location == nil {
		opts.Location = time.Local
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	m := &Module{opts: opts, log: opts.Logger, runs: map[string]*ruleRun{}, scenes: map[string]Scene{}, scripts: map[string]scriptSpec{}}
	m.actions = m.declare()
	return m
}

// The arguments of the module's actions.
type (
	ruleArgs struct {
		Name string `json:"name" desc:"the rule"`
	}
	scriptArgs struct {
		Name string `json:"name" desc:"the script"`
	}
	sceneArgs struct {
		ID string `json:"id" desc:"the scene"`
	}
)

// declare is the module's actions.
func (m *Module) declare() *kit.Actions {
	t := kit.NewActions("automation")
	kit.On(t, sdk.ActionSpec{Type: "scene.apply", Description: "Reach a scene: send only the actions still needed, in a fixed order",
		Keys: []sdk.KeyTemplate{"scene:{id}"}, Scope: "scenes",
		Timeout: sdk.Duration(10 * time.Minute), Busy: true}, // starting apps can take a while
		func(ctx context.Context, a sdk.Action, args sceneArgs) (Applied, error) {
			s, err := m.scene(args.ID)
			if err != nil {
				return Applied{}, err
			}
			res := m.apply(ctx, s)
			m.core.Emit(sdk.Event{Type: EventSceneApplied, Action: a.ID, Data: sdk.MustJSON(res)}) // in the chain: rules on it continue it
			return res, nil
		})
	kit.On(t, sdk.ActionSpec{Type: "rule.pause", Description: "Stop a rule from firing until it is resumed",
		Keys: []sdk.KeyTemplate{"rule:{name}"}, Scope: "rules"},
		func(_ context.Context, _ sdk.Action, args ruleArgs) (struct{}, error) {
			return struct{}{}, m.pauseRule(args.Name, true)
		})
	kit.On(t, sdk.ActionSpec{Type: "script.run", Description: "Run a script now, as a rule would (with an empty event)",
		Keys: []sdk.KeyTemplate{"script:{name}"}, Scope: "scripts", Timeout: sdk.Duration(10 * time.Minute)},
		func(ctx context.Context, _ sdk.Action, args scriptArgs) (map[string]string, error) {
			out, err := m.script(ctx, args.Name, args.Name, "", json.RawMessage("{}"))
			if err != nil {
				code := sdk.CodeInternal
				if strings.HasPrefix(err.Error(), "no script") {
					code = sdk.CodeNotFound
				}
				return nil, sdk.Errorf(code, "%v", err)
			}
			return map[string]string{"result": out}, nil
		})
	kit.On(t, sdk.ActionSpec{Type: "rule.resume", Description: "Let a paused rule fire again",
		Keys: []sdk.KeyTemplate{"rule:{name}"}, Scope: "rules"},
		func(_ context.Context, _ sdk.Action, args ruleArgs) (struct{}, error) {
			return struct{}{}, m.pauseRule(args.Name, false)
		})
	return t
}

func (m *Module) Manifest() sdk.Manifest {
	return sdk.Manifest{
		Name: "automation", Version: "0.1.0",
		Owns: []string{"scene.*", "rule.*", "script.*"},
		Scopes: []sdk.ScopeSpec{
			{Name: "scenes", Description: "Apply scenes"},
			{Name: "rules", Description: "Pause and resume rules"},
			{Name: "scripts", Description: "Run scripts"},
		},
		Kinds:   kinds(),
		Actions: m.actions.Specs(),
		Events: []sdk.EventSpec{
			{Type: EventSceneApplied, Description: "A scene was applied, with each step's result"},
		},
	}
}

func (m *Module) Start(_ context.Context, core sdk.Core) error {
	m.mu.Lock()
	m.core = core
	m.mu.Unlock()
	// What the core offers, unless the options already set it (tests).
	if m.opts.Limiter == nil {
		if l := core.Limiter(); l != nil {
			m.opts.Limiter = l
		}
	}
	if m.opts.Tokens == nil {
		if t := core.Tokens(); t != nil {
			m.opts.Tokens = t
		}
	}
	if err := os.MkdirAll(m.opts.ScriptsDir, 0o755); err != nil {
		m.log.Warn("cannot create a folder", "dir", m.opts.ScriptsDir, "err", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.stop = cancel
	events := core.Subscribe(ctx, "*")
	m.done.Add(1)
	go func() {
		defer m.done.Done()
		m.followRules(ctx, events)
	}()
	return nil
}

func (m *Module) Stop(context.Context) error {
	if m.stop != nil {
		m.stop()
	}
	m.done.Wait()
	return nil
}

func (m *Module) Validate(ctx context.Context, a sdk.Action) error { return m.actions.Validate(ctx, a) }

func (m *Module) Handle(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	return m.actions.Handle(ctx, a)
}

// scene returns a scene in use.
func (m *Module) scene(id string) (Scene, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.scenes[id]; ok {
		return s, nil
	}
	return Scene{}, sdk.Errorf(sdk.CodeNotFound, "no scene %q (hostctl get scenes)", id)
}
