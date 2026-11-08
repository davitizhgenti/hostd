package kit_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/sdk"
	"github.com/davitizhgenti/hostd/sdk/kit"
)

type setArgs struct {
	Lamp       string `json:"lamp" desc:"lamp name"`
	Brightness int    `json:"brightness" min:"0" max:"100"`
}

type lamp struct {
	Lamp       string `json:"lamp"`
	Brightness int    `json:"brightness"`
}

type listParams struct {
	Min  int  `json:"min,omitempty"`
	Only bool `json:"only,omitempty"`
}

// lights builds a small module with the kit: the same shape as the demo
// module, in a fraction of the code.
func lights() (*kit.Module, *sync.Map) {
	state := &sync.Map{}
	m := kit.New("lights", "1.0.0").Owns("lights.*").Scope("lights", "Change the lights")
	changed := kit.Event[lamp](m, "lights.changed", "A lamp changed")

	kit.Action(m, kit.Spec{Type: "lights.set", Description: "Set a lamp", Scope: "lights",
		Keys: []sdk.KeyTemplate{"lamp:{lamp}"}, Route: "POST /v1/lights/{lamp}"},
		func(_ context.Context, c kit.Ctx, a setArgs) (lamp, error) {
			state.Store(a.Lamp, a.Brightness)
			l := lamp(a)
			changed.Emit(c, l, "lamp:"+a.Lamp)
			return l, nil
		})
	kit.Validate(m, "lights.set", func(_ context.Context, _ kit.Ctx, a setArgs) error {
		if a.Lamp == "ghost" {
			return sdk.Errorf(sdk.CodeNotFound, "no lamp %q", a.Lamp)
		}
		return nil
	})
	kit.Action(m, kit.Spec{Type: "lights.off", Scope: "lights"},
		func(context.Context, kit.Ctx, struct{}) (struct{}, error) {
			state.Range(func(k, _ any) bool { state.Store(k, 0); return true })
			return struct{}{}, nil
		})
	kit.Read(m, kit.ReadSpec{Name: "lamps", Path: "/v1/lights"},
		func(_ context.Context, _ kit.Ctx, p listParams) ([]lamp, error) {
			out := []lamp{}
			state.Range(func(k, v any) bool {
				if b := v.(int); b >= p.Min && (!p.Only || b > 0) {
					out = append(out, lamp{k.(string), b})
				}
				return true
			})
			return out, nil
		})
	return m, state
}

func engine(t *testing.T, m sdk.Module) *core.Engine {
	t.Helper()
	reg := core.NewRegistry()
	if err := reg.Add(m); err != nil {
		t.Fatal(err)
	}
	e := core.New(reg, core.Options{})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Stop(context.Background()) })
	return e
}

var admin = core.Auth{Name: "test", Scopes: []string{sdk.ScopeAdmin}}

func do(t *testing.T, e *core.Engine, typ, args string) (sdk.Result, error) {
	t.Helper()
	a := sdk.Action{Type: typ, Source: sdk.Source{Kind: sdk.SourceManual, Name: "test"}}
	if args != "" {
		a.Args = json.RawMessage(args)
	}
	return e.Submit(context.Background(), a, admin)
}

func TestManifestIsValidAndGenerated(t *testing.T) {
	m, _ := lights()
	man := m.Manifest()
	if err := man.Validate(); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	set, _ := man.Action("lights.set")
	if set.Route == nil || set.Route.Method != "POST" || set.Route.Path != "/v1/lights/{lamp}" {
		t.Fatalf("route = %+v", set.Route)
	}
	if !strings.Contains(string(set.Schema), `"maximum":100`) || !strings.Contains(string(set.Schema), `"required":["lamp","brightness"]`) {
		t.Fatalf("schema = %s", set.Schema)
	}
	off, _ := man.Action("lights.off")
	if off.Schema != nil {
		t.Fatalf("an action with no arguments has no schema: %s", off.Schema)
	}
	if len(man.Reads) != 1 || !strings.Contains(string(man.Reads[0].Schema), `"array"`) {
		t.Fatalf("read schema missing: %+v", man.Reads)
	}
}

func TestActionsRunThroughTheCore(t *testing.T) {
	m, state := lights()
	e := engine(t, m)

	res, err := do(t, e, "lights.set", `{"lamp":"porch","brightness":40}`)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Data) != `{"lamp":"porch","brightness":40}` {
		t.Fatalf("data = %s", res.Data)
	}
	if v, _ := state.Load("porch"); v != 40 {
		t.Fatalf("state = %v", v)
	}
	if got := e.Version("lamp:porch"); got != 1 {
		t.Fatalf("event on a resource should version it: %d", got)
	}

	// The schema, from the Go type, refuses what it should before Handle.
	_, err = do(t, e, "lights.set", `{"lamp":"porch","brightness":400}`)
	if sdk.CodeOf(err) != sdk.CodeInvalidArgs {
		t.Fatalf("err = %v, want invalid_args", err)
	}
	// Validate runs with the keys held.
	_, err = do(t, e, "lights.set", `{"lamp":"ghost","brightness":1}`)
	if sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("err = %v, want not_found", err)
	}

	res, err = do(t, e, "lights.off", "")
	if err != nil || res.Data != nil {
		t.Fatalf("off: %v data=%s", err, res.Data)
	}
	if v, _ := state.Load("porch"); v != 0 {
		t.Fatalf("off left %v", v)
	}
}

