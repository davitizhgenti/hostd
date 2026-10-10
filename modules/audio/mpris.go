// Media players (MPRIS, M3): play/pause, next and previous for the app in
// front, and media.changed when a player starts, stops or changes track.
package audio

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/davitizhgenti/hostd/sdk"
)

// EventMediaChanged is emitted when a player starts, stops, pauses or
// changes track.
const EventMediaChanged = "media.changed"

// Player is a media player on the session bus.
type Player struct {
	Name     string `json:"name"` // its bus name after org.mpris.MediaPlayer2.
	PID      int    `json:"pid,omitempty"`
	Instance string `json:"instance,omitempty"`
	Status   string `json:"status"` // Playing, Paused or Stopped
	Title    string `json:"title,omitempty"`
	Artist   string `json:"artist,omitempty"`
}

// Media is the session's media players.
type Media interface {
	Players(ctx context.Context) ([]Player, error)
	// Command calls an MPRIS Player method (PlayPause, Next, Previous) on
	// the player with that name.
	Command(ctx context.Context, name, method string) error
	// Watch calls fn when a player may have changed, until ctx ends or the
	// bus goes away (then it returns an error).
	Watch(ctx context.Context, fn func()) error
}

const mprisPrefix = "org.mpris.MediaPlayer2."

// DBusMedia talks MPRIS on the user's session bus.
type DBusMedia struct {
	RuntimeDir string // where the user bus is: <RuntimeDir>/bus

	mu   sync.Mutex
	conn *dbus.Conn
}

func (d *DBusMedia) connect(ctx context.Context) (*dbus.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conn != nil && d.conn.Connected() {
		return d.conn, nil
	}
	// Never let the library autolaunch a bus: only the session's own will do.
	addr := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if addr == "" {
		path := filepath.Join(d.RuntimeDir, "bus")
		if fi, err := os.Stat(path); err != nil || fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("no user bus at %s", path)
		}
		addr = "unix:path=" + path
	}
	conn, err := dbus.Connect(addr, dbus.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	d.conn = conn
	return conn, nil
}

func (d *DBusMedia) Players(ctx context.Context) ([]Player, error) {
	conn, err := d.connect(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.ListNames", 0).Store(&names); err != nil {
		return nil, err
	}
	var out []Player
	for _, n := range names {
		if !strings.HasPrefix(n, mprisPrefix) {
			continue
		}
		p := Player{Name: strings.TrimPrefix(n, mprisPrefix)}
		var pid uint32
		if conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetConnectionUnixProcessID", 0, n).Store(&pid) == nil {
			p.PID = int(pid)
		}
		obj := conn.Object(n, "/org/mpris/MediaPlayer2")
		if v, err := obj.GetProperty("org.mpris.MediaPlayer2.Player.PlaybackStatus"); err == nil {
			p.Status, _ = v.Value().(string)
		}
		if v, err := obj.GetProperty("org.mpris.MediaPlayer2.Player.Metadata"); err == nil {
			if md, ok := v.Value().(map[string]dbus.Variant); ok {
				p.Title, _ = md["xesam:title"].Value().(string)
				if artists, ok := md["xesam:artist"].Value().([]string); ok {
					p.Artist = strings.Join(artists, ", ")
				}
			}
		}
		out = append(out, p)
	}
	return out, nil
}

func (d *DBusMedia) Command(ctx context.Context, name, method string) error {
	conn, err := d.connect(ctx)
	if err != nil {
		return err
	}
	return conn.Object(mprisPrefix+name, "/org/mpris/MediaPlayer2").
		CallWithContext(ctx, "org.mpris.MediaPlayer2.Player."+method, 0).Err
}

func (d *DBusMedia) Watch(ctx context.Context, fn func()) error {
	conn, err := d.connect(ctx)
	if err != nil {
		return err
	}
	opts := [][]dbus.MatchOption{
		{dbus.WithMatchInterface("org.freedesktop.DBus.Properties"), dbus.WithMatchMember("PropertiesChanged"),
			dbus.WithMatchObjectPath("/org/mpris/MediaPlayer2")},
		{dbus.WithMatchInterface("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged"),
			dbus.WithMatchArg0Namespace("org.mpris.MediaPlayer2")},
	}
	for _, o := range opts {
		if err := conn.AddMatchSignalContext(ctx, o...); err != nil {
			return err
		}
	}
	signals := make(chan *dbus.Signal, 64)
	conn.Signal(signals)
	defer conn.RemoveSignal(signals)
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-signals:
			if !ok {
				return fmt.Errorf("the session bus went away")
			}
			fn()
		}
	}
}

