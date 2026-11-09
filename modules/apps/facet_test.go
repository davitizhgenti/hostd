package apps

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/modules/apps/runner/podman/podmantest"
	"github.com/davitizhgenti/hostd/modules/apps/runner/units/unitstest"
	"github.com/davitizhgenti/hostd/sdk"
)

func TestAppsFacetPlan(t *testing.T) {
	w := &windows{}
	r := newRigWith(t, unitstest.NewFakeSystemd(), podmantest.NewFakeEngine(), unitstest.FakeSession(t), nil, rigApps, w.module())
	r.start(t, "term")
	r.start(t, "jelly")
	ctx := context.Background()
	plan := func(section string) []sdk.PlannedAction {
		t.Helper()
		out, err := r.m.Plan(ctx, "apps", json.RawMessage(section))
		if err != nil {
			t.Fatalf("%s: %v", section, err)
		}
		return out
	}
	summary := func(acts []sdk.PlannedAction) string {
		var s []string
		for _, a := range acts {
			s = append(s, a.Step+":"+string(a.Args))
		}
		return strings.Join(s, " ")
	}

	// Reached already: nothing. (Focus on term while term is in front.)
	w.focused = "term"
	if got := plan(`{"running":["term","jelly"],"focus":"term"}`); len(got) != 0 {
		t.Fatalf("a reached state plans %s", summary(got))
	}
	// Stop every instance of an app that runs; start one that does not; and
	// the order: stop (10) before start (50) before focus (60).
	got := plan(`{"stop":["jelly"],"running":["broken"],"focus":"term"}`)
	if summary(got) != `stop:{"id":"jelly"} start:{"id":"broken"}` || got[0].Order >= got[1].Order {
		t.Fatalf("plan = %s", summary(got))
	}
	// An app to the front that runs and is not in front: a switch. One that
	// does not run yet is started, and app.open brings it forward itself.
	w.focused = "jelly"
	got = plan(`{"focus":"term"}`)
	if summary(got) != `focus:{"id":"term"}` || got[0].Action != contract.ActionInstanceSwitch || got[0].Order != 60 {
		t.Fatalf("focus = %s", summary(got))
	}
	if got = plan(`{"focus":"broken"}`); summary(got) != `start:{"id":"broken"}` {
		t.Fatalf("focus on one that does not run: %s", summary(got))
	}
	// Without a display to say what is in front, a focus cannot be planned.
	plain := newRig(t, unitstest.NewFakeSystemd(), podmantest.NewFakeEngine(), unitstest.FakeSession(t))
	plain.start(t, "term")
	if _, err := plain.m.Plan(ctx, "apps", json.RawMessage(`{"focus":"term"}`)); err == nil || !strings.Contains(err.Error(), "focus") {
		t.Fatalf("no display: %v", err)
	}
}

func TestAppsFacetChecks(t *testing.T) {
	m := New(Options{})
	for section, want := range map[string]string{
		`{"stop":["a"],"running":["a"]}`: `"a" is both in stop and in running`,
		`{"stop":["a"],"focus":"a"}`:     `focus "a" is in stop`,
		`{"stop":["a"],"running":["b"]}`: "",
	} {
		err := m.CheckFacet("apps", json.RawMessage(section))
		if (want == "") != (err == nil) || (want != "" && !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v, want %q", section, err, want)
		}
	}
	man := m.Manifest()
	if len(man.Facets) != 1 || man.Facets[0].Name != "apps" {
		t.Fatalf("facets = %+v", man.Facets)
	}
	schema, _ := sdk.CompileArgsSchema(man.Facets[0].Schema)
	for section, ok := range map[string]bool{`{"focus":"x"}`: true, `{"focus":5}`: false, `{"run":["x"]}`: false} {
		if err := schema.Validate(json.RawMessage(section)); (err == nil) != ok {
			t.Errorf("%s: %v", section, err)
		}
	}
}
