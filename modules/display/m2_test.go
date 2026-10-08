package display

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/sdk"
)

// --- the Guide button --------------------------------------------------------------

// holdButton presses a controller button and keeps it held until hostd
// has acted on it.
func (r *displayRig) holdButton(t *testing.T, name string) {
	t.Helper()
	r.input.send(t, InputEvent{Buttons: []string{name}})
	waitFor(t, name+" held", func() bool {
		r.clock.Advance(100 * time.Millisecond)
		r.m.mu.Lock()
		defer r.m.mu.Unlock()
		_, held := r.m.holds[name]
		return !held
	})
}

func TestGuideButtonOpensMenu(t *testing.T) {
	r := newDisplayRig(t, true)
	r.holdButton(t, "guide")
	select {
	case a := <-r.started:
		if string(a.Args) != `{"id":"hostd-menu"}` || a.Source.Kind != sdk.SourceLocal {
			t.Fatalf("started %s from %+v", a.Args, a.Source)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the Guide button did nothing")
	}
	if !r.m.presence.present() {
		waitFor(t, "present", r.m.presence.present)
	}
}

func TestGuideTapIsTheApps(t *testing.T) {
	// A tap opens the app's own menu (Steam's); hostd waits for a hold.
	r := newDisplayRig(t, true)
	r.input.send(t, InputEvent{Buttons: []string{"guide"}})
	r.input.send(t, InputEvent{Released: []string{"guide"}})
	r.clock.Advance(time.Second)
	time.Sleep(50 * time.Millisecond)
	select {
	case a := <-r.started:
		t.Fatalf("a tap started %s", a.Args)
	default:
	}
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	if len(r.m.holds) != 0 {
		t.Fatalf("holds %v", r.m.holds)
	}
}

func TestGuideButtonTogglesBack(t *testing.T) {
	r := newDisplayRig(t, true)
	proc := `0::/user.slice/user-1000.slice/user@1000.service/app.slice/hostd-hostd-menu.service`
	r.procEnv(t, 500, proc)
	r.b.open(1, 100) // tv, in use
	r.event(t)
	r.b.focus(1)
	r.event(t)
	r.b.open(5, 500) // the menu, in front
	r.event(t)
	r.b.focus(5)
	r.event(t)
	r.b.commands()
	r.holdButton(t, "guide")
	waitFor(t, "back to tv", func() bool {
		r.b.mu.Lock()
		defer r.b.mu.Unlock()
		return reflect.DeepEqual(r.b.cmds, []string{"show hostd:tv", "focus 1"})
	})
	select {
	case a := <-r.started:
		t.Fatalf("started the menu again: %+v", a)
	default:
	}
}

func TestReadEventsGuide(t *testing.T) {
	var got []InputEvent
	var stream []byte
	stream = append(stream, inputEvent(evKey, btnMode, 1)...) // press
	stream = append(stream, inputEvent(0, 0, 0)...)
	readEvents(&chunked{data: stream, n: 1000}, map[uint16]string{btnMode: "guide"}, func(e InputEvent) { got = append(got, e) })
	stream = append(inputEvent(evKey, btnMode, 0), inputEvent(0, 0, 0)...) // release
	readEvents(&chunked{data: stream, n: 1000}, map[uint16]string{btnMode: "guide"}, func(e InputEvent) { got = append(got, e) })
	if len(got) != 2 || !reflect.DeepEqual(got[0].Buttons, []string{"guide"}) || len(got[1].Buttons) != 0 ||
		!reflect.DeepEqual(got[1].Released, []string{"guide"}) {
		t.Fatalf("events %+v", got)
	}
}

// --- windows outside the instance's unit ------------------------------------------------

func (r *displayRig) procEnv(t *testing.T, pid int, cgroup string, env ...string) {
	t.Helper()
	dir := filepath.Join(r.proc, fmt.Sprint(pid))
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "cgroup"), []byte(cgroup+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "environ"), []byte(strings.Join(env, "\x00")+"\x00"), 0o644)
}

