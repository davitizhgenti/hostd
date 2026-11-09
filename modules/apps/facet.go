package apps

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/sdk"
)

// The apps facet: the [apps] section of a scene.
//
//	[apps]
//	stop    = ["game"]        # every instance of these is stopped
//	running = ["jellyfin"]    # started if not running
//	focus   = "jellyfin"      # in front (started if needed)

type sceneApps struct {
	Stop    []string `json:"stop,omitempty" desc:"apps to stop, every instance"`
	Running []string `json:"running,omitempty" desc:"apps to have running, started if they are not"`
	Focus   string   `json:"focus,omitempty" desc:"the app in front"`
}

func facets() []sdk.FacetSpec {
	return []sdk.FacetSpec{{Name: "apps", Description: "Which apps run, and the one in front", Schema: sdk.SchemaOf[sceneApps]()}}
}

// CheckFacet implements sdk.FacetChecker: what a schema cannot say.
func (m *Module) CheckFacet(_ string, desired json.RawMessage) error {
	var a sceneApps
	if err := json.Unmarshal(desired, &a); err != nil {
		return err
	}
	for _, id := range a.Running {
		if slices.Contains(a.Stop, id) {
			return fmt.Errorf("%q is both in stop and in running", id)
		}
	}
	if a.Focus != "" && slices.Contains(a.Stop, a.Focus) {
		return fmt.Errorf("focus %q is in stop", a.Focus)
	}
	return nil
}

// Plan implements sdk.Planner: stop what should not run, start what should,
// and bring the app to the front, each only if it is not so already.
func (m *Module) Plan(ctx context.Context, _ string, desired json.RawMessage) ([]sdk.PlannedAction, error) {
	var want sceneApps
	if err := json.Unmarshal(desired, &want); err != nil {
		return nil, err
	}
	m.mu.Lock()
	live := func(app string) []string {
		var ids []string
		for _, in := range m.live(app) {
			ids = append(ids, in.ID)
		}
		return ids
	}
	var out []sdk.PlannedAction
	for _, app := range want.Stop {
		for _, id := range live(app) {
			out = append(out, sdk.PlannedAction{Step: "stop", Action: contract.ActionInstanceStop,
				Args: sdk.MustJSON(map[string]string{"id": id}), Order: 10})
		}
	}
	need := append([]string(nil), want.Running...)
	if want.Focus != "" && !slices.Contains(need, want.Focus) {
		need = append(need, want.Focus) // in front means running
	}
	for _, app := range need {
		if len(live(app)) == 0 {
			out = append(out, sdk.PlannedAction{Step: "start", Action: contract.ActionAppOpen,
				Args: sdk.MustJSON(map[string]string{"id": app}), Order: 50})
		}
	}
	focusIDs := []string(nil)
	if want.Focus != "" {
		focusIDs = live(want.Focus)
	}
	m.mu.Unlock()

	if want.Focus != "" {
		st, err := contract.ReadDisplayState(ctx, m.core)
		if err != nil {
			return out, fmt.Errorf("focus: %w", err)
		}
		// An app started just now is not in the list: app.open brings it to
		// the front itself (unless someone uses the screen).
		if contract.AppOf(st.Focused) != want.Focus && len(focusIDs) > 0 {
			out = append(out, sdk.PlannedAction{Step: "focus", Action: contract.ActionInstanceSwitch,
				Args: sdk.MustJSON(map[string]string{"id": focusIDs[0]}), Order: 60})
		}
	}
	return out, nil
}
