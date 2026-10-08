package display

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"go.uber.org/goleak"

	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/internal/clock"
	"github.com/davitizhgenti/hostd/internal/testutil"
	"github.com/davitizhgenti/hostd/sdk"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// --- IPC framing -----------------------------------------------------------

func TestIPCFraming(t *testing.T) {
	var buf bytes.Buffer
	_ = writeMessage(&buf, ipcGetTree, nil)
	_ = writeMessage(&buf, ipcRunCommand, []byte(`workspace "hostd:foot"`))
	_ = writeMessage(&buf, eventWindow, []byte(`{"change":"new"}`))

	// Byte by byte, as a slow socket might deliver it.
	r := iotest.OneByteReader(&buf)
	for _, want := range []struct {
		typ     uint32
		payload string
	}{{ipcGetTree, ""}, {ipcRunCommand, `workspace "hostd:foot"`}, {eventWindow, `{"change":"new"}`}} {
		typ, payload, err := readMessage(r)
		if err != nil || typ != want.typ || string(payload) != want.payload {
			t.Fatalf("got %d %q %v; want %d %q", typ, payload, err, want.typ, want.payload)
		}
	}
	if _, _, err := readMessage(r); err == nil {
		t.Fatal("read past the end")
	}
	if _, _, err := readMessage(strings.NewReader("i4-ipc\x00\x00\x00\x00\x00\x00\x00\x00")); err == nil {
		t.Fatal("bad magic accepted")
	}
	huge := []byte("i3-ipc\xff\xff\xff\x7f\x00\x00\x00\x00")
	if _, _, err := readMessage(bytes.NewReader(huge)); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("huge payload: %v", err)
	}
}

// --- a fake Sway serving the fixtures captured from a real one ------------

type fakeSway struct {
	path     string
	ln       net.Listener
	mu       sync.Mutex
	commands []string
	events   [][]byte // window events sent after a subscribe
	wg       sync.WaitGroup
}

func newFakeSway(t *testing.T) *fakeSway {
	t.Helper()
	dir, _ := os.MkdirTemp("", "sway-")
	t.Cleanup(func() { os.RemoveAll(dir) })
	f := &fakeSway{path: filepath.Join(dir, "sway-ipc.1000.1.sock")}
	ln, err := net.Listen("unix", f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.ln = ln
	data, _ := os.ReadFile("testdata/sway-events.jsonl")
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if bytes.Contains(sc.Bytes(), []byte(`"container"`)) {
			f.events = append(f.events, append([]byte(nil), sc.Bytes()...))
		}
	}
	f.wg.Add(1)
	go f.serve()
	t.Cleanup(func() { ln.Close(); f.wg.Wait() })
	return f
}

func (f *fakeSway) serve() {
	defer f.wg.Done()
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.wg.Add(1)
		go func() { defer f.wg.Done(); defer c.Close(); f.handle(c) }()
	}
}

func (f *fakeSway) handle(c net.Conn) {
	tree, _ := os.ReadFile("testdata/sway-tree.json")
	outputs, _ := os.ReadFile("testdata/sway-outputs.json")
	for {
		typ, payload, err := readMessage(c)
		if err != nil {
			return
		}
		switch typ {
		case ipcGetTree:
			_ = writeMessage(c, typ, tree)
		case ipcGetOutputs:
			_ = writeMessage(c, typ, outputs)
		case ipcRunCommand:
			f.mu.Lock()
			f.commands = append(f.commands, string(payload))
			f.mu.Unlock()
			if strings.Contains(string(payload), "con_id=999") {
				_ = writeMessage(c, typ, []byte(`[{"success":false,"error":"No matching node."}]`))
			} else {
				_ = writeMessage(c, typ, []byte(`[{"success":true}]`))
			}
		case ipcSubscribe:
			_ = writeMessage(c, typ, []byte(`{"success":true}`))
			for _, ev := range f.events {
				_ = writeMessage(c, eventWindow, ev)
			}
			_ = writeMessage(c, eventShutdown, []byte(`{"change":"exit"}`))
			return
		}
	}
}

func (f *fakeSway) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.commands...)
}

