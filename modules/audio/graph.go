// The audio graph as hostd sees it: outputs with stable names, and the
// streams apps are playing, read from one pw-dump.
package audio

import (
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Output is a place sound can go: the TV's HDMI, speakers, a headset.
type Output struct {
	// Name is stable: the same device gets the same name across
	// reconnects and reboots (hdmi, speakers, headphones, bt-<device>,
	// usb-<device>), derived from the device, so nothing is stored.
	Name        string `json:"name"`
	Description string `json:"description"`
	Node        string `json:"node"` // node.name
	ID          int    `json:"-"`    // PipeWire object ID: changes on reconnect
	Default     bool   `json:"default"`
	Percent     int    `json:"percent"`
	Muted       bool   `json:"muted"`
}

// Stream is sound an app is playing.
type Stream struct {
	ID       int    `json:"id"`
	PID      int    `json:"pid,omitempty"` // application.process.id
	App      string `json:"app,omitempty"` // application.name
	Instance string `json:"instance,omitempty"`
	Percent  int    `json:"percent"`
	Muted    bool   `json:"muted"`
}

// Graph is the outputs and streams at one moment.
type Graph struct {
	Outputs []Output `json:"outputs"`
	Streams []Stream `json:"streams"`
}

// Output returns the output with a stable name.
func (g Graph) Output(name string) (Output, bool) {
	for _, o := range g.Outputs {
		if o.Name == name {
			return o, true
		}
	}
	return Output{}, false
}

type dumpObject struct {
	ID   int    `json:"id"`
	Type string `json:"type"`
	Info *struct {
		Props  map[string]any `json:"props"`
		Params struct {
			Props []struct {
				ChannelVolumes []float64 `json:"channelVolumes"`
				Mute           bool      `json:"mute"`
			} `json:"Props"`
		} `json:"params"`
	} `json:"info"`
	Metadata []struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	} `json:"metadata"`
}

// parseGraph reads pw-dump's JSON.
func parseGraph(data []byte) (Graph, error) {
	var objs []dumpObject
	if err := json.Unmarshal(data, &objs); err != nil {
		return Graph{}, err
	}
	devices := map[int]map[string]any{}
	clients := map[int]map[string]any{}
	defaultSink := ""
	for _, o := range objs {
		switch {
		case strings.HasSuffix(o.Type, ":Device") && o.Info != nil:
			devices[o.ID] = o.Info.Props
		case strings.HasSuffix(o.Type, ":Client") && o.Info != nil:
			clients[o.ID] = o.Info.Props
		case strings.HasSuffix(o.Type, ":Metadata"):
			for _, m := range o.Metadata {
				if m.Key == "default.audio.sink" {
					var v struct{ Name string }
					if json.Unmarshal(m.Value, &v) == nil {
						defaultSink = v.Name
					}
				}
			}
		}
	}
	var g Graph
	for _, o := range objs {
		if !strings.HasSuffix(o.Type, ":Node") || o.Info == nil {
			continue
		}
		p := o.Info.Props
		percent, muted := 0, false
		if ps := o.Info.Params.Props; len(ps) > 0 {
			percent, muted = cubicPercent(ps[0].ChannelVolumes), ps[0].Mute
		}
		switch str(p, "media.class") {
		case "Audio/Sink":
			dev := devices[num(p, "device.id")]
			g.Outputs = append(g.Outputs, Output{
				Name: outputName(p, dev), Description: str(p, "node.description"), Node: str(p, "node.name"),
				ID: o.ID, Default: str(p, "node.name") == defaultSink, Percent: percent, Muted: muted,
			})
		case "Stream/Output/Audio":
			// pipewire.sec.pid is what PipeWire saw of the client's socket:
			// the real process. application.process.id is what the app
			// says, which inside a sandbox (Steam's) is its own namespace's.
			pid := streamPID(p, clients[num(p, "client.id")])
			g.Streams = append(g.Streams, Stream{ID: o.ID, PID: pid,
				App: str(p, "application.name"), Percent: percent, Muted: muted})
		}
	}
	uniqueNames(g.Outputs)
	// pw-dump lists objects in no set order; a graph that is the same
	// should read the same.
	sort.SliceStable(g.Streams, func(i, j int) bool { return g.Streams[i].ID < g.Streams[j].ID })
	return g, nil
}

// streamPID is the process playing a stream. pipewire.sec.pid is what
// PipeWire saw of the client's socket: the real process.
// application.process.id is what the app says, which inside a sandbox
// (Steam's) is its own namespace's. Streams that come through the
// PulseAudio layer (browsers) carry them on the node; native PipeWire
// clients (pw-cat, SDL games) only on their client object.
func streamPID(node, client map[string]any) int {
	for _, props := range []map[string]any{node, client} {
		if pid := num(props, "pipewire.sec.pid"); pid != 0 {
			return pid
		}
		if pid := num(props, "application.process.id"); pid != 0 {
			return pid
		}
	}
	return 0
}

// cubicPercent turns PipeWire's channel volumes (cubic) into the percent
// wpctl shows.
func cubicPercent(vols []float64) int {
	if len(vols) == 0 {
		return 0
	}
	var sum float64
	for _, v := range vols {
		sum += v
	}
	return int(math.Round(math.Cbrt(sum/float64(len(vols))) * 100))
}

var reSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	return strings.Trim(reSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// outputName derives an output's stable name from its node and device.
func outputName(node, dev map[string]any) string {
	desc := str(dev, "device.description")
	if desc == "" {
		desc = str(node, "node.description")
	}
	profile := strings.ToLower(str(node, "device.profile.name") + " " + str(node, "node.name"))
	switch {
	case str(node, "device.api") == "bluez5" || str(dev, "device.api") == "bluez5":
		return "bt-" + slug(desc)
	case strings.Contains(profile, "hdmi") || strings.Contains(profile, "displayport"):
		return "hdmi"
	case str(dev, "device.bus") == "usb":
		return "usb-" + slug(desc)
	case str(dev, "device.form-factor") == "headphone" || str(dev, "device.form-factor") == "headset" ||
		strings.Contains(profile, "headphone"):
		return "headphones"
	case strings.Contains(profile, "analog"):
		return "speakers"
	}
	if s := slug(desc); s != "" {
		return s
	}
	return "output"
}

// uniqueNames numbers outputs that would share a name (two HDMI ports),
// in the order of their node names, which do not change.
func uniqueNames(outs []Output) {
	sort.SliceStable(outs, func(i, j int) bool { return outs[i].Node < outs[j].Node })
	count := map[string]int{}
	for _, o := range outs {
		count[o.Name]++
	}
	seen := map[string]int{}
	for i := range outs {
		n := outs[i].Name
		if count[n] > 1 {
			seen[n]++
			outs[i].Name = n + "-" + strconv.Itoa(seen[n])
		}
	}
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func num(m map[string]any, k string) int {
	switch v := m[k].(type) {
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}