func TestMatchRules(t *testing.T) {
	r := newDisplayRig(t, true)
	steamCG := `0::/user.slice/user-1000.slice/user@1000.service/app.slice/hostd-portal2.service`
	other := `0::/user.slice/user-1000.slice/user@1000.service/app.slice/app-flatpak-tv.kodi.Kodi-123.scope`
	r.setLive(
		// Steam in Flatpak, followed by its sandbox variable, which its
		// games have too.
		contract.Instance{ID: "steam", State: "running", Match: &contract.Match{Env: "FLATPAK_ID=com.valvesoftware.Steam"}},
		contract.Instance{ID: "portal2", State: "running", Match: &contract.Match{Class: "steam_app_620", Env: "SteamAppId=620"}},
		contract.Instance{ID: "kodi", State: "running", Match: &contract.Match{AppID: "tv.kodi.Kodi", Env: "FLATPAK_ID=tv.kodi.Kodi"}},
		contract.Instance{ID: "old", State: "exited", Match: &contract.Match{Class: "*"}},
	)
	// Steam itself runs in portal2's unit (the first launch started it):
	// its own window is not the game's.
	r.procEnv(t, 900, steamCG, "HOME=/home/screen")
	// The game, by class and by its SteamAppId.
	r.procEnv(t, 901, steamCG, "SteamAppId=620")
	r.procEnv(t, 902, `0::/user.slice/other.scope`)
	// Kodi in Flatpak's own scope, by its sandbox variable.
	r.procEnv(t, 903, other, "FLATPAK_ID=tv.kodi.Kodi")
	steamScope := `0::/user.slice/user-1000.slice/user@1000.service/app.slice/app-flatpak-com.valvesoftware.Steam-1.scope`
	r.procEnv(t, 904, steamScope, "FLATPAK_ID=com.valvesoftware.Steam")
	r.procEnv(t, 905, steamScope, "FLATPAK_ID=com.valvesoftware.Steam", "SteamAppId=620")

	for _, c := range []struct {
		w    Window
		want string
	}{
		{Window{ID: 10, PID: 900, Class: "steam"}, ""},
		{Window{ID: 11, PID: 901, Class: "portal2_linux"}, "portal2"},
		{Window{ID: 12, PID: 902, Class: "steam_app_620"}, "portal2"},
		{Window{ID: 13, PID: 903, AppID: "kodi"}, "kodi"},
		{Window{ID: 14, PID: 902, AppID: "tv.kodi.Kodi"}, "kodi"},
		{Window{ID: 15, PID: 902, AppID: "foot"}, ""},
		{Window{ID: 16, PID: 100, AppID: "x"}, "tv"}, // a plain cgroup match is unchanged
		{Window{ID: 17, PID: 904, Class: "steam"}, "steam"},
		{Window{ID: 18, PID: 905, Class: "steam_app_620"}, "portal2"}, // matches both: the game's rules win
	} {
		r.b.openWindow(c.w)
		_, tw := r.event(t)
		if tw.Instance != c.want {
			t.Errorf("window %d (%+v): instance %q, want %q", c.w.ID, c.w, tw.Instance, c.want)
		}
	}
	// Actions find matched windows too.
	r.b.commands()
	if _, err := r.act(t, "window.focus", "kodi"); err != nil {
		t.Fatal(err)
	}
	if got := r.b.commands(); len(got) != 2 || got[1] != "focus 13" && got[1] != "focus 14" {
		t.Fatalf("commands %q", got)
	}
}

// --- window.place and screens -------------------------------------------------------------

func (r *displayRig) submit(t *testing.T, typ, args string, src sdk.SourceKind, scopes ...string) (sdk.Result, error) {
	t.Helper()
	if len(scopes) == 0 {
		scopes = []string{sdk.ScopeAdmin}
	}
	return r.e.Submit(context.Background(), sdk.Action{Type: typ, Args: json.RawMessage(args),
		Source: sdk.Source{Kind: src}}, core.Auth{Scopes: scopes})
}

func TestPlace(t *testing.T) {
	r := newDisplayRig(t, true)
	r.b.open(1, 100)
	r.event(t)
	r.b.open(2, 200)
	r.event(t)
	r.b.commands()
	res, err := r.submit(t, "window.place", `{"instance":"browser","beside":"tv"}`, sdk.SourceManual)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.b.commands(); !reflect.DeepEqual(got, []string{"show hostd:tv", "place 2 beside 1"}) {
		t.Fatalf("commands %q (%s)", got, res.Data)
	}
	for args, code := range map[string]sdk.Code{
		`{"instance":"tv","beside":"tv"}`:      sdk.CodeInvalidArgs,
		`{"instance":"ghost","beside":"tv"}`:   sdk.CodeNotFound,
		`{"instance":"browser","beside":"no"}`: sdk.CodeNotFound,
	} {
		if _, err := r.submit(t, "window.place", args, sdk.SourceManual); sdk.CodeOf(err) != code {
			t.Errorf("%s: %v, want %s", args, err, code)
		}
	}
	// Someone at the screen keeps their layout.
	r.input.press(t)
	waitFor(t, "present", r.m.presence.present)
	if res, err := r.submit(t, "window.place", `{"instance":"browser","beside":"tv"}`, sdk.SourceManual); err != nil || res.Status != sdk.StatusSkipped {
		t.Fatalf("while in use: %+v %v", res, err)
	}
}

