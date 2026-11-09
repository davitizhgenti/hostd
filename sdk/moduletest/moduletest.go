// Package moduletest is a fake sdk.Core for testing a module on its own:
// it records what the module emits and sends, answers its reads and Handles
// questions from a script, and feeds it events, with no engine in between.
//
//	core := moduletest.NewCore()
//	core.OnRead("apps", "instances", `[{"id":"firefox"}]`)
//	core.CanHandle("window.focus")
//	m.Start(ctx, core)
//	... drive the module ...
//	core.Emitted("lights.changed") // what it published
//	core.Did("window.focus")       // what it sent
//
// For a test of the module's actions through the real pipeline (locks,
// schema, priority), use core.New with a registry instead.
package moduletest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/davitizhgenti/hostd/sdk"
)

// Core is a fake sdk.Core. The zero value is not ready: use NewCore.
type Core struct {
	mu      sync.Mutex
	emitted []sdk.Event
	did     []sdk.Action
	reads   map[string]json.RawMessage
	handles map[string]bool
	doFunc  func(context.Context, sdk.Action) (sdk.Result, error)
	subs    []*sub

	// Optional capabilities, nil unless a test sets them.
	TokenMinter  sdk.TokenMinter
	RuleLimiter  sdk.Limiter
	Introspector sdk.Introspector
	// FacetInfos and Planned script Facets and Plan: Planned maps a facet
	// to the steps Plan returns for it.
	FacetInfos []sdk.FacetInfo
	Planned    map[string][]sdk.PlannedAction
}

type sub struct {
	filter string
	ch     chan sdk.Event
	ctx    context.Context
}

// NewCore returns a fake Core that answers every action with an empty
// applied result.
func NewCore() *Core {
	return &Core{reads: map[string]json.RawMessage{}, handles: map[string]bool{}}
}

// OnRead scripts the answer to Read(module, read): JSON text.
func (c *Core) OnRead(module, read, jsonText string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads[module+"/"+read] = json.RawMessage(jsonText)
}

// CanHandle makes Handles report true for the action types.
func (c *Core) CanHandle(types ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range types {
		c.handles[t] = true
	}
}

// OnDo sets what Do returns; without it every action succeeds.
func (c *Core) OnDo(f func(context.Context, sdk.Action) (sdk.Result, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.doFunc = f
}

// Publish delivers an event to the module's subscriptions that match it.
func (c *Core) Publish(ev sdk.Event) {
	c.mu.Lock()
	subs := append([]*sub(nil), c.subs...)
	c.mu.Unlock()
	for _, s := range subs {
		if !sdk.MatchType(s.filter, ev.Type) {
			continue
		}
		select {
		case s.ch <- ev:
		case <-s.ctx.Done():
		}
	}
}

// Emitted lists the events the module emitted, of the given types (all
// when none are given), oldest first.
func (c *Core) Emitted(types ...string) []sdk.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []sdk.Event
	for _, ev := range c.emitted {
		if len(types) == 0 || contains(types, ev.Type) {
			out = append(out, ev)
		}
	}
	return out
}

// Did lists the actions the module sent through Do, of the given types
// (all when none are given), oldest first.
func (c *Core) Did(types ...string) []sdk.Action {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []sdk.Action
	for _, a := range c.did {
		if len(types) == 0 || contains(types, a.Type) {
			out = append(out, a)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Emit implements sdk.Core. It fills the ID, as the real core does, so
// tests can tell events apart.
func (c *Core) Emit(ev sdk.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ev.ID = fmt.Sprintf("evt_test_%d", len(c.emitted)+1)
	c.emitted = append(c.emitted, ev)
}

// Do implements sdk.Core.
func (c *Core) Do(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	c.mu.Lock()
	a.ID = fmt.Sprintf("act_test_%d", len(c.did)+1)
	c.did = append(c.did, a)
	f := c.doFunc
	c.mu.Unlock()
	if f != nil {
		return f(ctx, a)
	}
	return sdk.Result{Action: a.ID, Status: sdk.StatusApplied}, nil
}

// Subscribe implements sdk.Core: events come from Publish.
func (c *Core) Subscribe(ctx context.Context, filter string) <-chan sdk.Event {
	s := &sub{filter: filter, ch: make(chan sdk.Event), ctx: ctx}
	c.mu.Lock()
	c.subs = append(c.subs, s)
	c.mu.Unlock()
	go func() {
		<-ctx.Done()
		close(s.ch)
	}()
	return s.ch
}

// Handles implements sdk.Core.
func (c *Core) Handles(actionType string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.handles[actionType]
}

// Read implements sdk.Core. An unscripted read is not_found.
func (c *Core) Read(_ context.Context, module, read string, _ map[string]string) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.reads[module+"/"+read]; ok {
		return v, nil
	}
	return nil, sdk.Errorf(sdk.CodeNotFound, "no scripted read %s", strings.Join([]string{module, read}, "/"))
}

// Tokens implements sdk.Core.
func (c *Core) Tokens() sdk.TokenMinter { return c.TokenMinter }

// Limiter implements sdk.Core.
func (c *Core) Limiter() sdk.Limiter { return c.RuleLimiter }

// Introspect implements sdk.Core.
func (c *Core) Introspect() sdk.Introspector { return c.Introspector }

// Facets implements sdk.Core.
func (c *Core) Facets() []sdk.FacetInfo { return c.FacetInfos }

// CheckFacet implements sdk.Core: every section is accepted.
func (c *Core) CheckFacet(string, json.RawMessage) error { return nil }

// Plan implements sdk.Core: the scripted steps, or not_found.
func (c *Core) Plan(_ context.Context, facet string, _ json.RawMessage) ([]sdk.PlannedAction, error) {
	if steps, ok := c.Planned[facet]; ok {
		return steps, nil
	}
	return nil, sdk.Errorf(sdk.CodeNotFound, "no scripted plan for %q", facet)
}

var _ sdk.Core = (*Core)(nil)
