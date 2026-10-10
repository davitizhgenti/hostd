package audio

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

// fakeGraphAudio is fakeAudio with outputs and streams.
type fakeGraphAudio struct {
	*fakeAudio
	gmu   sync.Mutex
	graph Graph
	calls []string
}

func (f *fakeGraphAudio) Graph(context.Context) (Graph, error) {
	f.gmu.Lock()
	defer f.gmu.Unlock()
	g := Graph{Outputs: append([]Output(nil), f.graph.Outputs...), Streams: append([]Stream(nil), f.graph.Streams...)}
	return g, nil
}
func (f *fakeGraphAudio) call(s string) { f.gmu.Lock(); f.calls = append(f.calls, s); f.gmu.Unlock() }
func (f *fakeGraphAudio) SetDefault(_ context.Context, id int) error {
	f.call(fmt.Sprintf("default %d", id))
	f.gmu.Lock()
	for i := range f.graph.Outputs {
		f.graph.Outputs[i].Default = f.graph.Outputs[i].ID == id
	}
	f.gmu.Unlock()
	return nil
}
func (f *fakeGraphAudio) MoveStream(_ context.Context, s int, node string) error {
	f.call(fmt.Sprintf("move %d %s", s, node))
	return nil
}
func (f *fakeGraphAudio) SetNodeVolume(_ context.Context, id, p int) error {
	f.call(fmt.Sprintf("volume %d %d", id, p))
	f.gmu.Lock()
	for i := range f.graph.Streams {
		if f.graph.Streams[i].ID == id {
			f.graph.Streams[i].Percent = p
		}
	}
	f.gmu.Unlock()
	return nil
}
func (f *fakeGraphAudio) SetNodeMute(_ context.Context, id int, m bool) error {
	f.call(fmt.Sprintf("mute %d %v", id, m))
	f.gmu.Lock()
	for i := range f.graph.Streams {
		if f.graph.Streams[i].ID == id {
			f.graph.Streams[i].Muted = m
		}
	}
	f.gmu.Unlock()
	return nil
}
func (f *fakeGraphAudio) taken() []string {
	f.gmu.Lock()
	defer f.gmu.Unlock()
	out := f.calls
	f.calls = nil
	return out
}

type m3Rig struct {
	e    *core.Engine
	m    *Module
	f    *fakeGraphAudio
	live []contract.Instance
	lmu  sync.Mutex
}

// fakeProcs writes /proc/<pid>/cgroup and environ.
func fakeProcs(t *testing.T, procs map[int][2]string) string {
	root := t.TempDir()
	for pid, p := range procs {
		dir := filepath.Join(root, fmt.Sprint(pid))
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "cgroup"), []byte(p[0]+"\n"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "environ"), []byte(strings.ReplaceAll(p[1], " ", "\x00")), 0o644)
	}
	return root
}

func newM3Rig(t *testing.T, media Media) *m3Rig {
	t.Helper()
	unit := "0::/user.slice/user-1000.slice/user@1000.service/app.slice/"
	proc := fakeProcs(t, map[int][2]string{
		100: {unit + "hostd-browser.service", "HOME=/x"},
		200: {unit + "app-flatpak-com.valvesoftware.Steam-1.scope", "FLATPAK_ID=com.valvesoftware.Steam SteamAppId=620"},
		300: {unit + "app-flatpak-com.valvesoftware.Steam-1.scope", "FLATPAK_ID=com.valvesoftware.Steam"},
	})
	r := &m3Rig{f: &fakeGraphAudio{fakeAudio: newFakeAudio(true)}}
	r.f.graph = Graph{
		Outputs: []Output{{Name: "hdmi", Node: "alsa_output.hdmi", ID: 54, Default: true, Percent: 50},
			{Name: "speakers", Node: "alsa_output.analog", ID: 55, Percent: 80}},
		Streams: []Stream{{ID: 70, PID: 100, App: "Chromium", Percent: 100}, {ID: 71, PID: 200, App: "portal2", Percent: 100}},
	}
	r.live = []contract.Instance{
		{ID: "browser", App: "browser", State: "running"},
		{ID: "steam", App: "steam", State: "running", Match: &contract.Match{Env: "FLATPAK_ID=com.valvesoftware.Steam"}},
		{ID: "portal2", App: "portal2", State: "running", Match: &contract.Match{Class: "steam_app_620", Env: "SteamAppId=620"}},
	}
	r.m = New(Options{Backend: r.f, Clock: clock.NewFake(time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)), ProcRoot: proc, Media: media})
	apps := testutil.NewModule("apps", nil, nil, nil)
	apps.M.Reads = []sdk.ReadSpec{{Name: "instances", Path: "/v1/instances"}}
	apps.ReadFunc = func(context.Context, string, map[string]string) (any, error) {
		r.lmu.Lock()
		defer r.lmu.Unlock()
		return append([]contract.Instance(nil), r.live...), nil
	}
	reg := core.NewRegistry()
	for _, mod := range []sdk.Module{apps, r.m} {
		if err := reg.Add(mod); err != nil {
			t.Fatal(err)
		}
	}
	r.e = core.New(reg, core.Options{})
	if err := r.e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.e.Stop(context.Background()) })
	waitFor(t, "audio", func() bool {
		st, _ := r.m.Read(context.Background(), "audio", nil)
		return len(st.(Status).Outputs) == 2
	})
	r.f.taken()
	return r
}

