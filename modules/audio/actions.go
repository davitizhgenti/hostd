package audio

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/davitizhgenti/hostd/sdk"
	"github.com/davitizhgenti/hostd/sdk/kit"
)

// Percent is a volume as an action gives it: 0-150, or a change such as
// "+5" or "-5".
type Percent struct{ raw json.RawMessage }

func (p *Percent) UnmarshalJSON(b []byte) error { p.raw = append(p.raw[:0], b...); return nil }
func (p Percent) MarshalJSON() ([]byte, error) {
	if p.raw == nil {
		return []byte("null"), nil
	}
	return p.raw, nil
}

func (*Percent) JSONSchema() json.RawMessage {
	return json.RawMessage(`{"oneOf":[{"type":"integer","minimum":0,"maximum":150},{"type":"string","pattern":"^([+-][0-9]{1,3}|[0-9]{1,3})$"}]}`)
}

// From works out the new volume from the current one, kept in 0-150.
func (p Percent) From(current int) (int, error) {
	var n int
	if json.Unmarshal(p.raw, &n) == nil {
		return n, nil
	}
	var s string
	if err := json.Unmarshal(p.raw, &s); err != nil {
		return 0, sdk.Errorf(sdk.CodeInvalidArgs, "percent must be a number or a change such as \"+5\"")
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, sdk.Errorf(sdk.CodeInvalidArgs, "percent %q is not a number", s)
	}
	if strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-") {
		v += current
	}
	return max(0, min(150, v)), nil
}

// Mute is a mute as an action gives it: true or "on", false or "off", or
// "toggle" (also when it is left out).
type Mute struct{ raw json.RawMessage }

func (m *Mute) UnmarshalJSON(b []byte) error { m.raw = append(m.raw[:0], b...); return nil }
func (m Mute) MarshalJSON() ([]byte, error) {
	if m.raw == nil {
		return []byte("null"), nil
	}
	return m.raw, nil
}

func (*Mute) JSONSchema() json.RawMessage {
	return json.RawMessage(`{"default":"toggle","oneOf":[{"type":"boolean"},{"enum":["toggle","on","off"]}]}`)
}

// Wanted is the mute to set, given the current one.
func (m Mute) Wanted(now bool) bool {
	switch strings.Trim(strings.TrimSpace(string(m.raw)), `"`) {
	case "true", "on":
		return true
	case "false", "off":
		return false
	}
	return !now
}

// The arguments and results of the module's actions.
type (
	volumeArgs struct {
		Percent Percent `json:"percent" desc:"0-150, or a change such as \"+5\" or \"-5\""`
	}
	muteArgs struct {
		Muted Mute `json:"muted,omitempty" desc:"true or on, false or off, or toggle (the default)"`
	}
	outputArgs struct {
		Output string `json:"output" desc:"an output's name, as GET /v1/audio lists them (hdmi, speakers, bt-...)"`
	}
	appVolumeArgs struct {
		Instance string  `json:"instance"`
		Percent  Percent `json:"percent" desc:"0-150, or a change such as \"+5\" or \"-5\""`
	}
	appMuteArgs struct {
		Instance string `json:"instance"`
		Muted    Mute   `json:"muted,omitempty" desc:"true or on, false or off, or toggle (the default)"`
	}
	mediaArgs struct {
		Player string `json:"player,omitempty" desc:"a player's name (GET /v1/media); default: the app in front's, or the one playing"`
	}

	outputSet struct {
		Output string `json:"output"`
		Moved  int    `json:"moved"`
	}
	appState struct {
		Instance string `json:"instance"`
		Percent  *int   `json:"percent,omitempty"`
		Muted    *bool  `json:"muted,omitempty"`
	}
	mediaSent struct {
		Player   string `json:"player"`
		Instance string `json:"instance"`
	}
)

// masterDone is how hostctl prints the volume: after a change, and for
// hostctl volume alone (the audio read).
var masterDone = []string{"volume {percent}% (muted) on {description|sink} if muted", "volume {percent}% on {description|sink}"}

