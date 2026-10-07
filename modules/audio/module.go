// Package audio is the audio module: master volume and mute of the default
// output, kept in step with changes made elsewhere (a game's own slider, a
// headset connecting). Output switching, per-app volume and media keys
// come in M3.
package audio

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/davitizhgenti/hostd/internal/clock"
	"github.com/davitizhgenti/hostd/sdk"
)

// EventVolumeChanged is emitted when volume or mute changes, by hostd or by
// anyone else.
const EventVolumeChanged = "audio.volume.changed"

const resource = "audio.master"

// Options configure the audio module.
type Options struct {
	Backend    Backend
	Clock      clock.Clock
	Logger     *slog.Logger
	RetryEvery time.Duration // between attempts to reach PipeWire (default 2s)
	Settle     time.Duration // wait after a change cue before reading (default 150ms)
}

// Module is the audio module.
type Module struct {
	opts Options
	log  *slog.Logger

	mu        sync.Mutex
	core      sdk.Core
	available bool
	last      Master
	settle    clock.Timer

	stop context.CancelFunc
	done sync.WaitGroup
}

// New returns the audio module.
func New(opts Options) *Module {
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.RetryEvery == 0 {
		opts.RetryEvery = 2 * time.Second
	}
	if opts.Settle == 0 {
		opts.Settle = 150 * time.Millisecond
	}
	return &Module{opts: opts, log: opts.Logger}
}

var (
	volumeSchema = json.RawMessage(`{"type":"object","properties":{
		"percent":{"description":"0-150, or a change such as \"+5\" or \"-5\"",
			"oneOf":[{"type":"integer","minimum":0,"maximum":150},{"type":"string","pattern":"^([+-][0-9]{1,3}|[0-9]{1,3})$"}]}},
		"required":["percent"]}`)
	muteSchema = json.RawMessage(`{"type":"object","properties":{
		"muted":{"description":"true, false or \"toggle\"",
			"oneOf":[{"type":"boolean"},{"const":"toggle"}]}},
		"required":["muted"]}`)
)

func (m *Module) Manifest() sdk.Manifest {
	return sdk.Manifest{
		Name: "audio", Version: "0.1.0",
		Owns:   []string{"audio.*", "media.*"},
		Scopes: []sdk.ScopeSpec{{Name: "audio", Description: "Volume, mute, outputs, per-app audio, media keys"}},
		Actions: []sdk.ActionSpec{
			{Type: "audio.volume.set", Description: "Set the volume (0-150) or change it (\"+5\", \"-5\")",
				Schema: volumeSchema, Keys: []sdk.KeyTemplate{resource}, Scope: "audio",
				Timeout: sdk.Duration(10 * time.Second),
				Route:   &sdk.Route{Method: "POST", Path: "/v1/audio/volume"}},
			{Type: "audio.mute.set", Description: "Mute, unmute or toggle",
				Schema: muteSchema, Keys: []sdk.KeyTemplate{resource}, Scope: "audio",
				Timeout: sdk.Duration(10 * time.Second),
				Route:   &sdk.Route{Method: "POST", Path: "/v1/audio/mute"}},
		},
		Events: []sdk.EventSpec{{Type: EventVolumeChanged, Description: "Volume or mute changed"}},
		Reads:  []sdk.ReadSpec{{Name: "audio", Description: "Volume, mute and the current output", Path: "/v1/audio"}},
	}
}

// Start begins following PipeWire. If it is not up yet (it starts with the
// user session), the module waits for it.
func (m *Module) Start(_ context.Context, core sdk.Core) error {
	m.mu.Lock()
	m.core = core
	m.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	m.stop = cancel
	m.done.Add(1)
	go func() { defer m.done.Done(); m.follow(ctx) }()
	return nil
}

func (m *Module) Stop(context.Context) error {
	if m.stop != nil {
		m.stop()
	}
	m.done.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settle != nil {
		m.settle.Stop()
	}
	return nil
}