func TestSwayBackend(t *testing.T) {
	f := newFakeSway(t)
	ctx := context.Background()
	s, err := DialSway(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Disconnect()

	wins, err := s.Windows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(wins) != 2 || wins[0].ID != 6 || wins[0].PID != 662 || wins[1].PID != 683 ||
		wins[0].AppID != "foot" || wins[0].Workspace != "1" || wins[0].Output != "HEADLESS-1" {
		t.Fatalf("windows = %+v", wins)
	}
	outs, err := s.Outputs(ctx)
	if err != nil || len(outs) != 1 || outs[0].Name != "HEADLESS-1" || outs[0].Width != 1920 || !outs[0].Active {
		t.Fatalf("outputs = %+v %v", outs, err)
	}

	_ = s.Move(ctx, 7, "hostd:foot")
	_ = s.Show(ctx, "hostd:foot")
	_ = s.Focus(ctx, 7)
	_ = s.Fullscreen(ctx, 7, true)
	_ = s.CloseWindow(ctx, 7)
	want := []string{`[con_id=7] move container to workspace "hostd:foot"`, `workspace "hostd:foot"`,
		`[con_id=7] focus`, `[con_id=7] fullscreen enable`, `[con_id=7] kill`}
	if got := f.sent(); !reflect.DeepEqual(got, want) {
		t.Fatalf("commands:\n%q\nwant\n%q", got, want)
	}
	if err := s.Focus(ctx, 999); err == nil || !strings.Contains(err.Error(), "No matching node") {
		t.Fatalf("sway error not reported: %v", err)
	}

	var got []string
	err = s.Watch(ctx, func(ev WindowEvent) { got = append(got, fmt.Sprintf("%s %d", ev.Change, ev.Window.ID)) })
	if err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("Watch ended with %v", err)
	}
	if len(got) < 5 || got[0] != "new 6" || got[3] != "new 7" || got[len(got)-1] != "close 7" {
		t.Fatalf("events = %v", got)
	}
}

func TestFindSway(t *testing.T) {
	f := newFakeSway(t)
	dir := filepath.Dir(f.path)
	stale := filepath.Join(dir, "sway-ipc.1000.2.sock") // left by a crashed Sway
	ln, _ := net.Listen("unix", stale)
	ln.Close()
	_ = os.WriteFile(stale, nil, 0o600)
	t.Setenv("SWAYSOCK", "")
	got, err := FindSway(context.Background(), dir)
	if err != nil || got != f.path {
		t.Fatalf("FindSway = %q %v, want %q", got, err, f.path)
	}
	if _, err := FindSway(context.Background(), t.TempDir()); err == nil {
		t.Fatal("found Sway in an empty directory")
	}
}

// --- matching windows to instances ------------------------------------------

func fakeProc(t *testing.T, cgroups map[int]string) string {
	t.Helper()
	root := t.TempDir()
	for pid, cg := range cgroups {
		dir := filepath.Join(root, fmt.Sprint(pid))
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "cgroup"), []byte(cg+"\n"), 0o644)
	}
	return root
}

func TestInstanceOf(t *testing.T) {
	// The real cgroups captured from the devbox, and a few more shapes.
	data, _ := os.ReadFile("testdata/sway-cgroups.txt")
	cgs := map[int]string{}
	var pid int
	for _, line := range strings.Split(string(data), "\n") {
		if n, ok := strings.CutPrefix(line, "== "); ok {
			fmt.Sscan(n, &pid)
		} else if line != "" {
			cgs[pid] = line
		}
	}
	cgs[10] = `0::/user.slice/user-1000.slice/user@1000.service/app.slice/hostd-firefox\x232.service`
	cgs[11] = `0::/user.slice/user-1000.slice/user@1000.service/app.slice/hostd-steam.service/game`
	cgs[12] = `0::/user.slice/user-1000.slice/user@1000.service/app.slice/hostd.service`
	root := fakeProc(t, cgs)
	for pid, want := range map[int]string{662: "", 683: "foot", 10: "firefox#2", 11: "steam", 12: "", 99: "", 0: ""} {
		if got := instanceOf(root, pid); got != want {
			t.Errorf("instanceOf(%d) = %q, want %q", pid, got, want)
		}
	}
}

// --- the module ---------------------------------------------------------------

type fakeBackend struct {
	mu     sync.Mutex
	wins   map[int64]*Window
	cmds   []string
	events chan WindowEvent
	ignore map[int64]bool // windows that do not close when asked
	gone   chan struct{}  // closed to end Watch (compositor quit)
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{wins: map[int64]*Window{}, events: make(chan WindowEvent, 64), ignore: map[int64]bool{},
		gone: make(chan struct{})}
}

