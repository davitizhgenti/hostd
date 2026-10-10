// Outputs and per-app audio (M3): which output sound goes to, and each
// app's own volume and mute.
package audio

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/davitizhgenti/hostd/contract"
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

func (w *WirePlumber) Graph(ctx context.Context) (Graph, error) {
	cmd := exec.CommandContext(ctx, "pw-dump", "--no-colors")
	cmd.Env = append(cmd.Environ(), w.Env...)
	out, err := cmd.Output()
	if err != nil {
		return Graph{}, sdk.Errorf(sdk.CodeModuleUnavailable, "pw-dump: %v", err)
	}
	return parseGraph(out)
}

func (w *WirePlumber) SetDefault(ctx context.Context, id int) error {
	_, err := w.wpctl(ctx, "set-default", fmt.Sprint(id))
	return err
}

func (w *WirePlumber) MoveStream(ctx context.Context, stream int, node string) error {
	cmd := exec.CommandContext(ctx, "pw-metadata", "-n", "default", fmt.Sprint(stream), "target.object", node)
	cmd.Env = append(cmd.Environ(), w.Env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return sdk.Errorf(sdk.CodeModuleUnavailable, "pw-metadata: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
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
	if core == nil {
		return nil
	}
	raw, err := core.Read(ctx, contract.AppsModule, contract.ReadInstances, nil)
	if err != nil {
		return nil
	}
	var all []contract.Instance
	if json.Unmarshal(raw, &all) != nil {
		return nil
	}
	return slices.DeleteFunc(all, func(in contract.Instance) bool { return in.Ended() })
}

// instanceOf finds the instance a stream's process belongs to: by its
// unit, or by an instance's env rule (Flatpak apps and Steam games run
// outside hostd's units). When several match (a game's process has
// Steam's FLATPAK_ID too), the instance with more match rules wins.
func (m *Module) instanceOf(pid int, live []contract.Instance) string {
	if pid <= 0 {
		return ""
	}
	if id := contract.InstanceOfPID(m.opts.ProcRoot, pid); id != "" {
		return id
	}
	best, top := "", 0
	for _, in := range live {
		if in.Match == nil || in.Match.Env == "" || !contract.ProcessHasEnv(m.opts.ProcRoot, pid, in.Match.Env) {
			continue
		}
		n := 0
		for _, r := range []string{in.Match.Class, in.Match.AppID, in.Match.Title, in.Match.Env} {
			if r != "" {
				n++
			}
		}
		if n > top {
			best, top = in.ID, n
		}
	}
	if best != "" {
		return best
	}
	for _, in := range live { // an app that moved itself out of its unit
		if contract.ProcessDescends(m.opts.ProcRoot, pid, in.PID) {
			return in.ID
		}
	}
	return ""
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

// handleOutput sends sound to an output: the default from now on, and the
// streams already playing move there too.
func (m *Module) handleOutput(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	var args struct {
		Output string `json:"output"`
	}
	if err := a.DecodeArgs(&args); err != nil {
		return sdk.Result{}, err
	}
	g, _, err := m.graph(ctx)
	if err != nil {
		return sdk.Result{}, err
	}
	out, ok := g.Output(args.Output)
	if !ok {
		names := make([]string, 0, len(g.Outputs))
		for _, o := range g.Outputs {
			names = append(names, o.Name)
		}
		return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "no output %q; there are: %s", args.Output, strings.Join(names, ", "))
	}
	gb := m.opts.Backend.(GraphBackend)
	if err := gb.SetDefault(ctx, out.ID); err != nil {
		return sdk.Result{}, err
	}
	for _, s := range g.Streams {
		if err := gb.MoveStream(ctx, s.ID, out.Node); err != nil {
			m.log.Warn("moving a stream", "stream", s.ID, "err", err)
		}
	}
	m.followGraph(ctx)
	m.check()
	return sdk.Result{Data: sdk.MustJSON(map[string]any{"output": out.Name, "moved": len(g.Streams)})}, nil
}

// handleApp sets an app's volume or mute, on every stream it plays.
func (m *Module) handleApp(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	var args struct {
		Instance string          `json:"instance"`
		Percent  json.RawMessage `json:"percent"`
		Muted    json.RawMessage `json:"muted"`
	}
	if err := a.DecodeArgs(&args); err != nil {
		return sdk.Result{}, err
	}
	g, _, err := m.graph(ctx)
	if err != nil {
		return sdk.Result{}, err
	}
	var streams []Stream
	for _, s := range g.Streams {
		if s.Instance == args.Instance {
			streams = append(streams, s)
		}
	}
	if len(streams) == 0 {
		return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "%s plays no sound", args.Instance)
	}
	gb := m.opts.Backend.(GraphBackend)
	state := map[string]any{"instance": args.Instance}
	if a.Type == "audio.app.volume.set" {
		p, err := target(args.Percent, streams[0].Percent)
		if err != nil {
			return sdk.Result{}, err
		}
		for _, s := range streams {
			if err := gb.SetNodeVolume(ctx, s.ID, p); err != nil {
				return sdk.Result{}, err
			}
		}
		state["percent"] = p
	} else {
		muted := !streams[0].Muted // toggle
		switch strings.TrimSpace(string(args.Muted)) {
		case "true":
			muted = true
		case "false":
			muted = false
		}
		for _, s := range streams {
			if err := gb.SetNodeMute(ctx, s.ID, muted); err != nil {
				return sdk.Result{}, err
			}
		}
		state["muted"] = muted
	}
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	if core != nil {
		core.Emit(sdk.Event{Type: EventAppVolumeChanged, Action: a.ID, Data: sdk.MustJSON(state)})
	}
	return sdk.Result{Data: sdk.MustJSON(state)}, nil
}

var (
	outputSchema = json.RawMessage(`{"type":"object","properties":{
		"output":{"type":"string","description":"an output's name, as GET /v1/audio lists them (hdmi, speakers, bt-...)"}},
		"required":["output"]}`)
	appVolumeSchema = json.RawMessage(`{"type":"object","properties":{
		"instance":{"type":"string"},
		"percent":{"description":"0-150, or a change such as \"+5\" or \"-5\"",
			"oneOf":[{"type":"integer","minimum":0,"maximum":150},{"type":"string","pattern":"^([+-][0-9]{1,3}|[0-9]{1,3})$"}]}},
		"required":["instance","percent"]}`)
	appMuteSchema = json.RawMessage(`{"type":"object","properties":{
		"instance":{"type":"string"},
		"muted":{"description":"true, false or \"toggle\"","oneOf":[{"type":"boolean"},{"const":"toggle"}]}},
		"required":["instance","muted"]}`)
)

// m3Actions are the output and per-app actions.
func m3Actions() []sdk.ActionSpec {
	return []sdk.ActionSpec{
		{Type: "audio.output.set", Description: "Send sound to an output; what plays moves there too",
			Schema: outputSchema, Keys: []sdk.KeyTemplate{"audio.output"}, Scope: "audio",
			Timeout: sdk.Duration(15 * time.Second),
			Route:   &sdk.Route{Method: "POST", Path: "/v1/audio/output"}},
		{Type: "audio.app.volume.set", Description: "Set an app's own volume (0-150) or change it",
			Schema: appVolumeSchema, Keys: []sdk.KeyTemplate{"audio.app:{instance}"}, Scope: "audio",
			Timeout: sdk.Duration(10 * time.Second),
			Route:   &sdk.Route{Method: "POST", Path: "/v1/audio/apps/{instance}/volume"}},
		{Type: "audio.app.mute.set", Description: "Mute, unmute or toggle an app",
			Schema: appMuteSchema, Keys: []sdk.KeyTemplate{"audio.app:{instance}"}, Scope: "audio",
			Timeout: sdk.Duration(10 * time.Second),
			Route:   &sdk.Route{Method: "POST", Path: "/v1/audio/apps/{instance}/mute"}},
	}
}
