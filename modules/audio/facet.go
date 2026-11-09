package audio

import (
	"context"
	"encoding/json"

	"github.com/davitizhgenti/hostd/sdk"
)

// The audio facet: the [audio] section of a scene.
//
//	[audio]
//	output = "hdmi"    # hdmi, speakers, headphones, bt-<device>, usb-<device>
//	volume = 40
//	muted  = false

type sceneAudio struct {
	Output string `json:"output,omitempty" desc:"where sound goes: an output's name (hostctl get audio)"`
	Volume *int   `json:"volume,omitempty" desc:"0-150" min:"0" max:"150"`
	Muted  *bool  `json:"muted,omitempty"`
}

func facets() []sdk.FacetSpec {
	return []sdk.FacetSpec{{Name: "audio", Description: "Where sound goes and how loud", Schema: sdk.SchemaOf[sceneAudio]()}}
}

// Plan implements sdk.Planner: what differs from the sound as it is now.
func (m *Module) Plan(ctx context.Context, _ string, desired json.RawMessage) ([]sdk.PlannedAction, error) {
	var want sceneAudio
	if err := json.Unmarshal(desired, &want); err != nil {
		return nil, err
	}
	v, err := m.Read(ctx, "audio", nil)
	if err != nil {
		return nil, err
	}
	cur := v.(Status)
	var out []sdk.PlannedAction
	if want.Output != "" {
		def := ""
		for _, o := range cur.Outputs {
			if o.Default {
				def = o.Name
			}
		}
		if def != want.Output {
			out = append(out, sdk.PlannedAction{Step: "output", Action: "audio.output.set",
				Args: sdk.MustJSON(map[string]string{"output": want.Output}), Order: 20})
		}
	}
	if want.Volume != nil && cur.Percent != *want.Volume {
		out = append(out, sdk.PlannedAction{Step: "volume", Action: "audio.volume.set",
			Args: sdk.MustJSON(map[string]int{"percent": *want.Volume}), Order: 30})
	}
	if want.Muted != nil && cur.Muted != *want.Muted {
		out = append(out, sdk.PlannedAction{Step: "mute", Action: "audio.mute.set",
			Args: sdk.MustJSON(map[string]bool{"muted": *want.Muted}), Order: 40})
	}
	return out, nil
}
