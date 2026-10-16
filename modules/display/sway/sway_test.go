package sway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/iotest"

	"github.com/davitizhgenti/hostd/modules/display/wm"
)

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
	extra    []rawEvent
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

type rawEvent struct {
	typ     uint32
	payload string
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
			for _, ev := range f.extra {
				_ = writeMessage(c, ev.typ, []byte(ev.payload))
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
	s, err := Dial(ctx, f.path)
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
	err = s.Watch(ctx, func(ev wm.Event) { got = append(got, fmt.Sprintf("%s %d", ev.Change, ev.Window.ID)) })
	if err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("Watch ended with %v", err)
	}
	if len(got) < 5 || got[0] != "new 6" || got[3] != "new 7" || got[len(got)-1] != "close 7" {
		t.Fatalf("events = %v", got)
	}
}

func TestFind(t *testing.T) {
	f := newFakeSway(t)
	dir := filepath.Dir(f.path)
	stale := filepath.Join(dir, "sway-ipc.1000.2.sock") // left by a crashed Sway
	ln, _ := net.Listen("unix", stale)
	ln.Close()
	_ = os.WriteFile(stale, nil, 0o600)
	t.Setenv("SWAYSOCK", "")
	got, err := Find(context.Background(), dir)
	if err != nil || got != f.path {
		t.Fatalf("Find = %q %v, want %q", got, err, f.path)
	}
	if _, err := Find(context.Background(), t.TempDir()); err == nil {
		t.Fatal("found Sway in an empty directory")
	}
}

// --- matching windows to instances ------------------------------------------

func TestSwayKeys(t *testing.T) {
	for name, want := range map[string]string{
		"Super":           "--release Super_L,Mod4+Super_L,Super_R,Mod4+Super_R",
		"Alt":             "--release Alt_L,Mod1+Alt_L",
		"Super+Tab":       "--no-repeat Mod4+Tab",
		"Super+Shift+Tab": "--no-repeat Mod4+Shift+Tab",
		"Super+Q":         "--no-repeat Mod4+q",
		"Ctrl+Alt+Delete": "--no-repeat Control+Mod1+Delete",
		"F1":              "--no-repeat F1",
		"Q":               "--no-repeat q",
	} {
		flags, combos := swayKeys(name)
		if got := flags + " " + strings.Join(combos, ","); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}

func TestSwayBindAndEvents(t *testing.T) {
	f := newFakeSway(t)
	f.extra = []rawEvent{
		{eventBinding, `{"change":"run","binding":{"command":"nop hostd key Super+Tab","symbol":"Tab"}}`},
		{eventBinding, `{"change":"run","binding":{"command":"exec foot"}}`}, // not hostd's
		{eventWorkspace, `{"change":"reload"}`},
		{eventWorkspace, `{"change":"focus"}`},
	}
	ctx := context.Background()
	s, err := Dial(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Disconnect()
	_ = s.Bind(ctx, []string{"Super", "Super+Tab"})
	_ = s.Bind(ctx, []string{"F1"})
	want := []string{
		`bindsym --release Super_L nop hostd key Super`,
		`bindsym --release Mod4+Super_L nop hostd key Super`,
		`bindsym --release Super_R nop hostd key Super`,
		`bindsym --release Mod4+Super_R nop hostd key Super`,
		`bindsym --no-repeat Mod4+Tab nop hostd key Super+Tab`,
		`unbindsym --release Super_L`,
		`unbindsym --release Mod4+Super_L`,
		`unbindsym --release Super_R`,
		`unbindsym --release Mod4+Super_R`,
		`unbindsym --no-repeat Mod4+Tab`,
		`bindsym --no-repeat F1 nop hostd key F1`,
	}
	if got := f.sent(); !reflect.DeepEqual(got, want) {
		t.Fatalf("commands\n%q\nwant\n%q", got, want)
	}
	var got []string
	_ = s.Watch(ctx, func(ev wm.Event) {
		if ev.Change == "binding" || ev.Change == "reload" {
			got = append(got, ev.Change+" "+ev.Binding)
		}
	})
	if !reflect.DeepEqual(got, []string{"binding Super+Tab", "reload "}) {
		t.Fatalf("events %q", got)
	}
}

// --- the module: keys and buttons run actions as the person at the screen ---

func TestSwayPlaceAndOutputCommands(t *testing.T) {
	f := newFakeSway(t)
	ctx := context.Background()
	s, err := Dial(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Disconnect()
	_ = s.Place(ctx, 7, 6)
	_ = s.SetOutput(ctx, "*", "power off")
	_ = s.SetOutput(ctx, "HDMI-A-1", "mode 1920x1080@60.000Hz")
	want := []string{
		`[con_id=6] fullscreen disable; [con_id=7] fullscreen disable; [con_id=6] mark --add hostd_place; [con_id=6] splith; ` +
			`[con_id=7] move container to mark hostd_place; [con_id=6] unmark hostd_place; [con_id=7] focus`,
		`output * power off`,
		`output "HDMI-A-1" mode 1920x1080@60.000Hz`,
	}
	if got := f.sent(); !reflect.DeepEqual(got, want) {
		t.Fatalf("commands\n%q\nwant\n%q", got, want)
	}
	outs, err := s.Outputs(ctx)
	if err != nil || len(outs) != 1 || !outs[0].Power {
		t.Fatalf("outputs %+v %v", outs, err)
	}
}

func TestSwayDialogs(t *testing.T) {
	for raw, want := range map[string]bool{
		`{"type":"con","pid":1,"window_properties":{"class":"steam_app_1","window_type":"normal","transient_for":null}}`: false,
		`{"type":"con","pid":1,"window_properties":{"class":"game","transient_for":4194305}}`:                            true,
		`{"type":"con","pid":1,"window_properties":{"class":"game","window_type":"dialog"}}`:                             true,
		`{"type":"floating_con","pid":1,"app_id":"foot"}`:                                                                true,
		`{"type":"con","pid":1,"app_id":"foot"}`:                                                                         false,
	} {
		var n node
		if err := json.Unmarshal([]byte(raw), &n); err != nil {
			t.Fatal(err)
		}
		if got := n.window().Dialog; got != want {
			t.Errorf("%s: dialog %v, want %v", raw, got, want)
		}
	}
}
