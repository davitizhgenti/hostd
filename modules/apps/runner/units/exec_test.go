package units

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/internal/clock"
	"github.com/davitizhgenti/hostd/internal/systemd"
	"github.com/davitizhgenti/hostd/modules/apps/runner/units/unitstest"
	"github.com/davitizhgenti/hostd/modules/apps/spec"
	"github.com/davitizhgenti/hostd/sdk"
)

func TestRunnerCommands(t *testing.T) {
	have := map[string]bool{"firefox-esr": true, "gamescope": true}
	look := func(name string) (string, error) {
		if have[name] {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
	state := t.TempDir()
	r := &ExecRunner{StateDir: state, LookPath: look}
	for _, c := range []struct {
		app  App
		want []string
	}{
		{App{ID: "kodi", Runner: spec.Runner{Type: spec.RunnerFlatpak, AppID: "tv.kodi.Kodi"}},
			[]string{"flatpak", "run", "tv.kodi.Kodi"}},
		{App{ID: "jelly", Runner: spec.Runner{Type: spec.RunnerURL, URL: "http://tv.lan:8096"}},
			[]string{"firefox-esr", "--kiosk", "--new-instance", "--profile", filepath.Join(state, "browser", "jelly"), "http://tv.lan:8096"}},
		{App{ID: "game", Runner: spec.Runner{Type: spec.RunnerExec, Command: []string{"supertux2"}}, Window: spec.Window{Wrap: "gamescope"}},
			[]string{"gamescope", "-f", "--", "supertux2"}},
	} {
		got, err := r.command(&c.app)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %q %v, want %q", c.app.ID, got, err, c.want)
		}
	}
	if fi, err := os.Stat(filepath.Join(state, "browser", "jelly")); err != nil || !fi.IsDir() {
		t.Fatal("no browser profile directory")
	}

	// Configured browser and gamescope arguments win.
	r.Browser = []string{"chromium", "--app={url}", "--user-data-dir={profile}"}
	r.Gamescope = []string{"gamescope", "-W", "1280", "-H", "800", "-f"}
	got, _ := r.command(&App{ID: "jelly", Runner: spec.Runner{Type: spec.RunnerURL, URL: "https://x"}, Window: spec.Window{Wrap: "gamescope"}})
	want := []string{"gamescope", "-W", "1280", "-H", "800", "-f", "--", "chromium", "--app=https://x", "--user-data-dir=" + filepath.Join(state, "browser", "jelly")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("configured: %q", got)
	}

	// Missing programs are clear errors.
	r = &ExecRunner{StateDir: state, LookPath: func(string) (string, error) { return "", errors.New("no") }}
	if _, err := r.command(&App{ID: "w", Runner: spec.Runner{Type: spec.RunnerURL, URL: "http://x"}}); sdk.CodeOf(err) != sdk.CodeModuleUnavailable {
		t.Fatalf("no browser: %v", err)
	}
	if _, err := r.command(&App{ID: "g", Runner: spec.Runner{Type: spec.RunnerExec, Command: []string{"x"}}, Window: spec.Window{Wrap: "gamescope"}}); sdk.CodeOf(err) != sdk.CodeModuleUnavailable {
		t.Fatalf("no gamescope: %v", err)
	}
}

// --- handoff -------------------------------------------------------------------------

// fakeProcs is a /proc with processes that have an environment.
type fakeProcs struct {
	root   string
	mu     sync.Mutex
	killed []string
}

func newFakeProcs(t *testing.T) *fakeProcs { return &fakeProcs{root: t.TempDir()} }

func (p *fakeProcs) spawn(pid int, env ...string) {
	dir := filepath.Join(p.root, fmt.Sprint(pid))
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "environ"), []byte(strings.Join(env, "\x00")+"\x00"), 0o644)
}

func (p *fakeProcs) exit(pid int) { _ = os.RemoveAll(filepath.Join(p.root, fmt.Sprint(pid))) }

// kill records the signal; SIGTERM ends a process unless it is stubborn.
func (p *fakeProcs) kill(stubborn map[int]bool) func(int, syscall.Signal) error {
	return func(pid int, sig syscall.Signal) error {
		p.mu.Lock()
		p.killed = append(p.killed, fmt.Sprintf("%d %v", pid, sig))
		p.mu.Unlock()
		if sig == syscall.SIGKILL || !stubborn[pid] {
			p.exit(pid)
		}
		return nil
	}
}

func (p *fakeProcs) signals() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.killed...)
}

type handoffRig struct {
	r     *ExecRunner
	sd    *unitstest.FakeSystemd
	procs *fakeProcs
	clock *clock.Fake
	ended chan Ended
	ran   chan []string
}

