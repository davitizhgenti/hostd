// Package demo is a pretend module for trying hostd and hostctl before the
// real modules exist: a set of lamps with a brightness. hostd loads it only
// with -demo. It is also the smallest example of a module written with the
// kit (sdk/kit): copy it to start one of your own.
package demo

import (
	"context"
	"sort"
	"sync"

	"github.com/davitizhgenti/hostd/internal/host"
	"github.com/davitizhgenti/hostd/sdk"
	"github.com/davitizhgenti/hostd/sdk/kit"
)

// setArgs are the arguments of demo.lamp.set; the schema comes from them.
type setArgs struct {
	Lamp       string `json:"lamp" desc:"lamp name"`
	Brightness int    `json:"brightness" desc:"0-100" min:"0" max:"100"`
}

// presetSpec is the spec of the kind "preset": a file in
// ~/.config/hostd/presets/ that saves a lamp's setting.
type presetSpec struct {
	Lamp       string `json:"lamp" desc:"lamp name"`
	Brightness int    `json:"brightness" desc:"0-100" min:"0" max:"100"`
}

// New returns the demo module.
func New() *kit.Module {
	var (
		mu      sync.Mutex
		lamps   = map[string]int{}
		presets = map[string]presetSpec{}
	)
	m := kit.New("demo", "0.1.0").Owns("demo.*").Scope("demo", "Change the demo lamps")
	changed := kit.Event[map[string]any](m, "demo.lamp.changed", "A lamp's brightness changed")

	kit.Action(m, kit.Spec{
		Type: "demo.lamp.set", Description: "Set a lamp's brightness", Scope: "demo",
		Keys: []sdk.KeyTemplate{"demo.lamp:{lamp}"}, Route: "POST /v1/demo/lamps/{lamp}",
	}, func(_ context.Context, c kit.Ctx, a setArgs) (struct{}, error) {
		mu.Lock()
		defer mu.Unlock()
		lamps[a.Lamp] = a.Brightness
		changed.Emit(c, map[string]any{"lamp": a.Lamp, "brightness": a.Brightness}, "demo.lamp:"+a.Lamp)
		return struct{}{}, nil
	})

	kit.Action(m, kit.Spec{Type: "demo.lamp.off", Description: "Turn every lamp off", Scope: "demo"},
		func(_ context.Context, c kit.Ctx, _ struct{}) (struct{}, error) {
			mu.Lock()
			defer mu.Unlock()
			names := make([]string, 0, len(lamps))
			for n := range lamps {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				lamps[n] = 0
				changed.Emit(c, map[string]any{"lamp": n, "brightness": 0}, "")
			}
			return struct{}{}, nil
		})

	// A kind of resource: saved settings, one file each. Applying one (the
	// file appearing or changing) only stores it; the verb "apply" uses it.
	kit.Kind(m, kit.KindSpec{Kind: "preset", Plural: "presets", Description: "A saved lamp setting",
		Verbs: []sdk.VerbSpec{{Name: "apply", Description: "Set the lamp as the preset says", Action: "demo.preset.apply"}}},
		func(_ context.Context, _ kit.Ctx, r kit.Res[presetSpec]) (sdk.ResourceStatus, error) {
			mu.Lock()
			defer mu.Unlock()
			presets[r.Name] = r.Spec
			return sdk.ResourceStatus{Conditions: []sdk.Condition{{Type: sdk.ConditionReady, Status: sdk.ConditionTrue}}}, nil
		},
		func(_ context.Context, _ kit.Ctx, name string) error {
			mu.Lock()
			defer mu.Unlock()
			delete(presets, name)
			return nil
		})
	kit.Action(m, kit.Spec{Type: "demo.preset.apply", Description: "Set the lamp as a preset says", Scope: "demo",
		Keys: []sdk.KeyTemplate{"demo.preset:{name}"}},
		func(ctx context.Context, c kit.Ctx, a struct {
			Name string `json:"name" desc:"the preset"`
		}) (struct{}, error) {
			mu.Lock()
			p, ok := presets[a.Name]
			mu.Unlock()
			if !ok {
				return struct{}{}, sdk.Errorf(sdk.CodeNotFound, "no preset %q", a.Name)
			}
			args := sdk.MustJSON(setArgs(p))
			_, err := c.Core.Do(ctx, sdk.Action{Type: "demo.lamp.set", Args: args})
			return struct{}{}, err
		})

	// GET /v1/state/demo: every lamp's brightness.
	return m.WithState(func(context.Context, kit.Ctx) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		out := make(map[string]int, len(lamps))
		for k, v := range lamps {
			out[k] = v
		}
		return map[string]any{"lamps": out}, nil
	})
}

// Builtin is how hostd builds the demo module (loaded only with -demo).
var Builtin = host.Builtin{
	Name: "demo",
	New:  func(*host.Host, any) (sdk.Module, error) { return New(), nil },
}
