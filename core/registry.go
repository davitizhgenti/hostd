package core

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/davitizhgenti/hostd/sdk"
)

// Namespaces the core itself emits events in; no module may claim them.
var coreNamespaces = []string{"action.*", "bus.*", "core.*"}

// moduleState tracks where a module is in its lifecycle.
type moduleState int

const (
	stateRegistered moduleState = iota
	stateStarted
	stateStopped
)

// module is a registered module with its validated manifest and compiled
// argument schemas.
type module struct {
	mod     sdk.Module
	man     sdk.Manifest
	schemas map[string]*sdk.ArgsSchema // by action type
	state   moduleState
}

// Registry holds the modules, checks that they fit together, and starts and
// stops them in dependency order.
type Registry struct {
	mu      sync.RWMutex
	modules []*module          // registration order
	byName  map[string]*module // by manifest name
	order   []*module          // start order, set by Resolve
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byName: map[string]*module{}}
}

// Add registers a module after checking its manifest on its own and
// against the modules already added: unique name, and namespaces that
// overlap neither another module nor the core's.
func (r *Registry) Add(m sdk.Module) error {
	man := m.Manifest()
	if err := man.Validate(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.order != nil {
		return fmt.Errorf("module %q: registry already resolved", man.Name)
	}
	if _, dup := r.byName[man.Name]; dup {
		return fmt.Errorf("module %q registered twice", man.Name)
	}
	for _, p := range man.Owns {
		for _, c := range coreNamespaces {
			if sdk.PatternsOverlap(p, c) {
				return fmt.Errorf("module %q: namespace %q is reserved for the core", man.Name, p)
			}
		}
		for _, other := range r.modules {
			for _, q := range other.man.Owns {
				if sdk.PatternsOverlap(p, q) {
					return fmt.Errorf("module %q: namespace %q overlaps %q owned by module %q",
						man.Name, p, q, other.man.Name)
				}
			}
		}
	}

	entry := &module{mod: m, man: man, schemas: make(map[string]*sdk.ArgsSchema, len(man.Actions))}
	for _, a := range man.Actions {
		s, err := sdk.CompileArgsSchema(a.Schema)
		if err != nil { // Validate compiled it already; cannot happen
			return fmt.Errorf("module %q: action %q: %w", man.Name, a.Type, err)
		}
		entry.schemas[a.Type] = s
	}
	r.modules = append(r.modules, entry)
	r.byName[man.Name] = entry
	return nil
}

// Resolve checks dependencies and fixes the start order: every module after
// the modules it requires, otherwise in registration order. It refuses
// missing dependencies and cycles.
func (r *Registry) Resolve() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var errs []error
	for _, m := range r.modules {
		for _, dep := range m.man.Requires {
			if _, ok := r.byName[dep]; !ok {
				errs = append(errs, fmt.Errorf("module %q requires %q, which is not enabled", m.man.Name, dep))
			}
		}
	}
	// Every scope an action needs must be declared by some module (or be
	// one of the core's), so no action is unreachable by any token.
	declared := map[string]bool{sdk.ScopeRead: true, sdk.ScopeAdmin: true}
	for _, m := range r.modules {
		for _, sc := range m.man.Scopes {
			declared[sc.Name] = true
		}
	}
	for _, m := range r.modules {
		for _, a := range m.man.Actions {
			if !declared[a.Scope] {
				errs = append(errs, fmt.Errorf("module %q, action %q: scope %q is not declared by any module", m.man.Name, a.Type, a.Scope))
			}
			for arg, sc := range a.ArgScopes {
				if !declared[sc] {
					errs = append(errs, fmt.Errorf("module %q, action %q: arg_scopes %q: scope %q is not declared by any module",
						m.man.Name, a.Type, arg, sc))
				}
			}
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	// Kahn's algorithm, always picking the earliest-registered ready module
	// so the order is stable.
	indegree := map[string]int{}
	for _, m := range r.modules {
		indegree[m.man.Name] = len(m.man.Requires)
	}
	order := make([]*module, 0, len(r.modules))
	done := map[string]bool{}
	for len(order) < len(r.modules) {
		progressed := false
		for _, m := range r.modules {
			if done[m.man.Name] || indegree[m.man.Name] > 0 {
				continue
			}
			done[m.man.Name] = true
			order = append(order, m)
			for _, other := range r.modules {
				for _, dep := range other.man.Requires {
					if dep == m.man.Name {
						indegree[other.man.Name]--
					}
				}
			}
			progressed = true
			break
		}
		if !progressed {
			var cycle []string
			for _, m := range r.modules {
				if !done[m.man.Name] {
					cycle = append(cycle, m.man.Name)
				}
			}
			sort.Strings(cycle)
			return fmt.Errorf("modules depend on each other in a cycle: %s", strings.Join(cycle, ", "))
		}
	}
	r.order = order
	return nil
}

// StartAll starts the modules in dependency order, giving each the Core
// returned by coreFor. If one fails, the ones already started are stopped
// in reverse order and the error is returned: nothing is left half-started.
func (r *Registry) StartAll(ctx context.Context, coreFor func(name string) sdk.Core) error {
	r.mu.RLock()
	order := r.order
	r.mu.RUnlock()
	if order == nil {
		return errors.New("registry not resolved")
	}
	for i, m := range order {
		if err := m.mod.Start(ctx, coreFor(m.man.Name)); err != nil {
			startErr := fmt.Errorf("module %q failed to start: %w", m.man.Name, err)
			for j := i - 1; j >= 0; j-- {
				if serr := r.stopOne(ctx, order[j]); serr != nil {
					startErr = errors.Join(startErr, serr)
				}
			}
			return startErr
		}
		r.setState(m, stateStarted)
	}
	return nil
}

// StopAll stops started modules in reverse start order, stopping every one
// even if some fail, and returns all errors.
func (r *Registry) StopAll(ctx context.Context) error {
	r.mu.RLock()
	order := r.order
	r.mu.RUnlock()
	var errs []error
	for i := len(order) - 1; i >= 0; i-- {
		if !r.started(order[i]) {
			continue
		}
		if err := r.stopOne(ctx, order[i]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (r *Registry) stopOne(ctx context.Context, m *module) error {
	err := m.mod.Stop(ctx)
	r.setState(m, stateStopped)
	if err != nil {
		return fmt.Errorf("module %q failed to stop: %w", m.man.Name, err)
	}
	return nil
}

func (r *Registry) setState(m *module, s moduleState) {
	r.mu.Lock()
	m.state = s
	r.mu.Unlock()
}

// lookup returns the module owning an action type and its spec. ok is
// false when no module declares the type.
func (r *Registry) lookup(actionType string) (m *module, spec sdk.ActionSpec, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, m := range r.modules {
		if spec, ok := m.man.Action(actionType); ok {
			return m, spec, true
		}
	}
	return nil, sdk.ActionSpec{}, false
}

// started reports whether m is running.
func (r *Registry) started(m *module) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return m.state == stateStarted
}

// CanEmit reports whether the named module may emit an event of this type:
// the type must be declared in its manifest.
func (r *Registry) CanEmit(moduleName, eventType string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.byName[moduleName]
	if !ok {
		return false
	}
	for _, e := range m.man.Events {
		if e.Type == eventType {
			return true
		}
	}
	return false
}

// Order returns module names in start order (empty before Resolve).
func (r *Registry) Order() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, len(r.order))
	for i, m := range r.order {
		names[i] = m.man.Name
	}
	return names
}

// Manifests returns the manifests of all modules, in start order once
// resolved and registration order before.
func (r *Registry) Manifests() []sdk.Manifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := r.order
	if list == nil {
		list = r.modules
	}
	out := make([]sdk.Manifest, len(list))
	for i, m := range list {
		out[i] = m.man
	}
	return out
}
