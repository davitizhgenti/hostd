package extmod

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/davitizhgenti/hostd/sdk"
)

// An external module that declares kinds is a sdk.Reconciler, and one that
// declares facets a sdk.Planner, over the protocol: the core's calls become
// the methods apply, delete, check, plan and check_facet. A module that
// declares neither is never asked.

var (
	_ sdk.Reconciler   = (*Module)(nil)
	_ sdk.Checker      = (*Module)(nil)
	_ sdk.Planner      = (*Module)(nil)
	_ sdk.FacetChecker = (*Module)(nil)
)

// Apply asks the module to put a resource in use.
func (m *Module) Apply(ctx context.Context, r sdk.Resource) (sdk.ResourceStatus, error) {
	var st sdk.ResourceStatus
	err := m.callModule(ctx, "apply", map[string]any{"resource": r}, &st)
	return st, err
}

// Delete asks the module to take a resource out of use.
func (m *Module) Delete(ctx context.Context, kind, name string) error {
	return m.callModule(ctx, "delete", map[string]string{"kind": kind, "name": name}, nil)
}

// Check asks whether the module would accept a resource. A module that
// does not implement check accepts what its schema accepts.
func (m *Module) Check(ctx context.Context, r sdk.Resource) error {
	return optional(m.callModule(ctx, "check", map[string]any{"resource": r}, nil))
}

// Plan asks the module for the actions that reach a facet's wish.
func (m *Module) Plan(ctx context.Context, facet string, desired json.RawMessage) ([]sdk.PlannedAction, error) {
	var out struct {
		Actions []sdk.PlannedAction `json:"actions"`
	}
	if err := m.callModule(ctx, "plan", map[string]any{"facet": facet, "desired": desired}, &out); err != nil {
		return nil, err
	}
	return out.Actions, nil
}

// CheckFacet asks the module for the rules of a facet that a schema cannot
// say. A module that does not implement check_facet has none.
func (m *Module) CheckFacet(facet string, desired json.RawMessage) error {
	ctx, cancel := context.WithTimeout(m.life, callWait)
	defer cancel()
	return optional(m.callModule(ctx, "check_facet", map[string]any{"facet": facet, "desired": desired}, nil))
}

// callWait bounds the calls that have no context of their own.
const callWait = 30 * time.Second

// optional turns "the module has no such method" into success.
func optional(err error) error {
	if err != nil && sdk.CodeOf(err) == sdk.CodeNotFound && strings.Contains(err.Error(), "no method") {
		return nil
	}
	return err
}