// players lists the players, each with its instance.
func (m *Module) players(ctx context.Context) ([]Player, error) {
	if m.opts.Media == nil {
		return nil, sdk.Errorf(sdk.CodeModuleUnavailable, "no media players: no session bus")
	}
	ps, err := m.opts.Media.Players(ctx)
	if err != nil {
		return nil, sdk.Errorf(sdk.CodeModuleUnavailable, "media players: %v", err)
	}
	live := m.liveInstances(ctx)
	for i := range ps {
		ps[i].Instance = m.instanceOf(ps[i].PID, live)
	}
	return ps, nil
}

// focusedInstance asks the display module which instance is in front.
func (m *Module) focusedInstance(ctx context.Context) string {
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	if core == nil {
		return ""
	}
	raw, err := core.Read(ctx, "display", "display", nil)
	if err != nil {
		return ""
	}
	var st struct {
		Focused string `json:"focused_instance"`
	}
	_ = json.Unmarshal(raw, &st)
	return st.Focused
}

// pick chooses the player a media key is for: the one named, else the
// app in front's, else the one playing, else the first.
func pick(players []Player, name, focused string) (Player, bool) {
	if name != "" {
		for _, p := range players {
			if p.Name == name || strings.HasPrefix(p.Name, name+".") {
				return p, true
			}
		}
		return Player{}, false
	}
	for _, p := range players {
		if focused != "" && p.Instance == focused {
			return p, true
		}
	}
	for _, p := range players {
		if p.Status == "Playing" {
			return p, true
		}
	}
	if len(players) > 0 {
		return players[0], true
	}
	return Player{}, false
}

var mediaMethods = map[string]string{"media.play_pause": "PlayPause", "media.next": "Next", "media.previous": "Previous"}

func (m *Module) handleMedia(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	var args struct {
		Player string `json:"player"`
	}
	if err := a.DecodeArgs(&args); err != nil {
		return sdk.Result{}, err
	}
	ps, err := m.players(ctx)
	if err != nil {
		return sdk.Result{}, err
	}
	p, ok := pick(ps, args.Player, m.focusedInstance(ctx))
	if !ok {
		if args.Player != "" {
			return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "no media player %q", args.Player)
		}
		return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "no media player is running")
	}
	if err := m.opts.Media.Command(ctx, p.Name, mediaMethods[a.Type]); err != nil {
		return sdk.Result{}, sdk.Errorf(sdk.CodeModuleUnavailable, "%s: %v", p.Name, err)
	}
	return sdk.Result{Data: sdk.MustJSON(map[string]string{"player": p.Name, "instance": p.Instance})}, nil
}

// followMedia announces players starting, stopping and changing track.
func (m *Module) followMedia(ctx context.Context) {
	if m.opts.Media == nil {
		return
	}
	last := ""
	report := func() {
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		ps, err := m.players(rctx)
		if err != nil {
			return
		}
		if ps == nil {
			ps = []Player{}
		}
		now := string(sdk.MustJSON(ps))
		m.mu.Lock()
		changed := now != last
		last = now
		m.media = ps
		core := m.core
		m.mu.Unlock()
		if changed && core != nil {
			core.Emit(sdk.Event{Type: EventMediaChanged, Data: json.RawMessage(now),
				Source: &sdk.Source{Kind: sdk.SourceExternal, Name: "mpris"}})
		}
	}
	for ctx.Err() == nil {
		report()
		err := m.opts.Media.Watch(ctx, report)
		if ctx.Err() != nil {
			return
		}
		m.log.Debug("media players: watching again", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-m.opts.Clock.After(m.opts.RetryEvery):
		}
	}
}

// mediaActions are the media keys.
func mediaActions() []sdk.ActionSpec {
	schema := json.RawMessage(`{"type":"object","properties":{
		"player":{"type":"string","description":"a player's name (GET /v1/media); default: the app in front's, or the one playing"}}}`)
	var out []sdk.ActionSpec
	for _, a := range []struct{ typ, desc, path string }{
		{"media.play_pause", "Play or pause", "/v1/media/play-pause"},
		{"media.next", "Next track", "/v1/media/next"},
		{"media.previous", "Previous track", "/v1/media/previous"},
	} {
		out = append(out, sdk.ActionSpec{Type: a.typ, Description: a.desc, Schema: schema,
			Keys: []sdk.KeyTemplate{"media"}, Scope: "audio", Timeout: sdk.Duration(10 * time.Second),
			Route: &sdk.Route{Method: "POST", Path: a.path}})
	}
	return out
}
