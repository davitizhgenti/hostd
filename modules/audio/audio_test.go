package audio

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/internal/clock"
	"github.com/davitizhgenti/hostd/sdk"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestParseVolume(t *testing.T) {
	for out, want := range map[string]Master{
		"Volume: 0.40\n":         {Percent: 40},
		"Volume: 0.40 [MUTED]\n": {Percent: 40, Muted: true},
		"Volume: 1.25\n":         {Percent: 125},
		"Volume: 0.335\n":        {Percent: 34},
		"W 10:14:03.4 mod.rt ../src/modules/module-rt.c:995: RTKit does not give us MaxRealtimePriority\nVolume: 0.70\n": {Percent: 70},
	} {
		got, err := parseVolume([]byte(out))
		if err != nil || got != want {
			t.Errorf("parseVolume(%q) = %+v %v, want %+v", out, got, err, want)
		}
	}
	for _, bad := range []string{"", "Volume:", "Translate: 0.4", "Volume: loud"} {
		if _, err := parseVolume([]byte(bad)); err == nil {
			t.Errorf("parseVolume(%q) accepted", bad)
		}
	}
}

func TestParseInspect(t *testing.T) {
	data, err := os.ReadFile("testdata/wpctl-inspect.txt") // captured from the devbox
	if err != nil {
		t.Fatal(err)
	}
	p := parseInspect(data)
	if p["node.name"] != "fake-hdmi" || p["node.description"] != "Fake HDMI (TV)" || p["media.class"] != "Audio/Sink" {
		t.Fatalf("props = %v", p)
	}
}

func TestTarget(t *testing.T) {
	for _, tc := range []struct {
		arg     string
		current int
		want    int
		err     bool
	}{
		{`40`, 70, 40, false},
		{`"40"`, 70, 40, false},
		{`"+5"`, 40, 45, false},
		{`"-5"`, 40, 35, false},
		{`"-50"`, 40, 0, false},    // clamped
		{`"+200"`, 40, 150, false}, // clamped
		{`"loud"`, 40, 0, true},
		{`true`, 40, 0, true},
	} {
		got, err := target(json.RawMessage(tc.arg), tc.current)
		if (err != nil) != tc.err || got != tc.want {
			t.Errorf("target(%s, %d) = %d, %v; want %d", tc.arg, tc.current, got, err, tc.want)
		}
	}
}

func TestSchemas(t *testing.T) {
	vol, _ := sdk.CompileArgsSchema(volumeSchema)
	for args, ok := range map[string]bool{
		`{"percent":40}`: true, `{"percent":150}`: true, `{"percent":"+5"}`: true, `{"percent":"-5"}`: true,
		`{"percent":"40"}`: true, `{"percent":151}`: false, `{"percent":-5}`: false, `{"percent":"loud"}`: false,
		`{"percent":"+1000"}`: false, `{}`: false,
	} {
		if err := vol.Validate(json.RawMessage(args)); (err == nil) != ok {
			t.Errorf("volume %s: %v", args, err)
		}
	}
	mute, _ := sdk.CompileArgsSchema(muteSchema)
	for args, ok := range map[string]bool{
		`{"muted":true}`: true, `{"muted":false}`: true, `{"muted":"toggle"}`: true, `{"muted":"yes"}`: false, `{"muted":1}`: false,
	} {
		if err := mute.Validate(json.RawMessage(args)); (err == nil) != ok {
			t.Errorf("mute %s: %v", args, err)
		}
	}
}

// --- the module, with a fake audio system ------------------------------------

type fakeAudio struct {
	mu      sync.Mutex
	up      bool
	state   Master
	sets    []string
	cues    chan struct{}
	monitor chan struct{} // closed: the monitor stops
	watches int           // Watch calls running
}

func (f *fakeAudio) watching() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.watches
}

func newFakeAudio(up bool) *fakeAudio {
	return &fakeAudio{up: up, state: Master{Percent: 70, Sink: "hdmi", Description: "HDMI"},
		cues: make(chan struct{}, 16), monitor: make(chan struct{})}
}

func (f *fakeAudio) Master(context.Context) (Master, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.up {
		return Master{}, sdk.Errorf(sdk.CodeModuleUnavailable, "PipeWire is not running")
	}
	return f.state, nil
}
func (f *fakeAudio) SetVolume(_ context.Context, p int) error {
	f.mu.Lock()
	f.state.Percent = p
	f.sets = append(f.sets, "volume "+itoa(p))
	f.mu.Unlock()
	f.cues <- struct{}{} // PipeWire echoes the change
	return nil
}
func (f *fakeAudio) SetMute(_ context.Context, m bool) error {
	f.mu.Lock()
	f.state.Muted = m
	f.sets = append(f.sets, "mute "+map[bool]string{true: "on", false: "off"}[m])
	f.mu.Unlock()
	f.cues <- struct{}{}
	return nil
}

