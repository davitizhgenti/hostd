// Outputs and per-app audio (M3): which output sound goes to, and each
// app's own volume and mute.
package audio

import (
	"context"
	"fmt"
	"strings"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/internal/proc"
	"github.com/davitizhgenti/hostd/sdk"
)

// Events the module emits for outputs and apps.
const (
	EventOutputsChanged   = "audio.outputs.changed"
	EventAppVolumeChanged = "audio.app.changed"
)

// GraphBackend is a Backend that also sees outputs and streams.
type GraphBackend interface {
	Graph(ctx context.Context) (Graph, error)
	SetDefault(ctx context.Context, id int) error
	// MoveStream sends a stream to an output (its node.name).
	MoveStream(ctx context.Context, stream int, node string) error
	SetNodeVolume(ctx context.Context, id, percent int) error
	SetNodeMute(ctx context.Context, id int, muted bool) error
}

// Graph answers from what the monitor has kept when it is running, and
// asks pw-dump otherwise (before its first dump, or while it restarts).
func (w *WirePlumber) Graph(ctx context.Context) (Graph, error) {
	if g, ok := w.graph.read(); ok {
		return g, nil
	}
	out, err := w.run(ctx, "pw-dump", "--no-colors")
	if err != nil {
		return Graph{}, err
	}
	return parseGraph(out)
}

func (w *WirePlumber) SetDefault(ctx context.Context, id int) error {
	_, err := w.wpctl(ctx, "set-default", fmt.Sprint(id))
	return err
}

func (w *WirePlumber) MoveStream(ctx context.Context, stream int, node string) error {
	_, err := w.run(ctx, "pw-metadata", "-n", "default", fmt.Sprint(stream), "target.object", node)
	return err
}

func (w *WirePlumber) SetNodeVolume(ctx context.Context, id, percent int) error {
	_, err := w.wpctl(ctx, "set-volume", fmt.Sprint(id), fmt.Sprintf("%.2f", float64(percent)/100))
	return err
}

func (w *WirePlumber) SetNodeMute(ctx context.Context, id int, muted bool) error {
	v := "0"
	if muted {
		v = "1"
	}
	_, err := w.wpctl(ctx, "set-mute", fmt.Sprint(id), v)
	return err
}

// graph reads the outputs and streams, with each stream's instance.
func (m *Module) graph(ctx context.Context) (Graph, map[string]contract.Instance, error) {
	gb, ok := m.opts.Backend.(GraphBackend)
	if !ok {
		return Graph{}, nil, sdk.Errorf(sdk.CodeModuleUnavailable, "this audio backend has no outputs or app volumes")
	}
	g, err := gb.Graph(ctx)
	if err != nil {
		return Graph{}, nil, err
	}
	live := m.liveInstances(ctx)
	for i := range g.Streams {
		g.Streams[i].Instance = m.instanceOf(g.Streams[i].PID, live)
	}
	byID := map[string]contract.Instance{}
	for _, in := range live {
		byID[in.ID] = in
	}
	return g, byID, nil
}

// liveInstances asks the apps module what runs (nothing without it).
func (m *Module) liveInstances(ctx context.Context) []contract.Instance {
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	all, _ := contract.Instances.Get(ctx, core, nil)
	return contract.Live(all)
}

// instanceOf finds the instance a stream's process belongs to (see
// proc.Resolve): by its unit, or by an instance's rules (Flatpak apps and
// Steam games run outside hostd's units).
func (m *Module) instanceOf(pid int, live []contract.Instance) string {
	return proc.Resolve(m.opts.ProcRoot, proc.Subject{PID: pid}, live)
}

// followGraph notes the outputs and streams after a change: it announces
// a changed set of outputs or default, and gives an app's new sound the
// app's own volume.
func (m *Module) followGraph(ctx context.Context) {
	g, instances, err := m.graph(ctx)
	if err != nil {
		return
	}
	m.mu.Lock()
	before := outputsKey(m.outputs.Outputs)
	m.outputs = g
	if m.applied == nil {
		m.applied = map[int]bool{}
	}
	var apply []Stream
	present := map[int]bool{}
	for _, s := range g.Streams {
		present[s.ID] = true
		if m.applied[s.ID] || s.Instance == "" {
			continue
		}
		m.applied[s.ID] = true
		if in, ok := instances[s.Instance]; ok && in.Volume != nil && *in.Volume != s.Percent {
			s.Percent = *in.Volume
			apply = append(apply, s)
		}
	}
	for id := range m.applied {
		if !present[id] {
			delete(m.applied, id)
		}
	}
	core := m.core
	changed := before != outputsKey(g.Outputs)
	m.mu.Unlock()
	gb := m.opts.Backend.(GraphBackend)
	for _, s := range apply {
		if err := gb.SetNodeVolume(ctx, s.ID, s.Percent); err != nil {
			m.log.Warn("setting an app's volume", "instance", s.Instance, "err", err)
		}
	}
	if changed && core != nil {
		core.Emit(sdk.Event{Type: EventOutputsChanged, Data: sdk.MustJSON(g.Outputs),
			Source: &sdk.Source{Kind: sdk.SourceExternal, Name: "pipewire"}})
	}
}

// outputsKey is what makes the outputs "changed": which exist, and which
// is the default.
func outputsKey(outs []Output) string {
	var b strings.Builder
	for _, o := range outs {
		b.WriteString(o.Name)
		if o.Default {
			b.WriteString("*")
		}
		b.WriteString(" ")
	}
	return b.String()
}

// setOutput sends sound to an output: the default from now on, and the
// streams already playing move there too.
func (m *Module) setOutput(ctx context.Context, _ sdk.Action, args outputArgs) (outputSet, error) {
	g, _, err := m.graph(ctx)
	if err != nil {
		return outputSet{}, err
	}
	out, ok := g.Output(args.Output)
	if !ok {
		names := make([]string, 0, len(g.Outputs))
		for _, o := range g.Outputs {
			names = append(names, o.Name)
		}
		return outputSet{}, sdk.Errorf(sdk.CodeNotFound, "no output %q; there are: %s", args.Output, strings.Join(names, ", "))
	}
	gb := m.opts.Backend.(GraphBackend)
	if err := gb.SetDefault(ctx, out.ID); err != nil {
		return outputSet{}, err
	}
	for _, s := range g.Streams {
		if err := gb.MoveStream(ctx, s.ID, out.Node); err != nil {
			m.log.Warn("moving a stream", "stream", s.ID, "err", err)
		}
	}
	m.followGraph(ctx)
	m.check()
	return outputSet{Output: out.Name, Moved: len(g.Streams)}, nil
}

// setApp sets an app's volume or mute (apply), on every stream it plays.
func (m *Module) setApp(ctx context.Context, a sdk.Action, instance string, apply func(GraphBackend, []Stream, *appState) error) (appState, error) {
	g, _, err := m.graph(ctx)
	if err != nil {
		return appState{}, err
	}
	var streams []Stream
	for _, s := range g.Streams {
		if s.Instance == instance {
			streams = append(streams, s)
		}
	}
	if len(streams) == 0 {
		return appState{}, sdk.Errorf(sdk.CodeNotFound, "%s plays no sound", instance)
	}
	state := appState{Instance: instance}
	if err := apply(m.opts.Backend.(GraphBackend), streams, &state); err != nil {
		return appState{}, err
	}
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	if core != nil {
		core.Emit(sdk.Event{Type: EventAppVolumeChanged, Action: a.ID, Data: sdk.MustJSON(state)})
	}
	return state, nil
}
