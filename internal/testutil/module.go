// Package testutil holds test helpers shared across hostd packages, such as
// a configurable fake module.
package testutil

import (
	"context"
	"sync"

	"github.com/davitizhgenti/hostd/sdk"
)

// Module is a fake sdk.Module. Zero hooks succeed; Handle without a hook
// returns an applied result. It records the order of lifecycle calls in Log
// when one is set, so tests can check start and stop order across modules.
type Module struct {
	M sdk.Manifest

	StartFunc    func(ctx context.Context, core sdk.Core) error
	ValidateFunc func(ctx context.Context, a sdk.Action) error
	HandleFunc   func(ctx context.Context, a sdk.Action) (sdk.Result, error)
	StopFunc     func(ctx context.Context) error

	Log *Log

	mu      sync.Mutex
	core    sdk.Core
	handled []sdk.Action
}

// NewModule returns a fake module named name that owns "<name>.*", with
// the given actions (scope "admin", no arguments) and events.
func NewModule(name string, requires []string, actions []string, events []string) *Module {
	m := &Module{M: sdk.Manifest{
		Name:     name,
		Version:  "0.0.0-test",
		Owns:     []string{name + ".*"},
		Requires: requires,
	}}
	for _, a := range actions {
		m.M.Actions = append(m.M.Actions, sdk.ActionSpec{Type: a, Scope: sdk.ScopeAdmin})
	}
	for _, e := range events {
		m.M.Events = append(m.M.Events, sdk.EventSpec{Type: e})
	}
	return m
}

func (m *Module) Manifest() sdk.Manifest { return m.M }

func (m *Module) Start(ctx context.Context, core sdk.Core) error {
	m.Log.Add("start " + m.M.Name)
	m.mu.Lock()
	m.core = core
	m.mu.Unlock()
	if m.StartFunc != nil {
		return m.StartFunc(ctx, core)
	}
	return nil
}

func (m *Module) Validate(ctx context.Context, a sdk.Action) error {
	if m.ValidateFunc != nil {
		return m.ValidateFunc(ctx, a)
	}
	return nil
}

func (m *Module) Handle(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	m.mu.Lock()
	m.handled = append(m.handled, a)
	m.mu.Unlock()
	if m.HandleFunc != nil {
		return m.HandleFunc(ctx, a)
	}
	return sdk.Result{Status: sdk.StatusApplied}, nil
}

func (m *Module) Stop(ctx context.Context) error {
	m.Log.Add("stop " + m.M.Name)
	if m.StopFunc != nil {
		return m.StopFunc(ctx)
	}
	return nil
}

// Core returns the Core given to Start, or nil before Start.
func (m *Module) Core() sdk.Core {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.core
}

// Handled returns the actions Handle was called with, in order.
func (m *Module) Handled() []sdk.Action {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]sdk.Action(nil), m.handled...)
}

// Log is a concurrency-safe list of strings, for recording call order.
// A nil *Log ignores Add.
type Log struct {
	mu      sync.Mutex
	entries []string
}

func (l *Log) Add(s string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, s)
}

func (l *Log) Entries() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.entries...)
}