func TestScreens(t *testing.T) {
	r := newDisplayRig(t, true)
	for _, c := range []struct {
		typ, args string
		want      string
		code      sdk.Code
	}{
		{"display.power", `{"on":false}`, "output * power off", ""},
		{"display.power", `{"on":true,"output":"DP-1"}`, "output DP-1 power on", ""},
		{"display.power", `{"on":true,"output":"VGA-9"}`, "", sdk.CodeNotFound},
		{"display.mode", `{"output":"HDMI-A-1","mode":"1920x1080"}`, "output HDMI-A-1 mode 1920x1080@60.000Hz", ""},
		{"display.mode", `{"output":"HDMI-A-1","mode":"1920x1080@50"}`, "output HDMI-A-1 mode 1920x1080@50.000Hz", ""},
		{"display.mode", `{"output":"HDMI-A-1","mode":"1280x800@60"}`, "output HDMI-A-1 mode 1280x800@59.810Hz", ""},
		{"display.mode", `{"output":"HDMI-A-1","mode":"640x480"}`, "", sdk.CodeInvalidArgs},
		{"display.mode", `{"output":"HDMI-A-1","mode":"big"}`, "", sdk.CodeInvalidArgs},
		{"display.mode", `{"output":"DP-1","mode":"2560x1440@144"}`, "output DP-1 mode 2560x1440@144Hz", ""}, // lists no modes
		{"display.mode", `{"output":"*","mode":"1920x1080"}`, "", sdk.CodeInvalidArgs},
		{"display.output.enable", `{"output":"DP-1","enabled":false}`, "output DP-1 disable", ""},
		{"display.output.enable", `{"output":"DP-1"}`, "output DP-1 enable", ""},
	} {
		r.b.commands()
		_, err := r.submit(t, c.typ, c.args, sdk.SourceManual)
		if sdk.CodeOf(err) != c.code {
			t.Errorf("%s %s: %v, want %q", c.typ, c.args, err, c.code)
			continue
		}
		got := r.b.commands()
		if c.want != "" && (len(got) != 1 || got[0] != c.want) {
			t.Errorf("%s %s: commands %q, want %q", c.typ, c.args, got, c.want)
		}
	}

	// While someone watches, a phone may turn the screen on, not off.
	r.input.press(t)
	waitFor(t, "present", r.m.presence.present)
	r.b.commands()
	if res, err := r.submit(t, "display.power", `{"on":false}`, sdk.SourceManual); err != nil || res.Status != sdk.StatusSkipped {
		t.Fatalf("off while in use: %+v %v", res, err)
	}
	if res, err := r.submit(t, "display.power", `{"on":true}`, sdk.SourceManual); err != nil || res.Status != sdk.StatusApplied {
		t.Fatalf("on while in use: %+v %v", res, err)
	}
	if _, err := r.submit(t, "display.power", `{"on":false,"front":true}`, sdk.SourceManual, "display"); sdk.CodeOf(err) != sdk.CodeForbidden {
		t.Fatalf("front without its scope: %v", err)
	}
	if res, err := r.submit(t, "display.power", `{"on":false}`, sdk.SourceLocal); err != nil || res.Status != sdk.StatusApplied {
		t.Fatalf("off from the screen: %+v %v", res, err)
	}
}

func TestSwayPlaceAndOutputCommands(t *testing.T) {
	f := newFakeSway(t)
	ctx := context.Background()
	s, err := DialSway(ctx, f.path)
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

func TestResyncAfterLag(t *testing.T) {
	r := newDisplayRig(t, true)
	// Someone waits for notes to end; that event will be among the missed.
	done := make(chan struct{})
	go func() { r.m.waitEnded(context.Background(), "notes", time.Hour); close(done) }()
	waitFor(t, "waiting", func() bool { r.m.mu.Lock(); defer r.m.mu.Unlock(); return r.m.ending["notes"] != nil })
	windowed := false
	r.setLive(contract.Instance{ID: "tv", State: "running", Fullscreen: &windowed})

	events := make(chan sdk.Event, 1)
	events <- sdk.Event{Type: sdk.EventLagged}
	close(events)
	r.m.followInstances(context.Background(), events)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the wait for an instance that ended unseen did not end")
	}
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	if full, ok := r.m.prefs["tv"]; !ok || full {
		t.Fatalf("preferences not resynced: %v", r.m.prefs)
	}
}

