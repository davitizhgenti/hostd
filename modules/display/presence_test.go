package display

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/internal/evdev"
	"github.com/davitizhgenti/hostd/sdk"
)

// --- fakes -------------------------------------------------------------------

// fakeInput lets a test press keys: press() calls the module's callback.
type fakeInput struct {
	mu    sync.Mutex
	fn    func(evdev.InputEvent)
	grabs []bool // Grab calls
}

func (f *fakeInput) Grab(on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.grabs = append(f.grabs, on)
	return nil
}

func (f *fakeInput) grabbed() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.grabs...)
}

func newFakeInput() *fakeInput { return &fakeInput{} }

func (f *fakeInput) Watch(ctx context.Context, fn func(evdev.InputEvent)) error {
	f.mu.Lock()
	f.fn = fn
	f.mu.Unlock()
	<-ctx.Done()
	return nil
}

func (f *fakeInput) press(t *testing.T) { f.send(t, evdev.InputEvent{}) }

func (f *fakeInput) send(t *testing.T, ev evdev.InputEvent) {
	t.Helper()
	waitFor(t, "input watcher", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.fn != nil })
	f.mu.Lock()
	fn := f.fn
	f.mu.Unlock()
	fn(ev)
}

type fakeNotifier struct {
	mu    sync.Mutex
	shown []string
}

func (f *fakeNotifier) Notify(_ context.Context, summary, _ string) error {
	f.mu.Lock()
	f.shown = append(f.shown, summary)
	f.mu.Unlock()
	return nil
}

func (f *fakeNotifier) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.shown...)
}

// --- evdev parsing -------------------------------------------------------------

func TestPresenceGoesIdleAndBack(t *testing.T) {
	r := newDisplayRig(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := r.e.Subscribe(ctx, "display.*")
	next := func() string {
		t.Helper()
		select {
		case ev := <-events:
			return ev.Type
		case <-time.After(5 * time.Second):
			t.Fatal("no display event")
			return ""
		}
	}
	if r.m.presence.Present() {
		t.Fatal("present before any input")
	}
	r.input.press(t)
	if typ := next(); typ != EventActive {
		t.Fatalf("got %s", typ)
	}
	// Input keeps coming for 4 minutes: still present at 5 minutes after
	// the first press.
	r.clock.BlockUntil(1)
	for range 4 {
		r.clock.Advance(time.Minute)
		r.input.press(t)
	}
	r.clock.Advance(time.Minute)
	r.clock.BlockUntil(1) // the timer re-armed for the remaining time
	if !r.m.presence.Present() {
		t.Fatal("idle although input came 1 minute ago")
	}
	r.clock.Advance(4 * time.Minute)
	if typ := next(); typ != EventIdle {
		t.Fatalf("got %s", typ)
	}
	r.input.press(t)
	if typ := next(); typ != EventActive {
		t.Fatalf("got %s", typ)
	}
}

// --- focus protection ------------------------------------------------------------

// start pretends the apps module started an instance on behalf of src.
func (r *displayRig) start(t *testing.T, instance, name string, src sdk.SourceKind, front bool) {
	t.Helper()
	r.apps.Core().Emit(sdk.Event{Type: "instance.starting", Source: &sdk.Source{Kind: src},
		Data: json.RawMessage(fmt.Sprintf(`{"id":%q,"name":%q,"fullscreen":true,"front":%v}`, instance, name, front))})
	waitFor(t, "launch known", func() bool { return r.m.launchKnown(instance) })
}

func TestFocusProtection(t *testing.T) {
	foreground := []string{"move 1 hostd:tv", "show hostd:tv", "focus 1", "fullscreen 1 true"}
	background := []string{"move 1 hostd:tv", "fullscreen 1 true"}
	for _, c := range []struct {
		name    string
		present bool
		src     sdk.SourceKind
		front   bool
		want    []string
		notice  bool
	}{
		{"nobody there, phone", false, sdk.SourceManual, false, foreground, false},
		{"nobody there, script", false, sdk.SourceAutomation, false, foreground, false},
		{"someone there, phone", true, sdk.SourceManual, false, background, true},
		{"someone there, script", true, sdk.SourceAutomation, false, background, true},
		{"someone there, phone with front", true, sdk.SourceManual, true, foreground, false},
		{"someone there, started at the screen", true, sdk.SourceLocal, false, foreground, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newDisplayRig(t, true)
			if c.present {
				r.input.press(t)
				waitFor(t, "present", r.m.presence.Present)
			}
			r.start(t, "tv", "TV", c.src, c.front)
			r.b.open(1, 100)
			r.event(t)
			if got := r.b.commands(); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("commands %q, want %q", got, c.want)
			}
			if c.notice {
				waitFor(t, "notice", func() bool { return len(r.notes.all()) == 1 })
				if got := r.notes.all()[0]; got != "TV is ready" {
					t.Fatalf("notice %q", got)
				}
			} else if n := r.notes.all(); len(n) != 0 {
				t.Fatalf("unexpected notices %q", n)
			}
		})
	}
}