func (r *m3Rig) do(t *testing.T, typ, args string) (sdk.Result, error) {
	t.Helper()
	return r.e.Submit(context.Background(), sdk.Action{Type: typ, Args: json.RawMessage(args), Source: sdk.Source{Kind: sdk.SourceManual}},
		core.Auth{Scopes: []string{sdk.ScopeAdmin}})
}

func TestOutputSet(t *testing.T) {
	r := newM3Rig(t, nil)
	events := r.e.Subscribe(t.Context(), EventOutputsChanged)
	if _, err := r.do(t, "audio.output.set", `{"output":"speakers"}`); err != nil {
		t.Fatal(err)
	}
	want := []string{"default 55", "move 70 alsa_output.analog", "move 71 alsa_output.analog"}
	if got := r.f.taken(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls %q, want %q", got, want)
	}
	select {
	case <-events:
	case <-time.After(5 * time.Second):
		t.Fatal("no audio.outputs.changed")
	}
	_, err := r.do(t, "audio.output.set", `{"output":"tv"}`)
	if sdk.CodeOf(err) != sdk.CodeNotFound || !strings.Contains(err.Error(), "hdmi, speakers") {
		t.Fatalf("unknown output: %v", err)
	}
}

func TestAppVolumeAndStreams(t *testing.T) {
	r := newM3Rig(t, nil)
	st, _ := r.m.Read(context.Background(), "audio", nil)
	streams := st.(Status).Streams
	// The browser by its unit; the game's process has Steam's FLATPAK_ID
	// too, but the game's rules are more specific.
	if streams[0].Instance != "browser" || streams[1].Instance != "portal2" {
		t.Fatalf("streams %+v", streams)
	}
	if _, err := r.do(t, "audio.app.volume.set", `{"instance":"portal2","percent":40}`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.do(t, "audio.app.volume.set", `{"instance":"portal2","percent":"-5"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.do(t, "audio.app.mute.set", `{"instance":"browser","muted":"toggle"}`); err != nil {
		t.Fatal(err)
	}
	if got := r.f.taken(); !reflect.DeepEqual(got, []string{"volume 71 40", "volume 71 35", "mute 70 true"}) {
		t.Fatalf("calls %q", got)
	}
	if _, err := r.do(t, "audio.app.volume.set", `{"instance":"steam","percent":10}`); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("app without sound: %v", err)
	}
}

func TestAppOwnVolumeWhenItStartsPlaying(t *testing.T) {
	r := newM3Rig(t, nil)
	v := 30
	r.lmu.Lock()
	r.live[1].Volume = &v // Steam's own volume
	r.lmu.Unlock()
	r.f.gmu.Lock()
	r.f.graph.Streams = append(r.f.graph.Streams, Stream{ID: 72, PID: 300, App: "steam", Percent: 100})
	r.f.gmu.Unlock()
	r.m.followGraph(context.Background())
	if got := r.f.taken(); !reflect.DeepEqual(got, []string{"volume 72 30"}) {
		t.Fatalf("calls %q", got)
	}
	r.m.followGraph(context.Background()) // once only: the person may change it afterwards
	if got := r.f.taken(); len(got) != 0 {
		t.Fatalf("applied again: %q", got)
	}
}