// declare is the module's actions.
func (m *Module) declare() *kit.Actions {
	t := kit.NewActions("audio")
	kit.On(t, sdk.ActionSpec{Type: "audio.volume.set", Description: "Set the volume (0-150) or change it (\"+5\", \"-5\")",
		Keys: []sdk.KeyTemplate{resource}, Scope: "audio", Timeout: sdk.Duration(10 * time.Second),
		Route: &sdk.Route{Method: "POST", Path: "/v1/audio/volume"},
		CLI:   &sdk.CLI{Alias: []string{"volume"}, Read: "audio", Done: masterDone}},
		func(ctx context.Context, a sdk.Action, args volumeArgs) (Master, error) {
			return m.setMaster(ctx, a, func(want *Master) error {
				p, err := args.Percent.From(want.Percent)
				if err != nil {
					return err
				}
				want.Percent = p
				m.expect(*want)
				return m.opts.Backend.SetVolume(ctx, p)
			})
		})
	kit.On(t, sdk.ActionSpec{Type: "audio.mute.set", Description: "Mute, unmute or toggle",
		Keys: []sdk.KeyTemplate{resource}, Scope: "audio", Timeout: sdk.Duration(10 * time.Second),
		Route: &sdk.Route{Method: "POST", Path: "/v1/audio/mute"},
		CLI:   &sdk.CLI{Alias: []string{"mute"}, Done: masterDone}},
		func(ctx context.Context, a sdk.Action, args muteArgs) (Master, error) {
			return m.setMaster(ctx, a, func(want *Master) error {
				want.Muted = args.Muted.Wanted(want.Muted)
				m.expect(*want)
				return m.opts.Backend.SetMute(ctx, want.Muted)
			})
		})
	kit.On(t, sdk.ActionSpec{Type: "audio.output.set", Description: "Send sound to an output; what plays moves there too",
		Keys: []sdk.KeyTemplate{"audio.output"}, Scope: "audio", Timeout: sdk.Duration(15 * time.Second),
		Route: &sdk.Route{Method: "POST", Path: "/v1/audio/output"}}, m.setOutput)
	kit.On(t, sdk.ActionSpec{Type: "audio.app.volume.set", Description: "Set an app's own volume (0-150) or change it",
		Keys: []sdk.KeyTemplate{"audio.app:{instance}"}, Scope: "audio", Timeout: sdk.Duration(10 * time.Second),
		Route: &sdk.Route{Method: "POST", Path: "/v1/audio/apps/{instance}/volume"}},
		func(ctx context.Context, a sdk.Action, args appVolumeArgs) (appState, error) {
			return m.setApp(ctx, a, args.Instance, func(gb GraphBackend, streams []Stream, state *appState) error {
				p, err := args.Percent.From(streams[0].Percent)
				if err != nil {
					return err
				}
				for _, s := range streams {
					if err := gb.SetNodeVolume(ctx, s.ID, p); err != nil {
						return err
					}
				}
				state.Percent = &p
				return nil
			})
		})
	kit.On(t, sdk.ActionSpec{Type: "audio.app.mute.set", Description: "Mute, unmute or toggle an app",
		Keys: []sdk.KeyTemplate{"audio.app:{instance}"}, Scope: "audio", Timeout: sdk.Duration(10 * time.Second),
		Route: &sdk.Route{Method: "POST", Path: "/v1/audio/apps/{instance}/mute"}},
		func(ctx context.Context, a sdk.Action, args appMuteArgs) (appState, error) {
			return m.setApp(ctx, a, args.Instance, func(gb GraphBackend, streams []Stream, state *appState) error {
				muted := args.Muted.Wanted(streams[0].Muted)
				for _, s := range streams {
					if err := gb.SetNodeMute(ctx, s.ID, muted); err != nil {
						return err
					}
				}
				state.Muted = &muted
				return nil
			})
		})
	for _, k := range []struct{ typ, desc, path, method string }{
		{"media.play_pause", "Play or pause", "/v1/media/play-pause", "PlayPause"},
		{"media.next", "Next track", "/v1/media/next", "Next"},
		{"media.previous", "Previous track", "/v1/media/previous", "Previous"},
	} {
		kit.On(t, sdk.ActionSpec{Type: k.typ, Description: k.desc,
			Keys: []sdk.KeyTemplate{"media"}, Scope: "audio", Timeout: sdk.Duration(10 * time.Second),
			Route: &sdk.Route{Method: "POST", Path: k.path}},
			func(ctx context.Context, _ sdk.Action, args mediaArgs) (mediaSent, error) {
				return m.sendMedia(ctx, args.Player, k.method)
			})
	}
	return t
}

// setMaster changes the volume or mute (set), then reads back what the
// system really did and announces it.
func (m *Module) setMaster(ctx context.Context, a sdk.Action, set func(want *Master) error) (Master, error) {
	m.mu.Lock()
	want := m.last
	m.mu.Unlock()
	if err := set(&want); err != nil {
		return Master{}, err
	}
	if got, err := m.opts.Backend.Master(ctx); err == nil {
		m.expect(got)
		want = got
	}
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	core.Emit(sdk.Event{Type: EventVolumeChanged, Action: a.ID, Resource: resource, Data: sdk.MustJSON(want)})
	return want, nil
}