func TestWindowBeforeLaunchIsKnown(t *testing.T) {
	// The window can appear before instance.starting reaches the display
	// module. While someone is there it waits in the background, and
	// comes forward once the start turns out to be theirs.
	r := newDisplayRig(t, true)
	r.input.press(t)
	waitFor(t, "present", r.m.presence.Present)
	r.b.open(1, 100)
	r.event(t)
	if got := r.b.commands(); !reflect.DeepEqual(got, []string{"move 1 hostd:tv", "fullscreen 1 true"}) {
		t.Fatalf("commands %q", got)
	}
	r.start(t, "tv", "TV", sdk.SourceLocal, false)
	waitFor(t, "brought forward", func() bool {
		r.b.mu.Lock()
		defer r.b.mu.Unlock()
		return reflect.DeepEqual(r.b.cmds, []string{"show hostd:tv", "focus 1"})
	})
}

func TestBackgroundAppDoesNotStealFocusLater(t *testing.T) {
	// Started in front, then the person moved on to another app: the
	// first app's next window (a dialog) stays on its workspace.
	r := newDisplayRig(t, true)
	r.input.press(t)
	waitFor(t, "present", r.m.presence.Present)
	r.start(t, "browser", "Browser", sdk.SourceLocal, false)
	r.b.open(2, 200)
	r.event(t)
	r.start(t, "tv", "TV", sdk.SourceLocal, false)
	r.b.open(1, 100)
	r.event(t)
	r.b.focus(1)
	r.event(t)
	r.b.commands()
	r.b.open(3, 201) // the browser's second window
	r.event(t)
	if got := r.b.commands(); !reflect.DeepEqual(got, []string{"move 3 hostd:browser:3", "fullscreen 3 true"}) {
		t.Fatalf("commands %q", got)
	}
}

func TestRemoteFocusWhileSomeoneIsThere(t *testing.T) {
	r := newDisplayRig(t, true)
	r.b.open(1, 100)
	r.event(t)
	r.b.open(2, 200)
	r.event(t)
	r.b.focus(2)
	r.event(t)
	r.input.press(t)
	waitFor(t, "present", r.m.presence.Present)
	r.b.commands()

	focus := func(src sdk.SourceKind, args string, scopes ...string) (sdk.Result, error) {
		return r.e.Submit(context.Background(), sdk.Action{Type: "window.focus", Args: json.RawMessage(args),
			Source: sdk.Source{Kind: src}}, core.Auth{Scopes: scopes})
	}
	res, err := focus(sdk.SourceManual, `{"instance":"tv"}`, "apps")
	if err != nil || res.Status != sdk.StatusSkipped || res.Reason != "in_use" {
		t.Fatalf("remote focus: %+v %v", res, err)
	}
	if got := r.b.commands(); len(got) != 0 {
		t.Fatalf("remote focus moved the screen: %q", got)
	}
	waitFor(t, "notice", func() bool { return len(r.notes.all()) == 1 })

	// front=true needs the display.front scope, then works.
	if _, err := focus(sdk.SourceManual, `{"instance":"tv","front":true}`, "apps"); sdk.CodeOf(err) != sdk.CodeForbidden {
		t.Fatalf("front without scope: %v", err)
	}
	if res, err := focus(sdk.SourceManual, `{"instance":"tv","front":true}`, "apps", "display.front"); err != nil || res.Status != sdk.StatusApplied {
		t.Fatalf("front with scope: %+v %v", res, err)
	}
	// Someone at the screen always can.
	r.b.commands()
	if res, err := focus(sdk.SourceLocal, `{"instance":"browser"}`, "apps"); err != nil || res.Status != sdk.StatusApplied {
		t.Fatalf("local focus: %+v %v", res, err)
	}
	if got := r.b.commands(); !reflect.DeepEqual(got, []string{"show hostd:browser", "focus 2"}) {
		t.Fatalf("local focus commands %q", got)
	}
}

func TestFocusAndCloseWhileStarting(t *testing.T) {
	// Started by a phone while someone is there; it has no window yet
	// (Steam updates itself first). The person picks it in the menu: its
	// window comes to the front once it opens.
	r := newDisplayRig(t, true)
	r.input.press(t)
	waitFor(t, "present", r.m.presence.Present)
	r.start(t, "tv", "TV", sdk.SourceManual, false)
	r.setLive(contract.Instance{ID: "tv", State: "running"})
	act := func(typ, instance string) (sdk.Result, error) {
		return r.e.Submit(context.Background(), sdk.Action{Type: typ, Args: json.RawMessage(fmt.Sprintf(`{"instance":%q}`, instance)),
			Source: sdk.Source{Kind: sdk.SourceLocal}}, core.Auth{Scopes: []string{"apps"}})
	}
	res, err := act("window.focus", "tv")
	if err != nil || res.Status != sdk.StatusApplied || !strings.Contains(string(res.Data), `"waiting":true`) {
		t.Fatalf("focus while starting: %+v %v", res, err)
	}
	r.b.open(1, 100)
	r.event(t)
	if got := r.b.commands(); !reflect.DeepEqual(got, []string{"move 1 hostd:tv", "show hostd:tv", "focus 1", "fullscreen 1 true"}) {
		t.Fatalf("commands %q", got)
	}

	// Closing one without a window stops it.
	r.setLive(contract.Instance{ID: "notes", State: "running"})
	if _, err := act("window.close", "notes"); err != nil {
		t.Fatal(err)
	}
	if id := <-r.stopped; id != "notes" {
		t.Fatalf("stopped %s", id)
	}
	// Not running at all: still not found.
	if _, err := act("window.focus", "ghost"); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("focus without instance: %v", err)
	}
}