func TestFailedCommandsAreCounted(t *testing.T) {
	r := newDisplayRig(t, true)
	r.b.mu.Lock()
	r.b.focusErr = errors.New("No matching node.")
	r.b.mu.Unlock()
	r.b.open(1, 100) // placing it focuses it: fails
	r.event(t)
	waitFor(t, "counted", func() bool {
		st, _ := r.m.Read(context.Background(), "display", nil)
		return st.(map[string]any)["failed_commands"] == 1
	})
}

func TestStartedInstanceClaimsOpenWindows(t *testing.T) {
	// Steam ran on (its instance had ended); started again, its new
	// instance hands off to it and takes its open window.
	r := newDisplayRig(t, true)
	r.procEnv(t, 904, `0::/user.slice/user-1000.slice/user@1000.service/app.slice/app-flatpak-com.valvesoftware.Steam-1.scope`,
		"FLATPAK_ID=com.valvesoftware.Steam")
	r.b.openWindow(Window{ID: 20, PID: 904, Class: "steam"})
	if _, tw := r.event(t); tw.Instance != "" {
		t.Fatalf("claimed by %q before its instance existed", tw.Instance)
	}
	r.b.commands()
	r.setLive(contract.Instance{ID: "steam", State: "running", Match: &contract.Match{Env: "FLATPAK_ID=com.valvesoftware.Steam"}})
	r.apps.Core().Emit(sdk.Event{Type: "instance.started", Source: &sdk.Source{Kind: sdk.SourceLocal},
		Data: json.RawMessage(`{"id":"steam","name":"Steam","fullscreen":true}`)})
	if _, tw := r.event(t); tw.Instance != "steam" {
		t.Fatalf("window instance %q", tw.Instance)
	}
	if got := r.b.commands(); !reflect.DeepEqual(got, []string{"move 20 hostd:steam", "show hostd:steam", "focus 20", "fullscreen 20 true"}) {
		t.Fatalf("commands %q", got)
	}
}

func TestShownByAnotherApp(t *testing.T) {
	// A game inside Steam's gamescope session has no window of its own:
	// starting it brings Steam's window, focusing it too, and closing it
	// stops the game only.
	r := newDisplayRig(t, true)
	r.input.press(t)
	waitFor(t, "present", r.m.presence.present)
	r.procEnv(t, 910, `0::/user.slice/user-1000.slice/user@1000.service/app.slice/hostd-steam.service`)
	steam := contract.Instance{ID: "steam", App: "steam", State: "running", Match: &contract.Match{AppID: "gamescope"}}
	r.setLive(steam)
	r.b.openWindow(Window{ID: 30, PID: 910, AppID: "gamescope"})
	r.event(t)
	r.b.open(1, 100) // tv, in front
	r.event(t)
	r.b.focus(1)
	r.event(t)
	r.b.commands()

	game := contract.Instance{ID: "steam-620", App: "steam-620", State: "running", ShownBy: "steam"}
	r.setLive(steam, game)
	r.start(t, "steam-620", "Portal 2", sdk.SourceLocal, false)
	r.apps.Core().Emit(sdk.Event{Type: "instance.started", Source: &sdk.Source{Kind: sdk.SourceLocal},
		Data: json.RawMessage(`{"id":"steam-620","app":"steam-620","shown_by":"steam"}`)})
	waitFor(t, "Steam's window in front", func() bool {
		r.b.mu.Lock()
		defer r.b.mu.Unlock()
		return reflect.DeepEqual(r.b.cmds, []string{"show hostd:steam", "focus 30"})
	})
	r.b.commands()
	r.b.focus(1)
	r.event(t)
	if res, err := r.submit(t, "window.focus", `{"instance":"steam-620"}`, sdk.SourceLocal); err != nil || res.Status != sdk.StatusApplied {
		t.Fatalf("focus: %+v %v", res, err)
	}
	if got := r.b.commands(); !reflect.DeepEqual(got, []string{"show hostd:steam", "focus 30"}) {
		t.Fatalf("focus commands %q", got)
	}
	if _, err := r.submit(t, "window.close", `{"instance":"steam-620"}`, sdk.SourceLocal); err != nil {
		t.Fatal(err)
	}
	if id := <-r.stopped; id != "steam-620" {
		t.Fatalf("stopped %s", id)
	}
	if got := r.b.commands(); len(got) != 0 {
		t.Fatalf("closing the game touched Steam's window: %q", got)
	}
}