func (f *fakeBackend) log(format string, a ...any) {
	f.mu.Lock()
	f.cmds = append(f.cmds, fmt.Sprintf(format, a...))
	f.mu.Unlock()
}

func (f *fakeBackend) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.cmds
	f.cmds = nil
	return out
}

func (f *fakeBackend) Windows(context.Context) ([]Window, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Window
	for _, w := range f.wins {
		out = append(out, *w)
	}
	return out, nil
}
func (f *fakeBackend) Outputs(context.Context) ([]Output, error) {
	return []Output{{Name: "HDMI-A-1", Width: 1280, Height: 800, Active: true}}, nil
}
func (f *fakeBackend) Show(_ context.Context, ws string) error { f.log("show %s", ws); return nil }
func (f *fakeBackend) Move(_ context.Context, id int64, ws string) error {
	f.log("move %d %s", id, ws)
	f.mu.Lock()
	if w := f.wins[id]; w != nil {
		w.Workspace = ws
	}
	f.mu.Unlock()
	return nil
}
func (f *fakeBackend) Focus(_ context.Context, id int64) error { f.log("focus %d", id); return nil }
func (f *fakeBackend) Fullscreen(_ context.Context, id int64, on bool) error {
	f.log("fullscreen %d %v", id, on)
	return nil
}
func (f *fakeBackend) CloseWindow(_ context.Context, id int64) error {
	f.log("close %d", id)
	f.mu.Lock()
	ignore := f.ignore[id]
	w := f.wins[id]
	f.mu.Unlock()
	if !ignore && w != nil {
		f.closeWin(id)
	}
	return nil
}
func (f *fakeBackend) Disconnect() error { return nil }
func (f *fakeBackend) Watch(ctx context.Context, fn func(WindowEvent)) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-f.gone:
			return errors.New("sway went away")
		case ev := <-f.events:
			fn(ev)
		}
	}
}

// open adds a window and announces it, as Sway would.
func (f *fakeBackend) open(id int64, pid int) {
	f.mu.Lock()
	w := &Window{ID: id, PID: pid, AppID: "app", Workspace: "1"}
	f.wins[id] = w
	ev := WindowEvent{Change: "new", Window: *w}
	f.mu.Unlock()
	f.events <- ev
}

func (f *fakeBackend) focus(id int64) {
	f.mu.Lock()
	for _, w := range f.wins {
		w.Focused = w.ID == id
	}
	ev := WindowEvent{Change: "focus", Window: *f.wins[id]}
	f.mu.Unlock()
	f.events <- ev
}

func (f *fakeBackend) closeWin(id int64) {
	f.mu.Lock()
	w := *f.wins[id]
	delete(f.wins, id)
	f.mu.Unlock()
	f.events <- WindowEvent{Change: "close", Window: w}
}

type displayRig struct {
	allowConnect func()
	e            *core.Engine
	m            *Module
	b            *fakeBackend
	apps         *testutil.Module
	clock        *clock.Fake
	input        *fakeInput
	notes        *fakeNotifier
	events       <-chan sdk.Event
	stopped      chan string
}

