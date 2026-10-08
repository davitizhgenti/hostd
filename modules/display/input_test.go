package display

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/sdk"
)

func TestBindingsConfig(t *testing.T) {
	keys, buttons, err := Bindings(nil, nil)
	if err != nil || !reflect.DeepEqual(keys, DefaultKeys) || !reflect.DeepEqual(buttons, DefaultButtons) {
		t.Fatalf("defaults: %v %v %v", keys, buttons, err)
	}
	keys, _, err = Bindings(map[string]string{"F1": "display.menu", "Super+Q": ""}, nil)
	if err != nil || keys["F1"] != "display.menu" || keys["Super+Q"] != "" || keys["Super"] != "display.menu" {
		t.Fatalf("override: %v %v", keys, err)
	}
	if _, ok := keys["Super+Q"]; ok {
		t.Fatal("an empty binding did not remove the default")
	}
	for name, c := range map[string]struct {
		keys, buttons map[string]string
		want          string
	}{
		"unknown action":   {map[string]string{"F2": "window.explode"}, nil, `"window.explode" cannot be bound`},
		"needs arguments":  {map[string]string{"F2": "window.focus"}, nil, `"window.focus" cannot be bound`},
		"bad modifier":     {map[string]string{"Hyper+Tab": "window.next"}, nil, `"Hyper" is not a modifier`},
		"bad key name":     {map[string]string{"Super+?": "window.next"}, nil, `not a key name`},
		"empty part":       {map[string]string{"Super++": "window.next"}, nil, `empty part`},
		"unknown button":   {nil, map[string]string{"turbo": "window.next"}, `button "turbo"`},
		"button to action": {nil, map[string]string{"guide": "audio.mute.set"}, `cannot be bound`},
	} {
		if _, _, err := Bindings(c.keys, c.buttons); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
	// What can be bound: the display actions without required arguments.
	want := []string{"display.menu", "window.back", "window.close", "window.next", "window.prev"}
	if got := Bindable(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Bindable = %v", got)
	}
}

func TestSwayKeys(t *testing.T) {
	for name, want := range map[string]string{
		"Super":           "--release Mod4+Super_L",
		"Super+Tab":       "--no-repeat Mod4+Tab",
		"Super+Shift+Tab": "--no-repeat Mod4+Shift+Tab",
		"Super+Q":         "--no-repeat Mod4+q",
		"Ctrl+Alt+Delete": "--no-repeat Control+Mod1+Delete",
		"F1":              "--no-repeat F1",
		"Q":               "--no-repeat q",
	} {
		flags, combo := swayKeys(name)
		if got := flags + " " + combo; got != want {
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
	s, err := DialSway(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Disconnect()
	_ = s.Bind(ctx, []string{"Super", "Super+Tab"})
	_ = s.Bind(ctx, []string{"F1"})
	want := []string{
		`bindsym --release Mod4+Super_L nop hostd key Super`,
		`bindsym --no-repeat Mod4+Tab nop hostd key Super+Tab`,
		`unbindsym --release Mod4+Super_L`,
		`unbindsym --no-repeat Mod4+Tab`,
		`bindsym --no-repeat F1 nop hostd key F1`,
	}
	if got := f.sent(); !reflect.DeepEqual(got, want) {
		t.Fatalf("commands\n%q\nwant\n%q", got, want)
	}
	var got []string
	_ = s.Watch(ctx, func(ev Event) {
		if ev.Change == "binding" || ev.Change == "reload" {
			got = append(got, ev.Change+" "+ev.Binding)
		}
	})
	if !reflect.DeepEqual(got, []string{"binding Super+Tab", "reload "}) {
		t.Fatalf("events %q", got)
	}
}

// --- the module: keys and buttons run actions as the person at the screen ---

func (r *displayRig) actionSources(t *testing.T) <-chan sdk.Event {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return r.e.Subscribe(ctx, "action.done")
}

func TestKeysRunActions(t *testing.T) {
	r := newDisplayRig(t, true)
	waitFor(t, "bindings installed", func() bool { return r.b.bindCalls() == 1 })
	r.b.mu.Lock()
	bound := r.b.bound[0]
	r.b.mu.Unlock()
	if !reflect.DeepEqual(bound, []string{"Super", "Super+Q", "Super+Shift+Tab", "Super+Tab"}) {
		t.Fatalf("bound %q", bound)
	}
	done := r.actionSources(t)
	r.b.open(1, 100) // tv
	r.event(t)
	r.b.open(2, 200) // browser
	r.event(t)
	r.b.focus(1)
	r.event(t)
	r.b.commands()

	r.b.key("Super+Tab")
	select {
	case ev := <-done:
		if ev.Source == nil || ev.Source.Kind != sdk.SourceLocal || ev.Source.Name != "keyboard" {
			t.Fatalf("action %s from %+v", ev.Data, ev.Source)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Super+Tab did nothing")
	}
	waitFor(t, "next app", func() bool {
		r.b.mu.Lock()
		defer r.b.mu.Unlock()
		return reflect.DeepEqual(r.b.cmds, []string{"show hostd:browser", "focus 2"})
	})

	// A config reload drops the bindings: they are installed again.
	r.b.events <- Event{Change: "reload"}
	waitFor(t, "bindings again", func() bool { return r.b.bindCalls() == 2 })
}

func TestBackNextPrev(t *testing.T) {
	r := newDisplayRig(t, true)
	r.b.open(1, 100) // tv
	r.event(t)
	r.b.open(2, 200) // browser
	r.event(t)
	r.b.open(4, 400) // notes
	r.event(t)
	r.b.focus(1)
	r.event(t)
	r.b.focus(4)
	r.event(t)
	r.b.commands()
	local := func(typ string) sdk.Result {
		t.Helper()
		res, err := r.submit(t, typ, `{}`, sdk.SourceLocal)
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		return res
	}
	// back: to tv, focused before notes.
	local("window.back")
	if got := r.b.commands(); !reflect.DeepEqual(got, []string{"show hostd:tv", "focus 1"}) {
		t.Fatalf("back %q", got)
	}
	// next and prev go by instance ID: browser, notes, tv. From notes
	// (still the focused one: the fake does not report focus by itself).
	local("window.next")
	if got := r.b.commands(); !reflect.DeepEqual(got, []string{"show hostd:tv", "focus 1"}) {
		t.Fatalf("next %q", got)
	}
	local("window.prev")
	if got := r.b.commands(); !reflect.DeepEqual(got, []string{"show hostd:browser", "focus 2"}) {
		t.Fatalf("prev %q", got)
	}
	// No history (as after a hostd restart): back goes to another app.
	r.m.mu.Lock()
	r.m.stack = []string{"notes"}
	r.m.mu.Unlock()
	local("window.back")
	if got := r.b.commands(); len(got) != 2 || got[0] == "show hostd:notes" {
		t.Fatalf("back without history %q", got)
	}
	// Only one app: nothing to go to.
	solo := newDisplayRig(t, true)
	solo.b.open(1, 100)
	solo.event(t)
	solo.b.focus(1)
	solo.event(t)
	for _, typ := range []string{"window.back", "window.next"} {
		res, err := solo.submit(t, typ, `{}`, sdk.SourceLocal)
		if err != nil || res.Status != sdk.StatusSkipped {
			t.Fatalf("%s alone: %+v %v", typ, res, err)
		}
	}
}

func TestCloseFront(t *testing.T) {
	r := newDisplayRig(t, true)
	r.b.open(1, 100) // tv
	r.event(t)
	r.b.open(9, 300) // a window hostd did not start
	r.event(t)
	r.b.focus(9)
	r.event(t)
	r.b.commands()
	if _, err := r.submit(t, "window.close", `{}`, sdk.SourceLocal); err != nil {
		t.Fatal(err)
	}
	if got := r.b.commands(); !reflect.DeepEqual(got, []string{"close 9"}) {
		t.Fatalf("unowned %q", got)
	}
	r.event(t) // closed
	r.b.focus(1)
	r.event(t)
	r.b.commands()
	if res, err := r.submit(t, "window.close", `{}`, sdk.SourceLocal); err != nil || !strings.Contains(string(res.Data), `"instance":"tv"`) {
		t.Fatalf("owned: %s %v", res.Data, err)
	}
	if got := r.b.commands(); len(got) == 0 || got[0] != "close 1" {
		t.Fatalf("owned %q", got)
	}
	r.event(t)
	if _, err := r.submit(t, "window.close", `{}`, sdk.SourceLocal); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("nothing in front: %v", err)
	}
}
