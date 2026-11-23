package extmod

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/davitizhgenti/hostd/sdk"
)

// The example module declares the kind look and the facet lights: the core
// reconciles and plans through it like through a built-in module.
func TestExternalKindsAndFacets(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()

	res := sdk.Resource{Kind: "look", Name: "evening", Spec: json.RawMessage(`{"levels":{"porch":30}}`)}
	if _, err := r.e.Reconcile(ctx, res); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := r.e.Reconcile(ctx, sdk.Resource{Kind: "look", Name: "bad", Spec: json.RawMessage(`{"levels":{"garage":30}}`)}); sdk.CodeOf(err) != sdk.CodeInvalidArgs {
		t.Fatalf("a look of a light that is not there: %v", err)
	}
	if err := r.e.Unreconcile(ctx, "look", "evening"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := r.m.Check(ctx, res); err != nil { // the module has no check: the schema's word stands
		t.Fatalf("check: %v", err)
	}

	p, info, ok := r.e.Registry().Planner("lights")
	if !ok || info.Module != "lights" {
		t.Fatalf("planner %v %+v", ok, info)
	}
	steps, err := p.Plan(ctx, "lights", json.RawMessage(`{"porch":40,"living-room":0}`))
	if err != nil || len(steps) != 1 || steps[0].Action != "lights.set" || steps[0].Step != "porch" {
		t.Fatalf("plan %+v %v", steps, err)
	}
	fc := p.(sdk.FacetChecker)
	if err := fc.CheckFacet("lights", json.RawMessage(`{"porch":40}`)); err != nil {
		t.Fatalf("check_facet: %v", err)
	}
	if err := fc.CheckFacet("lights", json.RawMessage(`{"garage":40}`)); sdk.CodeOf(err) != sdk.CodeInvalidArgs {
		t.Fatalf("check_facet of a light that is not there: %v", err)
	}
}

func TestOptionalMethods(t *testing.T) {
	if optional(nil) != nil || optional(sdk.Errorf(sdk.CodeNotFound, "no method %q", "check")) != nil {
		t.Fatal("a missing method is not an error")
	}
	if optional(sdk.Errorf(sdk.CodeNotFound, "no light porch")) == nil || optional(sdk.Errorf(sdk.CodeInvalidArgs, "no method")) == nil {
		t.Fatal("other errors stay")
	}
}