// newDisplayRig runs the display module with a fake compositor, and a
// fake apps module that provides instance.stop and the apps scope.
func newDisplayRig(t *testing.T, connectable bool) *displayRig {
	t.Helper()
	proc := fakeProc(t, map[int]string{
		100: `0::/user.slice/user-1000.slice/user@1000.service/app.slice/hostd-tv.service`,
		200: `0::/user.slice/user-1000.slice/user@1000.service/app.slice/hostd-browser.service`,
		201: `0::/user.slice/user-1000.slice/user@1000.service/app.slice/hostd-browser.service`,
		300: `0::/user.slice/user-1000.slice/user@1000.service/app.slice/sway.service`,
		400: `0::/user.slice/user-1000.slice/user@1000.service/app.slice/hostd-notes.service`,
	})
	r := &displayRig{b: newFakeBackend(), clock: clock.NewFake(time.Date(2026, 10, 8, 20, 0, 0, 0, time.UTC)),
		stopped: make(chan string, 4), input: newFakeInput(), notes: &fakeNotifier{}}
	var mu sync.Mutex
	canConnect := connectable
	r.m = New(Options{ProcRoot: proc, Clock: r.clock, Input: r.input, Notifier: r.notes, Connect: func(context.Context) (Backend, error) {
		mu.Lock()
		defer mu.Unlock()
		if !canConnect {
			return nil, errors.New("Sway is not running")
		}
		return r.b, nil
	}})
	r.apps = testutil.NewModule("apps", nil, nil, []string{"instance.starting", "instance.started", "instance.exited"})
	r.apps.M.Owns = []string{"app.*", "instance.*"}
	r.apps.M.Scopes = []sdk.ScopeSpec{{Name: "apps"}, {Name: "display.front"}}
	r.apps.M.Actions = []sdk.ActionSpec{{Type: "instance.stop", Scope: "apps",
		Schema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}}}`),
		Keys:   []sdk.KeyTemplate{"instance:{id}"}}}
	r.apps.HandleFunc = func(_ context.Context, a sdk.Action) (sdk.Result, error) {
		var args struct{ ID string }
		_ = a.DecodeArgs(&args)
		r.stopped <- args.ID
		return sdk.Result{}, nil
	}
	reg := core.NewRegistry()
	for _, m := range []sdk.Module{r.apps, r.m} {
		if err := reg.Add(m); err != nil {
			t.Fatal(err)
		}
	}
	r.e = core.New(reg, core.Options{})
	if err := r.e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.events = r.e.Subscribe(ctx, "window.*")
	t.Cleanup(func() { cancel(); _ = r.e.Stop(context.Background()) })
	if connectable {
		waitFor(t, "attach", func() bool { return r.attached() })
	}
	r.allowConnect = func() { mu.Lock(); canConnect = true; mu.Unlock() }
	return r
}

func (r *displayRig) attached() bool {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	return r.m.backend != nil
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

func (r *displayRig) event(t *testing.T) (string, trackedWindow) {
	t.Helper()
	select {
	case ev := <-r.events:
		var tw trackedWindow
		_ = json.Unmarshal(ev.Data, &tw)
		return ev.Type, tw
	case <-time.After(5 * time.Second):
		t.Fatal("no window event")
		return "", trackedWindow{}
	}
}

func (r *displayRig) act(t *testing.T, typ, instance string) (sdk.Result, error) {
	t.Helper()
	return r.e.Submit(context.Background(), sdk.Action{Type: typ, Args: json.RawMessage(fmt.Sprintf(`{"instance":%q}`, instance)),
		Source: sdk.Source{Kind: sdk.SourceManual}}, core.Auth{Scopes: []string{sdk.ScopeAdmin}})
}

func TestNewWindowGetsItsOwnFullscreenWorkspace(t *testing.T) {
	r := newDisplayRig(t, true)
	r.b.open(1, 100)
	typ, tw := r.event(t)
	if typ != EventOpened || tw.Instance != "tv" || tw.Workspace != "hostd:tv" {
		t.Fatalf("%s %+v", typ, tw)
	}
	want := []string{"move 1 hostd:tv", "show hostd:tv", "focus 1", "fullscreen 1 true"}
	if got := r.b.commands(); !reflect.DeepEqual(got, want) {
		t.Fatalf("commands %q, want %q", got, want)
	}

	// A window hostd did not start is left where it is.
	r.b.open(2, 300)
	typ, tw = r.event(t)
	if typ != EventOpened || tw.Instance != "" {
		t.Fatalf("%s %+v", typ, tw)
	}
	if got := r.b.commands(); len(got) != 0 {
		t.Fatalf("an unowned window was moved: %q", got)
	}
}

func TestWindowedPreference(t *testing.T) {
	r := newDisplayRig(t, true)
	// The apps module says notes is windowed, before its window appears.
	r.apps.Core().Emit(sdk.Event{Type: "instance.starting", Data: json.RawMessage(`{"id":"notes","fullscreen":false}`)})
	waitFor(t, "preference", func() bool { r.m.mu.Lock(); defer r.m.mu.Unlock(); _, ok := r.m.prefs["notes"]; return ok })
	r.b.open(4, 400)
	r.event(t)
	if got := r.b.commands(); !reflect.DeepEqual(got, []string{"move 4 hostd:notes", "show hostd:notes", "focus 4"}) {
		t.Fatalf("commands %q", got)
	}

	// The preference arrives after the window was placed fullscreen.
	r.b.open(1, 100)
	r.event(t)
	r.m.mu.Lock()
	r.m.windows[1].Fullscreen = true
	r.m.mu.Unlock()
	r.b.commands()
	r.apps.Core().Emit(sdk.Event{Type: "instance.started", Data: json.RawMessage(`{"id":"tv","fullscreen":false}`)})
	waitFor(t, "fullscreen off", func() bool {
		r.b.mu.Lock()
		defer r.b.mu.Unlock()
		return len(r.b.cmds) == 1 && r.b.cmds[0] == "fullscreen 1 false"
	})
}

func TestFocusAction(t *testing.T) {
	r := newDisplayRig(t, true)
	r.b.open(1, 100)
	r.event(t)
	r.b.open(2, 200)
	r.event(t)
	r.b.commands()
	res, err := r.act(t, "window.focus", "tv")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.b.commands(); !reflect.DeepEqual(got, []string{"show hostd:tv", "focus 1"}) {
		t.Fatalf("commands %q (result %s)", got, res.Data)
	}
	if _, err := r.act(t, "window.focus", "ghost"); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("instance without window: %v", err)
	}
	if _, err := r.act(t, "window.fullscreen", "browser"); err != nil {
		t.Fatal(err)
	}
	if got := r.b.commands(); !reflect.DeepEqual(got, []string{"fullscreen 2 true"}) {
		t.Fatalf("fullscreen commands %q", got)
	}
}

func TestCloseAction(t *testing.T) {
	r := newDisplayRig(t, true)
	r.b.open(1, 200)
	r.b.open(2, 201) // a second browser window
	r.event(t)
	r.event(t)
	res, err := r.act(t, "window.close", "browser")
	if err != nil || !strings.Contains(string(res.Data), `"closed":2`) {
		t.Fatalf("%s %v", res.Data, err)
	}
	select {
	case id := <-r.stopped:
		t.Fatalf("stopped %s although its windows closed", id)
	default:
	}

	// A window that ignores the close request: after the timeout, the app
	// is stopped (a child action that reuses the instance key).
	r.b.open(3, 100)
	r.event(t)
	r.b.mu.Lock()
	r.b.ignore[3] = true
	r.b.mu.Unlock()
	done := make(chan error, 1)
	go func() { _, err := r.act(t, "window.close", "tv"); done <- err }()
	r.clock.BlockUntil(1)
	r.clock.Advance(5 * time.Second)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if id := <-r.stopped; id != "tv" {
		t.Fatalf("stopped %s", id)
	}
}

func TestBackToPreviousAppOnClose(t *testing.T) {
	r := newDisplayRig(t, true)
	r.b.open(1, 100) // tv
	r.event(t)
	r.b.open(2, 400) // notes
	r.event(t)
	r.b.focus(1)
	r.event(t)
	r.b.focus(2)
	r.event(t)
	r.b.commands()
	r.b.closeWin(2) // notes closes: back to tv
	if typ, _ := r.event(t); typ != EventClosed {
		t.Fatalf("got %s", typ)
	}
	waitFor(t, "focus back", func() bool {
		r.b.mu.Lock()
		defer r.b.mu.Unlock()
		return reflect.DeepEqual(r.b.cmds, []string{"show hostd:tv", "focus 1"})
	})
}

func TestWaitsForSwayAndReattaches(t *testing.T) {
	r := newDisplayRig(t, false)
	if _, err := r.act(t, "window.focus", "tv"); sdk.CodeOf(err) != sdk.CodeModuleUnavailable {
		t.Fatalf("before Sway: %v", err)
	}
	st, _ := r.m.Read(context.Background(), "display", nil)
	if st.(map[string]any)["attached"] != false {
		t.Fatalf("display = %v", st)
	}
	// Wait until the module has failed once and waits to retry; only then
	// does Sway "appear" (otherwise its first try may simply succeed).
	r.clock.BlockUntil(1)
	r.allowConnect()
	r.clock.Advance(2 * time.Second)
	waitFor(t, "attach", r.attached)

	close(r.b.gone) // Sway quits
	waitFor(t, "detach", func() bool { return !r.attached() })
	r.b.gone = make(chan struct{})
	r.clock.BlockUntil(1)
	r.clock.Advance(2 * time.Second)
	waitFor(t, "reattach", r.attached)
	st, _ = r.m.Read(context.Background(), "display", nil)
	if st.(map[string]any)["attached"] != true {
		t.Fatalf("display = %v", st)
	}
}

func TestManifestIsValid(t *testing.T) {
	m := New(Options{}).Manifest()
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
}
