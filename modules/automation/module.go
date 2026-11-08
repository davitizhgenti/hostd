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
	"sync"
	"time"

	"github.com/davitizhgenti/hostd/internal/paths"

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
}

// Module is the automation module.
type Module struct {
	opts Options
	log  *slog.Logger

	mu         sync.Mutex
	core       sdk.Core
	scenes     map[string]Scene // in use, from the scenes kind
	rules      []Rule           // in use, from the rules kind, by name
	runs       map[string]*ruleRun
	scriptRuns int

	stop context.CancelFunc
	done sync.WaitGroup
}

// New returns the automation module.
func New(opts Options) *Module {
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
	return &Module{opts: opts, log: opts.Logger, runs: map[string]*ruleRun{}, scenes: map[string]Scene{}}
}

var ruleSchema = json.RawMessage(`{"type":"object","properties":{
	"name":{"type":"string","description":"the rule"}},"required":["name"],"additionalProperties":false}`)

var sceneSchema = json.RawMessage(`{"type":"object","properties":{
	"id":{"type":"string","description":"the scene"}},"required":["id"],"additionalProperties":false}`)

func (m *Module) Manifest() sdk.Manifest {
	return sdk.Manifest{
		Name: "automation", Version: "0.1.0",
		Owns: []string{"scene.*", "rule.*"},
		Scopes: []sdk.ScopeSpec{
			{Name: "scenes", Description: "Apply scenes"},
			{Name: "rules", Description: "Pause and resume rules"},
		},
		Kinds: kinds(),
		Actions: []sdk.ActionSpec{
			{Type: "scene.apply", Description: "Reach a scene: send only the actions still needed, in a fixed order",
				Schema: sceneSchema, Keys: []sdk.KeyTemplate{"scene:{id}"}, Scope: "scenes",
				Timeout: sdk.Duration(10 * time.Minute)}, // starting apps can take a while
			{Type: "rule.pause", Description: "Stop a rule from firing until it is resumed",
				Schema: ruleSchema, Keys: []sdk.KeyTemplate{"rule:{name}"}, Scope: "rules"},
			{Type: "rule.resume", Description: "Let a paused rule fire again",
				Schema: ruleSchema, Keys: []sdk.KeyTemplate{"rule:{name}"}, Scope: "rules"},
		},
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

func (m *Module) Validate(context.Context, sdk.Action) error { return nil }

func (m *Module) Handle(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	if a.Type == "rule.pause" || a.Type == "rule.resume" {
		return m.handleRule(a)
	}
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
	m.core.Emit(sdk.Event{Type: EventSceneApplied, Action: a.ID, Data: sdk.MustJSON(res)}) // in the chain: rules on it continue it
	return sdk.Result{Data: sdk.MustJSON(res)}, nil
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
