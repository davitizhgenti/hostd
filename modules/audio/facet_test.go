package audio

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/davitizhgenti/hostd/sdk"
)

func TestAudioFacetPlan(t *testing.T) {
	r := newM3Rig(t, nil)
	ctx := context.Background()
	v, _ := r.m.Read(ctx, "audio", nil)
	cur := v.(Status)
	plan := func(section string) []sdk.PlannedAction {
		t.Helper()
		out, err := r.m.Plan(ctx, "audio", json.RawMessage(section))
		if err != nil {
			t.Fatalf("%s: %v", section, err)
		}
		return out
	}
	steps := func(acts []sdk.PlannedAction) string {
		var s []string
		for _, a := range acts {
			s = append(s, a.Step)
		}
		return strings.Join(s, " ")
	}

	// Already so: nothing to do.
	same := `{"output":"hdmi","volume":` + jsonInt(cur.Percent) + `,"muted":` + btoa(cur.Muted) + `}`
	if got := plan(same); len(got) != 0 {
		t.Fatalf("a reached state plans %+v", got)
	}
	// What differs, in the order that reaches it: output, volume, mute.
	got := plan(`{"output":"speakers","volume":` + jsonInt(cur.Percent+10) + `,"muted":` + btoa(!cur.Muted) + `}`)
	if steps(got) != "output volume mute" || got[0].Order >= got[1].Order || got[1].Order >= got[2].Order {
		t.Fatalf("plan = %+v", got)
	}
	if got[0].Action != "audio.output.set" || string(got[0].Args) != `{"output":"speakers"}` ||
		got[1].Action != "audio.volume.set" || string(got[1].Args) != `{"percent":`+jsonInt(cur.Percent+10)+`}` ||
		got[2].Action != "audio.mute.set" || string(got[2].Args) != `{"muted":`+btoa(!cur.Muted)+`}` {
		t.Fatalf("plan = %+v", got)
	}
	if got := plan(`{}`); len(got) != 0 {
		t.Fatalf("an empty section plans %+v", got)
	}

	// The facet and what its schema refuses.
	man := r.m.Manifest()
	if len(man.Facets) != 1 || man.Facets[0].Name != "audio" {
		t.Fatalf("facets = %+v", man.Facets)
	}
	schema, err := sdk.CompileArgsSchema(man.Facets[0].Schema)
	if err != nil {
		t.Fatal(err)
	}
	for section, ok := range map[string]bool{`{"volume":40}`: true, `{"volume":400}`: false, `{"volume":"loud"}`: false,
		`{"vol":4}`: false, `{"muted":true,"output":"hdmi"}`: true} {
		if err := schema.Validate(json.RawMessage(section)); (err == nil) != ok {
			t.Errorf("%s: %v", section, err)
		}
	}
	if !reflect.DeepEqual(man.Facets[0].Name, "audio") {
		t.Fatal("unreachable")
	}
}

func jsonInt(n int) string { b, _ := json.Marshal(n); return string(b) }
func btoa(v bool) string   { b, _ := json.Marshal(v); return string(b) }
