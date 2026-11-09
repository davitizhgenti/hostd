package display

import (
	"context"
	"encoding/json"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/sdk"
)

// The display facet: the [display] section of a scene.
//
//	[display]
//	power = "on"    # or "off": the screens

type sceneDisplay struct {
	Power string `json:"power,omitempty" enum:"on,off" desc:"the screens: on or off (power saving)"`
}

func facets() []sdk.FacetSpec {
	return []sdk.FacetSpec{{Name: "display", Description: "Whether the screens are on", Schema: sdk.SchemaOf[sceneDisplay]()}}
}

// Plan implements sdk.Planner: switch the screens if any active one is not
// as wanted.
func (m *Module) Plan(ctx context.Context, _ string, desired json.RawMessage) ([]sdk.PlannedAction, error) {
	var want sceneDisplay
	if err := json.Unmarshal(desired, &want); err != nil {
		return nil, err
	}
	if want.Power == "" {
		return nil, nil
	}
	v, err := m.Read(ctx, contract.ReadDisplay, nil)
	if err != nil {
		return nil, err
	}
	on := want.Power == "on"
	if outs, _ := v.(map[string]any)["outputs"].([]Output); outs != nil {
		for _, o := range outs {
			if o.Active && o.Power != on {
				return []sdk.PlannedAction{{Step: "power", Action: contract.ActionDisplayPower,
					Args: sdk.MustJSON(map[string]bool{"on": on}), Order: 70}}, nil
			}
		}
	}
	return nil, nil
}