// follow waits for PipeWire, then watches it, starting over if the
// monitor stops.
func (m *Module) follow(ctx context.Context) {
	warned := false
	for ctx.Err() == nil {
		cur, err := m.opts.Backend.Master(ctx)
		if err != nil {
			if !warned {
				m.log.Info("audio not available yet; audio actions wait for it", "reason", err)
				warned = true
			}
			m.setAvailable(false)
		} else {
			warned = false
			m.mu.Lock()
			m.available, m.last = true, cur
			m.mu.Unlock()
			err := m.opts.Backend.Watch(ctx, m.cue)
			if ctx.Err() != nil {
				return
			}
			m.log.Warn("audio monitor stopped", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-m.opts.Clock.After(m.opts.RetryEvery):
		}
	}
}

func (m *Module) setAvailable(ok bool) {
	m.mu.Lock()
	m.available = ok
	m.mu.Unlock()
}

// cue is called for every batch of changes; reading waits until they
// settle, so a burst becomes one read.
func (m *Module) cue() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settle == nil {
		m.settle = m.opts.Clock.AfterFunc(m.opts.Settle, m.check)
	} else {
		m.settle.Reset(m.opts.Settle)
	}
}

// check reads the state and reports a change nobody asked hostd for.
func (m *Module) check() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cur, err := m.opts.Backend.Master(ctx)
	if err != nil {
		return
	}
	m.mu.Lock()
	changed := cur.Percent != m.last.Percent || cur.Muted != m.last.Muted || cur.Sink != m.last.Sink
	m.last = cur
	core := m.core
	m.mu.Unlock()
	if changed && core != nil {
		core.Emit(sdk.Event{Type: EventVolumeChanged, Resource: resource, Data: mustJSON(cur),
			Source: &sdk.Source{Kind: sdk.SourceExternal, Name: "pipewire"}})
	}
}

func (m *Module) Validate(context.Context, sdk.Action) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.available {
		return sdk.Errorf(sdk.CodeModuleUnavailable, "audio is not available: PipeWire is not running")
	}
	return nil
}

// target works out the new volume: absolute, or relative to now, kept in
// 0-150.
func target(arg json.RawMessage, current int) (int, error) {
	var n int
	if json.Unmarshal(arg, &n) == nil {
		return n, nil
	}
	var s string
	if err := json.Unmarshal(arg, &s); err != nil {
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

func (m *Module) Handle(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	m.mu.Lock()
	want := m.last
	m.mu.Unlock()

	switch a.Type {
	case "audio.volume.set":
		var args struct {
			Percent json.RawMessage `json:"percent"`
		}
		if err := a.DecodeArgs(&args); err != nil {
			return sdk.Result{}, err
		}
		p, err := target(args.Percent, want.Percent)
		if err != nil {
			return sdk.Result{}, err
		}
		want.Percent = p
		m.expect(want)
		if err := m.opts.Backend.SetVolume(ctx, p); err != nil {
			return sdk.Result{}, err
		}
	case "audio.mute.set":
		var args struct {
			Muted json.RawMessage `json:"muted"`
		}
		if err := a.DecodeArgs(&args); err != nil {
			return sdk.Result{}, err
		}
		switch strings.TrimSpace(string(args.Muted)) {
		case "true":
			want.Muted = true
		case "false":
			want.Muted = false
		default: // "toggle"
			want.Muted = !want.Muted
		}
		m.expect(want)
		if err := m.opts.Backend.SetMute(ctx, want.Muted); err != nil {
			return sdk.Result{}, err
		}
	default:
		return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "audio module has no action %q", a.Type)
	}

	// Read back what the system really did.
	got, err := m.opts.Backend.Master(ctx)
	if err == nil {
		m.expect(got)
		want = got
	}
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	core.Emit(sdk.Event{Type: EventVolumeChanged, Action: a.ID, Resource: resource, Data: mustJSON(want)})
	return sdk.Result{Data: mustJSON(want)}, nil
}

// expect records the state hostd is setting, so the monitor's echo of the
// same change is not reported again as an outside change.
func (m *Module) expect(s Master) {
	m.mu.Lock()
	m.last = s
	m.mu.Unlock()
}

// Status is the answer of GET /v1/audio.
type Status struct {
	Available bool `json:"available"`
	Master
}

func (m *Module) Read(_ context.Context, name string, _ map[string]string) (any, error) {
	if name != "audio" {
		return nil, sdk.Errorf(sdk.CodeNotFound, "audio module has no read %q", name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return Status{Available: m.available, Master: m.last}, nil
}

func (m *Module) State(ctx context.Context) (any, error) { return m.Read(ctx, "audio", nil) }

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("audio: %v", err))
	}
	return b
}
