package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/internal/clock"
	"github.com/davitizhgenti/hostd/internal/testutil"
	"github.com/davitizhgenti/hostd/sdk"
)

// world is the fake state of the apps, audio and display modules, which
// their fake actions change.
type world struct {
	mu        sync.Mutex
	instances []contract.Instance
	volume    int
	muted     bool
	output    string
	outputs   []string
	focused   string
	power     bool
	lastInput time.Time
	sent      []string // actions handled, "type args"
	fail      map[string]bool
	minted    []string // script tokens: "name scopes"
	mods      map[string]*testutil.Module
	revoked   int
}

type fakeTokens struct{ w *world }

func (w *world) tokens() fakeTokens { return fakeTokens{w} }

func (t fakeTokens) Mint(_ context.Context, name string, scopes []string, _ time.Duration) (string, func(), error) {
	t.w.mu.Lock()
	defer t.w.mu.Unlock()
	t.w.minted = append(t.w.minted, name+" "+strings.Join(scopes, ","))
	return "hostd_secret", func() { t.w.mu.Lock(); t.w.revoked++; t.w.mu.Unlock() }, nil
}

var anyArgs = json.RawMessage(`{"type":"object","additionalProperties":true}`)

func (w *world) handle(fn func(a sdk.Action, args map[string]any) error) func(context.Context, sdk.Action) (sdk.Result, error) {
	return func(_ context.Context, a sdk.Action) (sdk.Result, error) {
		var args map[string]any
		_ = json.Unmarshal(a.Args, &args)
		w.mu.Lock()
		defer w.mu.Unlock()
		w.sent = append(w.sent, a.Type+" "+string(a.Args))
		if w.fail[a.Type] {
			return sdk.Result{}, sdk.Errorf(sdk.CodeInternal, "%s broke", a.Type)
		}
		return sdk.Result{}, fn(a, args)
	}
}

func (w *world) modules() []sdk.Module {
	apps := testutil.NewModule(contract.AppsModule, nil, nil, nil)
	apps.M.Owns = []string{"app.*", "instance.*"}
	apps.M.Scopes = []sdk.ScopeSpec{{Name: "apps"}}
	apps.M.Actions = []sdk.ActionSpec{
		{Type: contract.ActionAppStart, Scope: "apps", Schema: anyArgs, Keys: []sdk.KeyTemplate{"app:{id}"}},
		{Type: contract.ActionInstanceStop, Scope: "apps", Schema: anyArgs, Keys: []sdk.KeyTemplate{"instance:{id}"}},
	}
	apps.M.Reads = []sdk.ReadSpec{{Name: contract.ReadInstances, Path: "/v1/instances"}}
	for _, e := range []string{"instance.starting", "instance.started", "instance.exited", "instance.failed", "instance.closing"} {
		apps.M.Events = append(apps.M.Events, sdk.EventSpec{Type: e})
	}
	apps.ReadFunc = func(context.Context, string, map[string]string) (any, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		return append([]contract.Instance{}, w.instances...), nil
	}
	apps.HandleFunc = w.handle(func(a sdk.Action, args map[string]any) error {
		id := args["id"].(string)
		if a.Type == contract.ActionAppStart {
			w.instances = append(w.instances, contract.Instance{ID: id, App: id, State: "running"})
			w.focused = id
			return nil
		}
		for i, in := range w.instances {
			if in.ID == id {
				w.instances = append(w.instances[:i], w.instances[i+1:]...)
				break
			}
		}
		return nil
	})

	audio := testutil.NewModule("audio", nil, nil, nil)
	audio.M.Scopes = []sdk.ScopeSpec{{Name: "audio"}}
	audio.M.Actions = []sdk.ActionSpec{
		{Type: "audio.volume.set", Scope: "audio", Schema: anyArgs, Keys: []sdk.KeyTemplate{"audio.master"}},
		{Type: "audio.mute.set", Scope: "audio", Schema: anyArgs, Keys: []sdk.KeyTemplate{"audio.master"}},
		{Type: "audio.output.set", Scope: "audio", Schema: anyArgs, Keys: []sdk.KeyTemplate{"audio.output"}},
	}
	audio.M.Reads = []sdk.ReadSpec{{Name: "audio", Path: "/v1/audio"}}
	audio.ReadFunc = func(context.Context, string, map[string]string) (any, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		outs := []map[string]any{}
		for _, o := range w.outputs {
			outs = append(outs, map[string]any{"name": o, "default": o == w.output})
		}
		return map[string]any{"percent": w.volume, "muted": w.muted, "outputs": outs}, nil
	}
	audio.HandleFunc = w.handle(func(a sdk.Action, args map[string]any) error {
		switch a.Type {
		case "audio.volume.set":
			w.volume = int(args["percent"].(float64))
		case "audio.mute.set":
			w.muted = args["muted"].(bool)
		default:
			w.output = args["output"].(string)
		}
		return nil
	})

	display := testutil.NewModule("display", nil, nil, nil)
	display.M.Owns = []string{"display.*", "window.*"}
	display.M.Scopes = []sdk.ScopeSpec{{Name: "display"}}
	display.M.Actions = []sdk.ActionSpec{
		{Type: "window.focus", Scope: "display", Schema: anyArgs, Keys: []sdk.KeyTemplate{"display.focus"}},
		{Type: "display.power", Scope: "display", Schema: anyArgs, Keys: []sdk.KeyTemplate{"display.outputs"}},
	}
	display.M.Reads = []sdk.ReadSpec{{Name: "display", Path: "/v1/display"}}
	display.M.Events = []sdk.EventSpec{{Type: "display.idle"}}
	display.ReadFunc = func(context.Context, string, map[string]string) (any, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		st := map[string]any{"focused_instance": w.focused,
			"outputs": []map[string]any{{"name": "HDMI-A-1", "active": true, "power": w.power}}}
		if !w.lastInput.IsZero() {
			st["last_input"] = w.lastInput
		}
		return st, nil
	}
	display.HandleFunc = w.handle(func(a sdk.Action, args map[string]any) error {
		if a.Type == "window.focus" {
			w.focused = args["instance"].(string)
		} else {
			w.power = args["on"].(bool)
		}
		return nil
	})
	w.mods = map[string]*testutil.Module{"apps": apps, "audio": audio, "display": display}
	return []sdk.Module{apps, audio, display}
}

