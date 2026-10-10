package audio

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"

	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/sdk"
)

func TestPick(t *testing.T) {
	ps := []Player{
		{Name: "chromium.instance42", Instance: "browser", Status: "Paused"},
		{Name: "vlc", Instance: "vlc", Status: "Playing"},
	}
	for _, c := range []struct{ name, focused, want string }{
		{"", "browser", "chromium.instance42"},  // the app in front's
		{"", "tv", "vlc"},                       // else the one playing
		{"chromium", "", "chromium.instance42"}, // named, without its instance suffix
		{"vlc", "browser", "vlc"},
	} {
		p, ok := pick(ps, c.name, c.focused)
		if !ok || p.Name != c.want {
			t.Errorf("pick(%q, %q) = %q, want %q", c.name, c.focused, p.Name, c.want)
		}
	}
	if _, ok := pick(ps, "spotify", ""); ok {
		t.Error("an unknown player was picked")
	}
	if _, ok := pick(nil, "", ""); ok {
		t.Error("picked from no players")
	}
}

type fakeMedia struct {
	mu       sync.Mutex
	players  []Player
	commands []string
	changed  chan struct{}
}

func (f *fakeMedia) Players(context.Context) ([]Player, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Player(nil), f.players...), nil
}
func (f *fakeMedia) Command(_ context.Context, name, method string) error {
	f.mu.Lock()
	f.commands = append(f.commands, name+" "+method)
	f.mu.Unlock()
	return nil
}
func (f *fakeMedia) Watch(ctx context.Context, fn func()) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-f.changed:
			fn()
		}
	}
}

func TestMediaKeysAndChanges(t *testing.T) {
	media := &fakeMedia{players: []Player{{Name: "chromium.instance100", PID: 100, Status: "Paused"}}, changed: make(chan struct{}, 4)}
	r := newM3Rig(t, media) // the browser runs as pid 100's unit
	events := r.e.Subscribe(t.Context(), EventMediaChanged)
	waitFor(t, "first look", func() bool {
		ps, _ := r.m.Read(context.Background(), "media", nil)
		l, _ := ps.([]Player)
		return len(l) == 1 && l[0].Instance == "browser"
	})
	res, err := r.e.Submit(context.Background(), sdk.Action{Type: "media.play_pause", Args: json.RawMessage(`{}`),
		Source: sdk.Source{Kind: sdk.SourceManual}}, core.Auth{Scopes: []string{sdk.ScopeAdmin}})
	if err != nil || !strings.Contains(string(res.Data), "chromium.instance100") {
		t.Fatalf("play/pause: %s %v", res.Data, err)
	}
	media.mu.Lock()
	media.players[0].Status = "Playing"
	got := media.commands
	media.mu.Unlock()
	if len(got) != 1 || got[0] != "chromium.instance100 PlayPause" {
		t.Fatalf("commands %q", got)
	}
	media.changed <- struct{}{}
	deadline := time.After(5 * time.Second)
	for playing := false; !playing; {
		select {
		case ev := <-events: // the first look may come first
			playing = strings.Contains(string(ev.Data), `"status":"Playing"`)
		case <-deadline:
			t.Fatal("no media.changed after the player changed")
		}
	}
}

// privateBus starts a session bus of its own for the test.
func privateBus(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("no dbus-daemon")
	}
	cmd := exec.Command("dbus-daemon", "--session", "--nofork", "--print-address=1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("dbus-daemon: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	addr, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(addr)
}

type testPlayer struct {
	mu     sync.Mutex
	pauses int
}

func (p *testPlayer) PlayPause() *dbus.Error { p.mu.Lock(); p.pauses++; p.mu.Unlock(); return nil }

func TestDBusMedia(t *testing.T) {
	addr := privateBus(t)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	player := &testPlayer{}
	const path = "/org/mpris/MediaPlayer2"
	if err := conn.Export(player, path, "org.mpris.MediaPlayer2.Player"); err != nil {
		t.Fatal(err)
	}
	props, err := prop.Export(conn, path, prop.Map{"org.mpris.MediaPlayer2.Player": {
		"PlaybackStatus": {Value: "Paused", Emit: prop.EmitTrue},
		"Metadata": {Value: map[string]dbus.Variant{"xesam:title": dbus.MakeVariant("Song"),
			"xesam:artist": dbus.MakeVariant([]string{"Band"})}, Emit: prop.EmitTrue},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.RequestName(mprisPrefix+"testplayer", dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}

	d := &DBusMedia{}
	ctx := context.Background()
	ps, err := d.Players(ctx)
	if err != nil || len(ps) != 1 {
		t.Fatalf("players %+v %v", ps, err)
	}
	if p := ps[0]; p.Name != "testplayer" || p.Status != "Paused" || p.Title != "Song" || p.Artist != "Band" || p.PID != os.Getpid() {
		t.Fatalf("player %+v", p)
	}
	if err := d.Command(ctx, "testplayer", "PlayPause"); err != nil {
		t.Fatal(err)
	}
	player.mu.Lock()
	n := player.pauses
	player.mu.Unlock()
	if n != 1 {
		t.Fatalf("PlayPause called %d times", n)
	}
	// A change on the player reaches Watch.
	wctx, cancel := context.WithCancel(ctx)
	seen := make(chan struct{}, 4)
	done := make(chan struct{})
	go func() { _ = d.Watch(wctx, func() { seen <- struct{}{} }); close(done) }()
	defer func() { cancel(); <-done }()
	time.Sleep(100 * time.Millisecond) // the match rules are in place
	props.SetMust("org.mpris.MediaPlayer2.Player", "PlaybackStatus", "Playing")
	select {
	case <-seen:
	case <-time.After(5 * time.Second):
		t.Fatal("Watch did not see the change")
	}
}
