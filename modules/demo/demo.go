// Package demo is a pretend module for trying hostd and hostctl before the
// real modules exist: a set of lamps with a brightness. hostd loads it only
// with -demo.
package demo

import (
	"context"
	"encoding/json"
	"sort"
	"sync"

	"github.com/davitizhgenti/hostd/sdk"
)

// Module is the demo module.
type Module struct {
	mu    sync.Mutex
	core  sdk.Core
	lamps map[string]int
}

// New returns the demo module.
func New() *Module { return &Module{lamps: map[string]int{}} }

func (m *Module) Manifest() sdk.Manifest {
	return sdk.Manifest{
		Name: "demo", Version: "0.1.0", Owns: []string{"demo.*"},
		Scopes: []sdk.ScopeSpec{{Name: "demo", Description: "Change the demo lamps"}},
		Actions: []sdk.ActionSpec{
			{
				Type:        "demo.lamp.set",
				Description: "Set a lamp's brightness",
				Schema: json.RawMessage(`{
					"type": "object",
					"properties": {
						"lamp": {"type": "string", "description": "lamp name"},
						"brightness": {"type": "integer", "minimum": 0, "maximum": 100, "description": "0-100"}
					},
					"required": ["lamp", "brightness"]
				}`),
				Keys:  []sdk.KeyTemplate{"demo.lamp:{lamp}"},
				Scope: "demo",
				Route: &sdk.Route{Method: "POST", Path: "/v1/demo/lamps/{lamp}"},
			},
			{
				Type:        "demo.lamp.off",
				Description: "Turn every lamp off",
				Scope:       "demo",
			},
		},
		Events: []sdk.EventSpec{{Type: "demo.lamp.changed", Description: "A lamp's brightness changed"}},
	}
}

func (m *Module) Start(_ context.Context, core sdk.Core) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.core = core
	return nil
}

func (m *Module) Stop(context.Context) error { return nil }

func (m *Module) Validate(context.Context, sdk.Action) error { return nil }

func (m *Module) Handle(_ context.Context, a sdk.Action) (sdk.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch a.Type {
	case "demo.lamp.set":
		var args struct {
			Lamp       string `json:"lamp"`
			Brightness int    `json:"brightness"`
		}
		if err := a.DecodeArgs(&args); err != nil {
			return sdk.Result{}, err
		}
		m.lamps[args.Lamp] = args.Brightness
		m.core.Emit(sdk.Event{Type: "demo.lamp.changed", Action: a.ID, Resource: "demo.lamp:" + args.Lamp,
			Data: mustJSON(map[string]any{"lamp": args.Lamp, "brightness": args.Brightness})})
	case "demo.lamp.off":
		names := make([]string, 0, len(m.lamps))
		for n := range m.lamps {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			m.lamps[n] = 0
			m.core.Emit(sdk.Event{Type: "demo.lamp.changed", Action: a.ID,
				Data: mustJSON(map[string]any{"lamp": n, "brightness": 0})})
		}
	}
	return sdk.Result{}, nil
}

// State reports every lamp's brightness.
func (m *Module) State(context.Context) (any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]int, len(m.lamps))
	for k, v := range m.lamps {
		out[k] = v
	}
	return map[string]any{"lamps": out}, nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