func (w *world) takeSent() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.sent
	w.sent = nil
	return s
}

type rig struct {
	audit *core.MemoryAudit
	e     *core.Engine
	m     *Module
	w     *world
	dir   string
	clock *clock.Fake
}

func newRig(t *testing.T, w *world) *rig {
	t.Helper()
	dir := t.TempDir()
	clk := clock.NewFake(time.Date(2026, 10, 11, 20, 0, 0, 0, time.UTC))
	m := New(Options{ScenesDir: dir, RulesFile: filepath.Join(dir, "rules.toml"), ScriptsDir: filepath.Join(dir, "scripts"),
		Location: time.UTC, Clock: clk, Limiter: core.NewRuleLimiter(clk), Tokens: w.tokens()})
	reg := core.NewRegistry()
	for _, mod := range append(w.modules(), m) {
		if err := reg.Add(mod); err != nil {
			t.Fatal(err)
		}
	}
	audit := core.NewMemoryAudit(1000)
	e := core.New(reg, core.Options{Clock: clk, Audit: audit})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Stop(context.Background()) })
	return &rig{e: e, m: m, w: w, dir: dir, clock: clk, audit: audit}
}

func (r *rig) scene(t *testing.T, id, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, id+".toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (r *rig) apply(t *testing.T, id string, kind sdk.SourceKind) (Applied, error) {
	t.Helper()
	res, err := r.e.Submit(context.Background(), sdk.Action{Type: "scene.apply", Args: sdk.MustJSON(map[string]string{"id": id}),
		Source: sdk.Source{Kind: kind, Name: "test"}}, core.Auth{Scopes: []string{sdk.ScopeAdmin}})
	if err != nil {
		return Applied{}, err
	}
	var out Applied
	_ = json.Unmarshal(res.Data, &out)
	return out, nil
}

var adminAuth = core.Auth{Scopes: []string{sdk.ScopeAdmin}}