func newHandoffRig(t *testing.T) *handoffRig {
	t.Helper()
	h := &handoffRig{sd: unitstest.NewFakeSystemd(), procs: newFakeProcs(t),
		clock: clock.NewFake(time.Date(2026, 10, 8, 20, 0, 0, 0, time.UTC)), ended: make(chan Ended, 4), ran: make(chan []string, 4)}
	h.r = &ExecRunner{Systemd: h.sd, RuntimeDir: unitstest.FakeSession(t), ProcRoot: h.procs.root, Clock: h.clock,
		Kill: h.procs.kill(map[int]bool{502: true}),
		RunCommand: func(_ context.Context, argv, _ []string, _ string) error {
			h.ran <- argv
			return nil
		}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = h.r.Watch(ctx, func(e Ended) { h.ended <- e }) }()
	t.Cleanup(func() { cancel(); <-done })
	waitUntil(t, func() bool { return h.sd.Watching() == 1 })
	return h
}

// poll lets the poller run once.
func (h *handoffRig) poll(t *testing.T) {
	t.Helper()
	h.clock.BlockUntil(1)
	h.clock.Advance(2 * time.Second)
}

// seen waits until the poller has seen an instance's processes.
func (h *handoffRig) seen(t *testing.T, id string) {
	t.Helper()
	waitUntil(t, func() bool {
		h.clock.Advance(2 * time.Second)
		h.r.mu.Lock()
		defer h.r.mu.Unlock()
		hh, ok := h.r.handoffs[id]
		return ok && hh.seen
	})
}

// exitedWhileAway marks a unit's command as exited without telling any
// watcher, as if hostd was not running.
func (h *handoffRig) exitedWhileAway(name string) {
	h.sd.Mu.Lock()
	defer h.sd.Mu.Unlock()
	u := h.sd.Units[name]
	u.Info.SubState, u.Info.ExitCode, u.Info.MainPID = "exited", 1, 0
}

func (h *handoffRig) noEnd(t *testing.T) {
	t.Helper()
	select {
	case e := <-h.ended:
		t.Fatalf("ended early: %+v", e)
	case <-time.After(30 * time.Millisecond):
	}
}

func (h *handoffRig) end(t *testing.T) Ended {
	t.Helper()
	for range 50 {
		select {
		case e := <-h.ended:
			return e
		case <-time.After(10 * time.Millisecond):
			h.clock.Advance(2 * time.Second)
		}
	}
	t.Fatal("instance did not end")
	return Ended{}
}

var portal = &App{ID: "portal2", Surface: spec.SurfaceWindow,
	Runner: spec.Runner{Type: spec.RunnerExec, Command: []string{"steam", "steam://rungameid/620"}, Handoff: "SteamAppId=620"}}

func TestHandoffLifecycle(t *testing.T) {
	h := newHandoffRig(t)
	ctx := context.Background()
	inst, err := h.r.Start(ctx, Instance{ID: "portal2", App: "portal2", Surface: spec.SurfaceWindow}, portal)
	if err != nil {
		t.Fatal(err)
	}
	unit := contract.UnitName("portal2")
	if d := h.sd.Units[unit].Info.Description; d != "hostd instance portal2 of app portal2 handoff SteamAppId=620" {
		t.Fatalf("description %q", d)
	}
	// The command hands off and exits: the instance goes on.
	h.sd.Exit(unit, 0, 1)
	h.poll(t)
	h.noEnd(t)
	// The game runs, then ends.
	h.procs.spawn(501, "SteamAppId=620", "HOME=/home/screen")
	h.procs.spawn(777, "HOME=/home/screen") // someone else
	h.seen(t, "portal2")
	h.noEnd(t)
	h.procs.exit(501)
	if e := h.end(t); e.Instance != "portal2" || e.ExitCode != 0 {
		t.Fatalf("ended %+v", e)
	}
	if names := h.sd.UnitNames(); len(names) != 0 {
		t.Fatalf("units left: %v (inst %+v)", names, inst)
	}
}

func TestHandoffNeverAppears(t *testing.T) {
	h := newHandoffRig(t)
	if _, err := h.r.Start(context.Background(), Instance{ID: "portal2", App: "portal2", Surface: spec.SurfaceWindow}, portal); err != nil {
		t.Fatal(err)
	}
	h.sd.Exit(contract.UnitName("portal2"), 0, 1)
	h.clock.BlockUntil(1)
	h.clock.Advance(3*time.Minute + 2*time.Second)
	if e := h.end(t); e.ExitCode != 1 || !strings.Contains(e.Reason, "SteamAppId=620") {
		t.Fatalf("ended %+v", e)
	}
}

