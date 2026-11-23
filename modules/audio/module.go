// Package audio is the audio module: master volume and mute of the default
// output, kept in step with changes made elsewhere (a game's own slider, a
// headset connecting), the outputs (with stable names) and each app's
// own volume and mute.
package audio

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/internal/clock"
	"github.com/davitizhgenti/hostd/sdk"
	"github.com/davitizhgenti/hostd/sdk/kit"
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
	ProcRoot   string        // default /proc: finds which app a stream belongs to
	Media      Media         // media players (nil: no media keys)
	RetryEvery time.Duration // between attempts to reach PipeWire (default 2s)
	Settle     time.Duration // wait after a change cue before reading (default 150ms)
}

// Module is the audio module.
type Module struct {
	opts Options
	log  *slog.Logger

	actions *kit.Actions

	mu        sync.Mutex
	core      sdk.Core
	available bool
	last      Master
	settle    clock.Timer
	outputs   Graph        // outputs and streams, after the last change
	applied   map[int]bool // streams that got their app's own volume
	media     []Player     // media players, after the last change

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
	if opts.ProcRoot == "" {
		opts.ProcRoot = "/proc"
	}
	if opts.Settle == 0 {
		opts.Settle = 150 * time.Millisecond
	}
	m := &Module{opts: opts, log: opts.Logger}
	m.actions = m.declare()
	return m
}

func (m *Module) Manifest() sdk.Manifest {
	return sdk.Manifest{
		Name: "audio", Version: "0.1.0",
		// Sound is told apart by instance; media keys go to the app in front.
		Uses:    []string{contract.AppsModule, contract.DisplayModule},
		Owns:    []string{"audio.*", "media.*"},
		Facets:  facets(),
		Scopes:  []sdk.ScopeSpec{{Name: "audio", Description: "Volume, mute, outputs, per-app audio, media keys"}},
		Actions: m.actions.Specs(),
		Events: []sdk.EventSpec{
			{Type: EventVolumeChanged, Description: "Volume or mute changed"},
			{Type: EventOutputsChanged, Description: "An output came or went, or the default output changed"},
			{Type: EventAppVolumeChanged, Description: "An app's own volume or mute changed"},
			{Type: EventMediaChanged, Description: "A media player started, stopped, paused or changed track"},
		},
		Reads: []sdk.ReadSpec{
			{Name: "audio", Description: "Volume, mute, the outputs and the sound each app plays (a table of the outputs)", Path: "/v1/audio",
				CLI: &sdk.CLI{Rows: "outputs", Empty: "no audio outputs", Columns: outputColumns}},
			{Name: "streams", Description: "The sound each app plays", Path: "/v1/audio/streams",
				CLI: &sdk.CLI{Empty: "no app is playing sound", Columns: []sdk.Column{{Title: "STREAM", Path: "id"}, {Title: "APP", Path: "app"},
					{Title: "INSTANCE", Path: "instance"}, {Title: "OUTPUT", Path: "output"}, {Title: "VOLUME", Path: "percent"}, {Title: "MUTED", Path: "muted"}}}},
			{Name: "media", Description: "Media players: their app, status and track", Path: "/v1/media",
				CLI: &sdk.CLI{Empty: "no media players", Columns: []sdk.Column{{Title: "PLAYER", Path: "name"}, {Title: "STATUS", Path: "status"},
					{Title: "INSTANCE", Path: "instance"}, {Title: "TITLE", Path: "title", Max: 40}, {Title: "ARTIST", Path: "artist", Max: 30}}}},
		},
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
	m.done.Add(2)
	go func() { defer m.done.Done(); m.follow(ctx) }()
	go func() { defer m.done.Done(); m.followMedia(ctx) }()
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
			m.followGraph(ctx)
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
	m.followGraph(ctx)
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
		core.Emit(sdk.Event{Type: EventVolumeChanged, Resource: resource, Data: sdk.MustJSON(cur),
			Source: &sdk.Source{Kind: sdk.SourceExternal, Name: "pipewire"}})
	}
}

func (m *Module) Validate(_ context.Context, a sdk.Action) error {
	if strings.HasPrefix(a.Type, "media.") {
		return nil // players, not PipeWire
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.available {
		return sdk.Errorf(sdk.CodeModuleUnavailable, "audio is not available: PipeWire is not running")
	}
	return nil
}

func (m *Module) Handle(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	return m.actions.Handle(ctx, a)
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
	Outputs []Output `json:"outputs"`
	Streams []Stream `json:"streams"`
}

var outputColumns = []sdk.Column{{Title: "OUTPUT", Path: "name"}, {Title: "DEFAULT", Path: "default"},
	{Title: "PORT", Path: "port"}, {Title: "VOLUME", Path: "percent"}, {Title: "MUTED", Path: "muted"}, {Title: "DEVICE", Path: "description", Max: 50}}

func (m *Module) Read(_ context.Context, name string, _ map[string]string) (any, error) {
	if name == "streams" {
		m.mu.Lock()
		defer m.mu.Unlock()
		return append([]Stream{}, m.outputs.Streams...), nil
	}
	if name == "media" {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.media == nil {
			return []Player{}, nil
		}
		return append([]Player(nil), m.media...), nil
	}
	if name != "audio" {
		return nil, sdk.Errorf(sdk.CodeNotFound, "audio module has no read %q", name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	outs, streams := m.outputs.Outputs, m.outputs.Streams
	if outs == nil {
		outs = []Output{}
	}
	if streams == nil {
		streams = []Stream{}
	}
	return Status{Available: m.available, Master: m.last, Outputs: outs, Streams: streams}, nil
}

func (m *Module) State(ctx context.Context) (any, error) { return m.Read(ctx, "audio", nil) }