func summary(a Applied) string {
	var parts []string
	for _, s := range a.Steps {
		p := s.Step + ":" + s.Status
		if s.Reason != "" {
			p += "(" + s.Reason + ")"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, " ")
}

const movie = `
name = "Movie night"
[audio]
output = "hdmi"
volume = 40
muted = false
[apps]
stop    = ["game"]
running = ["jellyfin", "lights"]
focus   = "jellyfin"
[display]
power = "on"
`

func TestSceneOrderAndIdempotence(t *testing.T) {
	w := &world{volume: 70, muted: true, output: "speakers", outputs: []string{"speakers", "hdmi"}, power: false,
		instances: []contract.Instance{{ID: "game", App: "game", State: "running"}, {ID: "lights", App: "lights", State: "running"}},
		focused:   "game"}
	r := newRig(t, w)
	r.scene(t, "movie", movie)

	got, err := r.apply(t, "movie", sdk.SourceManual)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`instance.stop {"id":"game"}`,
		`audio.output.set {"output":"hdmi"}`,
		`audio.volume.set {"percent":40}`,
		`audio.mute.set {"muted":false}`,
		`app.start {"id":"jellyfin"}`, // lights already runs
		`display.power {"on":true}`,   // jellyfin came to the front when it started
	}
	if sent := w.takeSent(); fmt.Sprint(sent) != fmt.Sprint(want) {
		t.Fatalf("sent\n%s\nwant\n%s", strings.Join(sent, "\n"), strings.Join(want, "\n"))
	}
	if summary(got) != "stop:applied output:applied volume:applied mute:applied start:applied power:applied" {
		t.Fatalf("result %s", summary(got))
	}

	// Reached: the second time sends nothing.
	got, err = r.apply(t, "movie", sdk.SourceManual)
	if err != nil || len(got.Steps) != 0 || len(w.takeSent()) != 0 {
		t.Fatalf("second apply: %+v %v", got, err)
	}

	// Something else in front: only a focus.
	w.mu.Lock()
	w.focused = "lights"
	w.mu.Unlock()
	if _, err := r.apply(t, "movie", sdk.SourceManual); err != nil {
		t.Fatal(err)
	}
	if sent := w.takeSent(); fmt.Sprint(sent) != `[window.focus {"instance":"jellyfin"}]` {
		t.Fatalf("refocus %v", sent)
	}
}

func TestScenePartialFailure(t *testing.T) {
	w := &world{volume: 70, output: "speakers", outputs: []string{"speakers"}, power: true,
		fail: map[string]bool{"audio.output.set": true}}
	r := newRig(t, w)
	r.scene(t, "s", "[audio]\noutput = \"hdmi\"\nvolume = 20\n[apps]\nrunning = [\"tv\"]\n")
	got, err := r.apply(t, "s", sdk.SourceManual)
	if err != nil {
		t.Fatal(err)
	}
	if summary(got) != "output:failed volume:applied start:applied" || !strings.Contains(got.Steps[0].Error, "broke") {
		t.Fatalf("result %s %+v", summary(got), got.Steps)
	}
}

func TestSceneHeldByManual(t *testing.T) {
	// A rule's scene does not undo what someone set by hand just before.
	w := &world{volume: 70}
	r := newRig(t, w)
	r.scene(t, "quiet", "[audio]\nvolume = 20\n")
	if _, err := r.e.Submit(context.Background(), sdk.Action{Type: "audio.volume.set", Args: json.RawMessage(`{"percent":55}`),
		Source: sdk.Source{Kind: sdk.SourceManual, Name: "phone"}}, core.Auth{Scopes: []string{sdk.ScopeAdmin}}); err != nil {
		t.Fatal(err)
	}
	got, err := r.apply(t, "quiet", sdk.SourceAutomation)
	if err != nil {
		t.Fatal(err)
	}
	if summary(got) != "volume:skipped(held by manual)" || w.volume != 55 {
		t.Fatalf("result %s, volume %d", summary(got), w.volume)
	}
	r.clock.Advance(4 * time.Minute) // the hold is over
	if got, _ := r.apply(t, "quiet", sdk.SourceAutomation); summary(got) != "volume:applied" || w.volume != 20 {
		t.Fatalf("after the hold: %s, volume %d", summary(got), w.volume)
	}
}

func TestSceneFiles(t *testing.T) {
	r := newRig(t, &world{})
	r.scene(t, "movie", movie)
	r.scene(t, "bad", "[audio]\nvolume = 400\n")
	r.scene(t, "typo", "[audoi]\nvolume = 4\n")
	r.scene(t, "clash", "[apps]\nstop = [\"a\"]\nrunning = [\"a\"]\n")
	v, err := r.m.Read(context.Background(), "scenes", nil)
	if err != nil {
		t.Fatal(err)
	}
	list := v.(SceneList)
	if len(list.Scenes) != 1 || list.Scenes[0].Name != "Movie night" || len(list.Problems) != 3 {
		t.Fatalf("list %+v", list)
	}
	if _, err := r.apply(t, "bad", sdk.SourceManual); sdk.CodeOf(err) != sdk.CodeInvalidArgs || !strings.Contains(err.Error(), "0-150") {
		t.Fatalf("bad scene: %v", err)
	}
	if _, err := r.apply(t, "nope", sdk.SourceManual); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("missing scene: %v", err)
	}
}