func TestHandoffCommandFails(t *testing.T) {
	// The command fails instead of handing off: reported as usual.
	h := newHandoffRig(t)
	if _, err := h.r.Start(context.Background(), Instance{ID: "portal2", App: "portal2", Surface: spec.SurfaceWindow}, portal); err != nil {
		t.Fatal(err)
	}
	h.sd.Exit(contract.UnitName("portal2"), 2, 1)
	select {
	case e := <-h.ended:
		if e.ExitCode != 2 {
			t.Fatalf("ended %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("failure not reported")
	}
}

func TestHandoffHostStaysAndIsReused(t *testing.T) {
	// The first launch started Steam itself, which stays in the unit:
	// the game's end does not stop it, and the next launch hands off to
	// it without a new unit.
	h := newHandoffRig(t)
	ctx := context.Background()
	in := Instance{ID: "portal2", App: "portal2", Surface: spec.SurfaceWindow}
	if _, err := h.r.Start(ctx, in, portal); err != nil {
		t.Fatal(err)
	}
	h.procs.spawn(501, "SteamAppId=620")
	h.seen(t, "portal2")
	h.procs.exit(501)
	h.end(t)
	if names := h.sd.UnitNames(); len(names) != 1 {
		t.Fatalf("the host unit was stopped: %v", names)
	}
	if _, err := h.r.Start(ctx, in, portal); err != nil {
		t.Fatal(err)
	}
	select {
	case argv := <-h.ran:
		if !reflect.DeepEqual(argv, portal.Runner.Command) {
			t.Fatalf("ran %q", argv)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not hand off to the running host")
	}
	// Stopping the game ends its processes, politely, then firmly for
	// the stubborn one; the host stays.
	h.procs.spawn(501, "SteamAppId=620")
	h.procs.spawn(502, "SteamAppId=620")
	stopped := make(chan error, 1)
	go func() { stopped <- h.r.Stop(ctx, in) }()
	waitUntil(t, func() bool { return len(h.procs.signals()) == 2 })
	h.clock.BlockUntil(2)
	h.clock.Advance(10 * time.Second)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	want := []string{"501 terminated", "502 terminated", "502 killed"}
	if got := h.procs.signals(); !reflect.DeepEqual(got, want) {
		t.Fatalf("signals %q", got)
	}
	if names := h.sd.UnitNames(); len(names) != 1 {
		t.Fatalf("units %v", names)
	}
}

func TestHandoffAdopt(t *testing.T) {
	h := newHandoffRig(t)
	ctx := context.Background()
	desc := description(Instance{ID: "portal2", App: "portal2"}, "SteamAppId=620")
	// Before hostd restarted: the command exited, the game still runs.
	_ = h.sd.StartTransient(ctx, contract.UnitName("portal2"), systemd.UnitSpec{Description: desc, Argv: []string{"steam"}})
	h.exitedWhileAway(contract.UnitName("portal2"))
	h.procs.spawn(501, "SteamAppId=620")
	// Another one whose game ended while hostd was away.
	_ = h.sd.StartTransient(ctx, contract.UnitName("hl"), systemd.UnitSpec{Description: description(Instance{ID: "hl", App: "hl"}, "SteamAppId=70"), Argv: []string{"steam"}})
	h.exitedWhileAway(contract.UnitName("hl"))
	// And a unit that hosts Steam, with no game running.
	_ = h.sd.StartTransient(ctx, contract.UnitName("tf2"), systemd.UnitSpec{Description: description(Instance{ID: "tf2", App: "tf2"}, "SteamAppId=440"), Argv: []string{"steam"}})

	found, err := h.r.Adopt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]State{}
	for _, in := range found {
		got[in.ID] = in.State
	}
	if !reflect.DeepEqual(got, map[string]State{"portal2": StateRunning, "hl": StateExited}) {
		t.Fatalf("adopted %v", got)
	}
	h.procs.exit(501)
	if e := h.end(t); e.Instance != "portal2" {
		t.Fatalf("ended %+v", e)
	}
}

// --- app actions ---------------------------------------------------------------------

func TestUnitNameAndDescription(t *testing.T) {
	if got := contract.UnitName("firefox#2"); got != `hostd-firefox\x232.service` {
		t.Fatalf("unitName = %q", got)
	}
	m := reDescription.FindStringSubmatch(description(Instance{ID: "firefox#2", App: "firefox"}, ""))
	if m == nil || m[1] != "firefox#2" || m[2] != "firefox" || m[3] != "" {
		t.Fatalf("description does not parse back: %v", m)
	}
	m = reDescription.FindStringSubmatch(description(Instance{ID: "portal2", App: "portal2"}, "SteamAppId=620"))
	if m == nil || m[1] != "portal2" || m[3] != "SteamAppId=620" {
		t.Fatalf("handoff description does not parse back: %v", m)
	}
}

// waitUntil polls cond for up to 5 seconds.
func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	for range 500 {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}