func TestReadsDecodeTypedParams(t *testing.T) {
	m, _ := lights()
	e := engine(t, m)
	for _, c := range []struct {
		lamp string
		b    int
	}{{"a", 10}, {"b", 0}, {"c", 70}} {
		if _, err := do(t, e, "lights.set", fmt.Sprintf(`{"lamp":%q,"brightness":%d}`, c.lamp, c.b)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := e.ModuleRead(context.Background(), "lights", "lamps", map[string]string{"min": "50", "only": "true"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got)
	if string(b) != `[{"lamp":"c","brightness":70}]` {
		t.Fatalf("read = %s", b)
	}
	if _, err := e.ModuleRead(context.Background(), "lights", "lamps", map[string]string{"min": "lots"}); sdk.CodeOf(err) != sdk.CodeInvalidArgs {
		t.Fatalf("bad param: %v", err)
	}
}

func TestLifecycleAndMistakes(t *testing.T) {
	var log []string
	m := kit.New("x", "1").Owns("x.*").
		OnStart(func(context.Context, kit.Ctx) error { log = append(log, "start1"); return nil }).
		OnStart(func(_ context.Context, c kit.Ctx) error {
			if c.Core == nil {
				t.Error("no Core in Start")
			}
			log = append(log, "start2")
			return nil
		}).
		OnStop(func(context.Context) error { log = append(log, "stop1"); return nil }).
		OnStop(func(context.Context) error { log = append(log, "stop2"); return nil }).
		WithState(func(context.Context, kit.Ctx) (any, error) { return map[string]int{"n": 1}, nil })
	e := engine(t, m)
	st, err := e.ModuleState(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(st); string(b) != `{"n":1}` {
		t.Fatalf("state = %s", b)
	}
	_ = e.Stop(context.Background())
	if got := strings.Join(log, ","); got != "start1,start2,stop2,stop1" {
		t.Fatalf("lifecycle order = %s", got)
	}

	for name, f := range map[string]func(){
		"twice": func() {
			mm := kit.New("y", "1")
			h := func(context.Context, kit.Ctx, struct{}) (struct{}, error) { return struct{}{}, nil }
			kit.Action(mm, kit.Spec{Type: "y.a", Scope: "read"}, h)
			kit.Action(mm, kit.Spec{Type: "y.a", Scope: "read"}, h)
		},
		"route": func() {
			kit.Action(kit.New("y", "1"), kit.Spec{Type: "y.a", Scope: "read", Route: "nonsense"},
				func(context.Context, kit.Ctx, struct{}) (struct{}, error) { return struct{}{}, nil })
		},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			f()
		}()
	}
}

type sceneSpec struct {
	Volume int `json:"volume" min:"0" max:"150"`
}

func TestKindsAreReconciled(t *testing.T) {
	var applied []string
	m := kit.New("scenes", "1").Owns("scenes.*")
	kit.Action(m, kit.Spec{Type: "scenes.apply", Scope: sdk.ScopeAdmin},
		func(context.Context, kit.Ctx, struct {
			Name string `json:"name"`
		}) (struct{}, error) {
			return struct{}{}, nil
		})
	kit.Kind(m, kit.KindSpec{Kind: "scene", Plural: "scenes", Verbs: []sdk.VerbSpec{{Name: "apply", Action: "scenes.apply"}}},
		func(_ context.Context, _ kit.Ctx, r kit.Res[sceneSpec]) (sdk.ResourceStatus, error) {
			applied = append(applied, r.Name)
			if r.Spec.Volume == 99 {
				return sdk.ResourceStatus{}, errors.New("99 is too loud")
			}
			return sdk.ResourceStatus{}, nil
		}, func(_ context.Context, _ kit.Ctx, name string) error {
			applied = append(applied, "-"+name)
			return nil
		})
	kit.CheckKind(m, "scene", func(_ context.Context, _ kit.Ctx, r kit.Res[sceneSpec]) error {
		if r.Spec.Volume == 1 {
			return errors.New("too quiet")
		}
		return nil
	})

	man := m.Manifest()
	if err := man.Validate(); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	k, ok := man.Kind("scene")
	if !ok || !strings.Contains(string(k.Schema), `"maximum":150`) {
		t.Fatalf("kind = %+v", k)
	}
	ctx := context.Background()
	res := sdk.Resource{Kind: "scene", Name: "movie", Spec: json.RawMessage(`{"volume":40}`)}
	if _, err := m.Apply(ctx, res); err != nil {
		t.Fatal(err)
	}
	res.Spec = json.RawMessage(`{"volume":99}`)
	if _, err := m.Apply(ctx, res); err == nil {
		t.Fatal("the module's refusal was lost")
	}
	if err := m.Delete(ctx, "scene", "movie"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(applied, ","); got != "movie,movie,-movie" {
		t.Fatalf("calls = %s", got)
	}
	res.Spec = json.RawMessage(`{"volume":1}`)
	if err := m.Check(ctx, res); err == nil {
		t.Fatal("check should refuse")
	}
	if _, err := m.Apply(ctx, sdk.Resource{Kind: "ghost", Name: "x"}); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("unknown kind: %v", err)
	}
	if err := m.Delete(ctx, "ghost", "x"); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("unknown kind delete: %v", err)
	}
}
