package display

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/davitizhgenti/hostd/sdk"
)

func TestDisplayFacetPlan(t *testing.T) {
	r := newDisplayRig(t, true)
	ctx := context.Background()
	plan := func(section string) []sdk.PlannedAction {
		t.Helper()
		out, err := r.m.Plan(ctx, "display", json.RawMessage(section))
		if err != nil {
			t.Fatalf("%s: %v", section, err)
		}
		return out
	}
	// The screens are on (the fake compositor has two, both on).
	if got := plan(`{"power":"on"}`); len(got) != 0 {
		t.Fatalf("on, and they are: %+v", got)
	}
	got := plan(`{"power":"off"}`)
	if len(got) != 1 || got[0].Step != "power" || got[0].Action != "display.power" || string(got[0].Args) != `{"on":false}` || got[0].Order != 70 {
		t.Fatalf("off: %+v", got)
	}
	if got := plan(`{}`); len(got) != 0 {
		t.Fatalf("an empty section: %+v", got)
	}
	// Without a compositor there are no screens to switch.
	if got, err := newDisplayRig(t, false).m.Plan(ctx, "display", json.RawMessage(`{"power":"off"}`)); err != nil || len(got) != 0 {
		t.Fatalf("no compositor: %+v %v", got, err)
	}

	man := r.m.Manifest()
	schema, err := sdk.CompileArgsSchema(man.Facets[0].Schema)
	if err != nil || man.Facets[0].Name != "display" {
		t.Fatalf("facet %+v %v", man.Facets, err)
	}
	for section, ok := range map[string]bool{`{"power":"on"}`: true, `{"power":"dim"}`: false, `{"powr":"on"}`: false} {
		if err := schema.Validate(json.RawMessage(section)); (err == nil) != ok {
			t.Errorf("%s: %v", section, err)
		}
	}
}