// killMonitor ends the running monitor, as pw-dump exiting would.
func (f *fakeAudio) killMonitor() {
	f.mu.Lock()
	defer f.mu.Unlock()
	close(f.monitor)
	f.monitor = make(chan struct{})
}

func (f *fakeAudio) Watch(ctx context.Context, fn func()) error {
	f.mu.Lock()
	dead := f.monitor
	f.watches++
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.watches--; f.mu.Unlock() }()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-dead:
			return errors.New("pw-dump exited")
		case <-f.cues:
			fn()
		}
	}
}

// outside changes the volume the way a game's own slider would.
func (f *fakeAudio) outside(p int) {
	f.mu.Lock()
	f.state.Percent = p
	f.mu.Unlock()
	f.cues <- struct{}{}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

type rig struct {
	e      *core.Engine
	m      *Module
	f      *fakeAudio
	clock  *clock.Fake
	events <-chan sdk.Event
	audit  *core.MemoryAudit
}

func newRig(t *testing.T, up bool) *rig {
	t.Helper()
	r := &rig{f: newFakeAudio(up), clock: clock.NewFake(time.Date(2026, 10, 8, 21, 0, 0, 0, time.UTC)), audit: core.NewMemoryAudit(100)}
	r.m = New(Options{Backend: r.f, Clock: r.clock})
	reg := core.NewRegistry()
	if err := reg.Add(r.m); err != nil {
		t.Fatal(err)
	}
	r.e = core.New(reg, core.Options{Clock: r.clock, Audit: r.audit})
	if err := r.e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.events = r.e.Subscribe(ctx, EventVolumeChanged)
	t.Cleanup(func() { cancel(); _ = r.e.Stop(context.Background()) })
	if up {
		waitFor(t, "audio", r.available)
	}
	return r
}

func (r *rig) available() bool {
	st, _ := r.m.Read(context.Background(), "audio", nil)
	return st.(Status).Available
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func (r *rig) set(t *testing.T, typ, args string, kind sdk.SourceKind) (Master, sdk.Result, error) {
	t.Helper()
	res, err := r.e.Submit(context.Background(), sdk.Action{Type: typ, Args: json.RawMessage(args),
		Source: sdk.Source{Kind: kind, Name: "test"}}, core.Auth{Scopes: []string{sdk.ScopeAdmin}})
	var m Master
	_ = json.Unmarshal(res.Data, &m)
	return m, res, err
}

func (r *rig) event(t *testing.T) sdk.Event {
	t.Helper()
	select {
	case ev := <-r.events:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no volume event")
		return sdk.Event{}
	}
}

func (r *rig) noEvent(t *testing.T) {
	t.Helper()
	select {
	case ev := <-r.events:
		t.Fatalf("unexpected event %+v (%s)", ev, ev.Data)
	case <-time.After(50 * time.Millisecond):
	}
}

// settle lets the module's settle timer run, so it reads the state after
// a cue.
func (r *rig) settle() {
	r.clock.Advance(200 * time.Millisecond)
}

func TestVolumeAndMute(t *testing.T) {
	r := newRig(t, true)
	got, res, err := r.set(t, "audio.volume.set", `{"percent":40}`, sdk.SourceManual)
	if err != nil || got.Percent != 40 || res.Version != 1 {
		t.Fatalf("%+v %+v %v", got, res, err)
	}
	if ev := r.event(t); ev.Action == "" || ev.Version != 1 || ev.Source.Kind != sdk.SourceManual {
		t.Fatalf("event %+v", ev)
	}
	got, _, _ = r.set(t, "audio.volume.set", `{"percent":"+5"}`, sdk.SourceManual)
	if got.Percent != 45 {
		t.Fatalf("+5 from 40 = %d", got.Percent)
	}
	r.event(t)
	got, _, _ = r.set(t, "audio.mute.set", `{"muted":"toggle"}`, sdk.SourceManual)
	if !got.Muted {
		t.Fatal("toggle did not mute")
	}
	r.event(t)
	got, _, _ = r.set(t, "audio.mute.set", `{"muted":"toggle"}`, sdk.SourceManual)
	if got.Muted {
		t.Fatal("toggle did not unmute")
	}
	r.event(t)
	// PipeWire echoes each change; the echoes are not outside changes.
	r.settle()
	r.noEvent(t)
}

func TestOutsideChangeIsObservedAndNeverHolds(t *testing.T) {
	r := newRig(t, true)
	r.f.outside(55) // a game moved its own slider
	waitFor(t, "cue", func() bool { return r.clock.Waiters() > 0 })
	r.settle()
	ev := r.event(t)
	var m Master
	_ = json.Unmarshal(ev.Data, &m)
	if ev.Action != "" || ev.Source.Kind != sdk.SourceExternal || ev.Source.Name != "pipewire" || m.Percent != 55 || ev.Version != 1 {
		t.Fatalf("observed event %+v %s", ev, ev.Data)
	}
	// An outside change does not hold the volume: automation may change it.
	if _, res, err := r.set(t, "audio.volume.set", `{"percent":30}`, sdk.SourceAutomation); err != nil || res.Status != sdk.StatusApplied {
		t.Fatalf("automation after an outside change: %+v %v", res, err)
	}
	found := false
	for _, e := range r.audit.Entries() {
		found = found || (e.Status == "observed" && e.Source.Name == "pipewire")
	}
	if !found {
		t.Fatal("outside change not in the audit trail")
	}
}

func TestWaitsForPipeWire(t *testing.T) {
	r := newRig(t, false)
	if _, _, err := r.set(t, "audio.volume.set", `{"percent":40}`, sdk.SourceManual); sdk.CodeOf(err) != sdk.CodeModuleUnavailable {
		t.Fatalf("before PipeWire: %v", err)
	}
	r.f.mu.Lock()
	r.f.up = true
	r.f.mu.Unlock()
	r.clock.BlockUntil(1)
	r.clock.Advance(2 * time.Second)
	waitFor(t, "audio", r.available)
	if _, _, err := r.set(t, "audio.volume.set", `{"percent":40}`, sdk.SourceManual); err != nil {
		t.Fatal(err)
	}

	// The monitor dies: the module starts over. (Wait for the watcher
	// itself: the module is available a moment before it watches, and a
	// settle timer may be pending besides the retry timer.)
	waitFor(t, "watching", func() bool { return r.f.watching() == 1 })
	r.f.killMonitor()
	waitFor(t, "watch ended", func() bool { return r.f.watching() == 0 })
	waitFor(t, "watching again", func() bool {
		r.clock.Advance(2 * time.Second)
		return r.f.watching() == 1
	})
	if _, _, err := r.set(t, "audio.volume.set", `{"percent":41}`, sdk.SourceManual); err != nil {
		t.Fatal(err)
	}
}

func TestManifestIsValid(t *testing.T) {
	m := New(Options{}).Manifest()
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
}

// --- the real WirePlumber backend, against stand-in commands ------------------

func TestWirePlumberCommands(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	_ = os.WriteFile(state, []byte("0.70\n"), 0o644)
	script := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Records every call, and keeps volume and mute in files, as PipeWire would.
	script("wpctl", `echo "$@" >> "$AUDIO_DIR/calls"
echo "W rtkit warning" >&2
case "$1" in
get-volume) v=$(cat "$AUDIO_DIR/state"); if [ -f "$AUDIO_DIR/muted" ]; then echo "Volume: $v [MUTED]"; else echo "Volume: $v"; fi ;;
set-volume) echo "$3" > "$AUDIO_DIR/state" ;;
set-mute) if [ "$3" = 1 ]; then touch "$AUDIO_DIR/muted"; else rm -f "$AUDIO_DIR/muted"; fi ;;
inspect) printf '  * node.name = "hdmi-out"\n  * node.description = "TV"\n' ;;
esac`)
	script("pw-dump", `printf '[\n  {}\n]\n[\n]\n'`)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	w := &WirePlumber{Env: []string{"AUDIO_DIR=" + dir}}
	ctx := context.Background()

	m, err := w.Master(ctx)
	if err != nil || m.Percent != 70 || m.Muted || m.Sink != "hdmi-out" || m.Description != "TV" {
		t.Fatalf("Master = %+v %v", m, err)
	}
	if err := w.SetVolume(ctx, 125); err != nil {
		t.Fatal(err)
	}
	if err := w.SetMute(ctx, true); err != nil {
		t.Fatal(err)
	}
	if m, _ := w.Master(ctx); m.Percent != 125 || !m.Muted {
		t.Fatalf("after setting: %+v", m)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	for _, want := range []string{"set-volume @DEFAULT_AUDIO_SINK@ 1.25", "set-mute @DEFAULT_AUDIO_SINK@ 1"} {
		if !strings.Contains(string(calls), want) {
			t.Errorf("no call %q in:\n%s", want, calls)
		}
	}

	cues := 0
	err = w.Watch(ctx, func() { cues++ })
	if cues != 2 || err == nil || !strings.Contains(err.Error(), "pw-dump exited") {
		t.Fatalf("Watch: %d cues, %v", cues, err)
	}

	t.Setenv("PATH", t.TempDir()) // no wpctl at all
	if _, err := w.Master(ctx); sdk.CodeOf(err) != sdk.CodeModuleUnavailable || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("without wpctl: %v", err)
	}
}
