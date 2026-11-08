// Package kit is the quick way to write a module in Go: actions, reads and
// events are declared with typed Go values, and the kit builds the manifest
// (schemas come from the types, see sdk.SchemaOf) and does the decoding,
// dispatch and encoding that a hand-written sdk.Module repeats.
//
//	m := kit.New("lights", "1.0.0").Owns("lights.*").Scope("lights", "Change the lights")
//
//	type SetArgs struct {
//		ID         string `json:"id"`
//		Brightness int    `json:"brightness" min:"0" max:"100"`
//	}
//	kit.Action(m, kit.Spec{Type: "lights.set", Scope: "lights", Keys: []sdk.KeyTemplate{"light:{id}"},
//		Route: "POST /v1/lights/{id}"},
//		func(ctx context.Context, c kit.Ctx, a SetArgs) (struct{}, error) { ... })
//
// A *Module is an sdk.Module: give it to the core like any other.
package kit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/davitizhgenti/hostd/sdk"
)

// Ctx is what a handler gets besides its context and arguments.
type Ctx struct {
	// Core is the module's Core: emit events, send actions, read.
	Core sdk.Core
	// Action is the action being handled (its ID, source and cause); it is
	// the zero Action in Start hooks and reads.
	Action sdk.Action
}

// Spec declares an action. Type and Scope are required.
type Spec struct {
	Type        string
	Description string
	Scope       string
	ArgScopes   map[string]string
	// Keys are the resource keys the action locks (see sdk.ActionSpec).
	Keys []sdk.KeyTemplate
	// Secret names arguments whose values are never recorded.
	Secret  []string
	Timeout time.Duration
	// Slow actions return 202 at once; the outcome follows as an event.
	Slow bool
	// Route is the friendly HTTP route, "POST /v1/lights/{id}".
	Route string
	CLI   *sdk.CLI
}

// ReadSpec declares a read. Name, Path and the result type are required.
type ReadSpec struct {
	Name        string
	Description string
	Path        string
	// Scope is "" for read, or sdk.ScopePublic.
	Scope string
	CLI   *sdk.CLI
}

// Module is a module built with the kit. It implements sdk.Module,
// sdk.Reader and sdk.StateReporter.
type Module struct {
	man sdk.Manifest

	mu   sync.RWMutex
	core sdk.Core

	handlers   map[string]func(context.Context, Ctx, sdk.Action) (sdk.Result, error)
	validators map[string]func(context.Context, Ctx, sdk.Action) error
	reads      map[string]func(context.Context, Ctx, map[string]string) (any, error)
	kinds      map[string]*kindHandler
	starts     []func(context.Context, Ctx) error
	stops      []func(context.Context) error
	state      func(context.Context, Ctx) (any, error)
}

// New starts a module with a name and a version.
func New(name, version string) *Module {
	return &Module{
		man:        sdk.Manifest{Name: name, Version: version},
		handlers:   map[string]func(context.Context, Ctx, sdk.Action) (sdk.Result, error){},
		validators: map[string]func(context.Context, Ctx, sdk.Action) error{},
		reads:      map[string]func(context.Context, Ctx, map[string]string) (any, error){},
		kinds:      map[string]*kindHandler{},
	}
}

// Owns adds namespaces the module owns ("lights.*").
func (m *Module) Owns(patterns ...string) *Module {
	m.man.Owns = append(m.man.Owns, patterns...)
	return m
}

// Requires adds modules that must start first.
func (m *Module) Requires(names ...string) *Module {
	m.man.Requires = append(m.man.Requires, names...)
	return m
}

// Scope declares a token scope the module defines.
func (m *Module) Scope(name, description string) *Module {
	m.man.Scopes = append(m.man.Scopes, sdk.ScopeSpec{Name: name, Description: description})
	return m
}

// OnStart adds a function run when the module starts, in order, with the
// Core available.
func (m *Module) OnStart(f func(ctx context.Context, c Ctx) error) *Module {
	m.starts = append(m.starts, f)
	return m
}

// OnStop adds a function run at shutdown, in reverse order of adding.
func (m *Module) OnStop(f func(ctx context.Context) error) *Module {
	m.stops = append(m.stops, f)
	return m
}

// WithState serves GET /v1/state/<module> from f.
func (m *Module) WithState(f func(ctx context.Context, c Ctx) (any, error)) *Module {
	m.state = f
	return m
}

// Action declares an action whose arguments are the Go type A and whose
// result data is R (struct{} for none). The schema comes from A. It panics
// if the type is declared twice or the route is malformed: a mistake in
// the module, found when it is built.
func Action[A, R any](m *Module, spec Spec, h func(ctx context.Context, c Ctx, args A) (R, error)) {
	if _, dup := m.handlers[spec.Type]; dup {
		panic(fmt.Sprintf("kit: action %q declared twice", spec.Type))
	}
	as := sdk.ActionSpec{
		Type: spec.Type, Description: spec.Description, Scope: spec.Scope,
		ArgScopes: spec.ArgScopes, Keys: spec.Keys, Secret: spec.Secret,
		Timeout: sdk.Duration(spec.Timeout), Slow: spec.Slow, CLI: spec.CLI,
	}
	if !isNone[A]() {
		as.Schema = sdk.SchemaOf[A]()
	}
	if spec.Route != "" {
		method, path, ok := strings.Cut(spec.Route, " ")
		if !ok || method == "" || !strings.HasPrefix(path, "/") {
			panic(fmt.Sprintf("kit: action %q: route %q: use \"POST /v1/path\"", spec.Type, spec.Route))
		}
		as.Route = &sdk.Route{Method: method, Path: path}
	}
	m.man.Actions = append(m.man.Actions, as)
	m.handlers[spec.Type] = func(ctx context.Context, c Ctx, a sdk.Action) (sdk.Result, error) {
		var args A
		if err := a.DecodeArgs(&args); err != nil {
			return sdk.Result{}, err
		}
		out, err := h(ctx, c, args)
		if err != nil {
			return sdk.Result{}, err
		}
		return sdk.Result{Data: dataOf(out)}, nil
	}
}

// Validate adds the re-check of an action's targets that runs when it
// reaches the front of its queue (see sdk.Module.Validate). The arguments
// have already passed the schema.
func Validate[A any](m *Module, actionType string, f func(ctx context.Context, c Ctx, args A) error) {
	m.validators[actionType] = func(ctx context.Context, c Ctx, a sdk.Action) error {
		var args A
		if err := a.DecodeArgs(&args); err != nil {
			return err
		}
		return f(ctx, c, args)
	}
}

// Read declares a read. P is the type of its parameters (path and query),
// R what it returns; the result's schema is recorded in the manifest.
// Parameters are decoded as JSON, with numbers and booleans taken from
// their text where the field needs one, so P may be a struct of strings,
// ints and bools tagged like an action's arguments, or map[string]string.
func Read[P, R any](m *Module, spec ReadSpec, h func(ctx context.Context, c Ctx, p P) (R, error)) {
	if _, dup := m.reads[spec.Name]; dup {
		panic(fmt.Sprintf("kit: read %q declared twice", spec.Name))
	}
	m.man.Reads = append(m.man.Reads, sdk.ReadSpec{
		Name: spec.Name, Description: spec.Description, Path: spec.Path, Scope: spec.Scope,
		Schema: sdk.SchemaOf[R](), CLI: spec.CLI,
	})
	m.reads[spec.Name] = func(ctx context.Context, c Ctx, params map[string]string) (any, error) {
		var p P
		if err := decodeParams(params, &p); err != nil {
			return nil, sdk.Errorf(sdk.CodeInvalidArgs, "%s: %v", spec.Name, err)
		}
		return h(ctx, c, p)
	}
}

// EventDef is a declared event type carrying data of type D.
type EventDef[D any] struct {
	m   *Module
	typ string
}

// Event declares an event type of the module.
func Event[D any](m *Module, typ, description string) EventDef[D] {
	m.man.Events = append(m.man.Events, sdk.EventSpec{Type: typ, Description: description})
	return EventDef[D]{m: m, typ: typ}
}

// Type is the event's type.
func (e EventDef[D]) Type() string { return e.typ }

// Emit publishes the event for the action being handled (if c has one).
// resource, when not empty, names the resource that changed, so the core
// versions it.
func (e EventDef[D]) Emit(c Ctx, data D, resource string) {
	if c.Core == nil {
		return
	}
	c.Core.Emit(sdk.Event{Type: e.typ, Action: c.Action.ID, Resource: resource, Data: dataOf(data)})
}

// Manifest implements sdk.Module.
func (m *Module) Manifest() sdk.Manifest {
	man := m.man
	man.Actions = append([]sdk.ActionSpec(nil), m.man.Actions...)
	return man
}

func (m *Module) ctx(a sdk.Action) Ctx {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return Ctx{Core: m.core, Action: a}
}

// Start implements sdk.Module.
func (m *Module) Start(ctx context.Context, core sdk.Core) error {
	m.mu.Lock()
	m.core = core
	m.mu.Unlock()
	for _, f := range m.starts {
		if err := f(ctx, m.ctx(sdk.Action{})); err != nil {
			return err
		}
	}
	return nil
}

// Stop implements sdk.Module.
func (m *Module) Stop(ctx context.Context) error {
	var first error
	for i := len(m.stops) - 1; i >= 0; i-- {
		if err := m.stops[i](ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Validate implements sdk.Module.
func (m *Module) Validate(ctx context.Context, a sdk.Action) error {
	if v := m.validators[a.Type]; v != nil {
		return v(ctx, m.ctx(a), a)
	}
	return nil
}

// Handle implements sdk.Module.
func (m *Module) Handle(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	h := m.handlers[a.Type]
	if h == nil {
		return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "%s has no action %q", m.man.Name, a.Type)
	}
	return h(ctx, m.ctx(a), a)
}

// Read implements sdk.Reader.
func (m *Module) Read(ctx context.Context, name string, params map[string]string) (any, error) {
	r := m.reads[name]
	if r == nil {
		return nil, sdk.Errorf(sdk.CodeNotFound, "%s has no read %q", m.man.Name, name)
	}
	return r(ctx, m.ctx(sdk.Action{}), params)
}

// State implements sdk.StateReporter: f from WithState, or an empty object.
func (m *Module) State(ctx context.Context) (any, error) {
	if m.state == nil {
		return struct{}{}, nil
	}
	return m.state(ctx, m.ctx(sdk.Action{}))
}

// dataOf encodes a handler's result; an empty struct, nil or an error-free
// zero struct{} carries no data.
func dataOf(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	if t := reflect.TypeOf(v); t.Kind() == reflect.Struct && t.NumField() == 0 {
		return nil
	}
	return sdk.MustJSON(v)
}

// isNone reports whether T is struct{}: an action with no arguments.
func isNone[T any]() bool {
	var zero T
	t := reflect.TypeOf(&zero).Elem()
	return t.Kind() == reflect.Struct && t.NumField() == 0
}

// decodeParams fills p from a read's text parameters. It first tries them
// all as strings; if a field wants a number or a boolean it tries again
// with the values that look like one given as such.
func decodeParams(params map[string]string, p any) error {
	asStrings := make(map[string]any, len(params))
	for k, v := range params {
		asStrings[k] = v
	}
	b, _ := json.Marshal(asStrings)
	err := json.Unmarshal(b, p)
	if err == nil {
		return nil
	}
	var wrongType *json.UnmarshalTypeError
	if !errors.As(err, &wrongType) {
		return err
	}
	typed := make(map[string]any, len(params))
	for k, v := range params {
		switch v {
		case "true", "false":
			typed[k] = v == "true"
		default:
			if n, perr := strconv.ParseFloat(v, 64); perr == nil {
				typed[k] = json.Number(strconv.FormatFloat(n, 'f', -1, 64))
			} else {
				typed[k] = v
			}
		}
	}
	b, _ = json.Marshal(typed)
	return json.Unmarshal(b, p)
}

// KindSpec declares a kind of resource (a file in ~/.config/hostd/<plural>/).
type KindSpec struct {
	Kind, Plural, Description string
	// WriteScope is the scope to apply or delete one (default admin).
	WriteScope string
	// Verbs are the actions a resource takes: POST /v1/<plural>/<name>/<verb>.
	Verbs []sdk.VerbSpec
}

// Res is a resource of a kind whose spec is the Go type S.
type Res[S any] struct {
	Name string
	Spec S
	// Raw is the file as written; Generation counts its changes.
	Raw        []byte
	Generation uint64
}

type kindHandler struct {
	apply  func(context.Context, Ctx, sdk.Resource) (sdk.ResourceStatus, error)
	remove func(context.Context, Ctx, string) error
	check  func(context.Context, Ctx, sdk.Resource) error
}

// Kind declares a kind of resource whose spec is the Go type S; the schema
// comes from S. apply puts a resource in use (an error rejects it and the
// previous version stays), remove takes it out. The module then implements
// sdk.Reconciler.
func Kind[S any](m *Module, spec KindSpec,
	apply func(ctx context.Context, c Ctx, r Res[S]) (sdk.ResourceStatus, error),
	remove func(ctx context.Context, c Ctx, name string) error) {
	if _, dup := m.kinds[spec.Kind]; dup {
		panic(fmt.Sprintf("kit: kind %q declared twice", spec.Kind))
	}
	m.man.Kinds = append(m.man.Kinds, sdk.KindSpec{
		Kind: spec.Kind, Plural: spec.Plural, Description: spec.Description,
		Schema: sdk.SchemaOf[S](), WriteScope: spec.WriteScope, Verbs: spec.Verbs,
	})
	typed := func(r sdk.Resource) (Res[S], error) {
		var out Res[S]
		if err := json.Unmarshal(r.Spec, &out.Spec); err != nil {
			return out, fmt.Errorf("%s %q: %w", r.Kind, r.Name, err)
		}
		out.Name, out.Raw, out.Generation = r.Name, r.Raw, r.Generation
		return out, nil
	}
	m.kinds[spec.Kind] = &kindHandler{
		apply: func(ctx context.Context, c Ctx, r sdk.Resource) (sdk.ResourceStatus, error) {
			t, err := typed(r)
			if err != nil {
				return sdk.ResourceStatus{}, err
			}
			return apply(ctx, c, t)
		},
		remove: remove,
	}
}

// CheckKind adds the dry run for a kind: whether a resource would be
// accepted, without putting it in use (hostctl apply --check).
func CheckKind[S any](m *Module, kind string, check func(ctx context.Context, c Ctx, r Res[S]) error) {
	h := m.kinds[kind]
	if h == nil {
		panic(fmt.Sprintf("kit: CheckKind: no kind %q", kind))
	}
	h.check = func(ctx context.Context, c Ctx, r sdk.Resource) error {
		var out Res[S]
		if err := json.Unmarshal(r.Spec, &out.Spec); err != nil {
			return err
		}
		out.Name, out.Raw, out.Generation = r.Name, r.Raw, r.Generation
		return check(ctx, c, out)
	}
}

// Apply implements sdk.Reconciler.
func (m *Module) Apply(ctx context.Context, r sdk.Resource) (sdk.ResourceStatus, error) {
	h := m.kinds[r.Kind]
	if h == nil {
		return sdk.ResourceStatus{}, sdk.Errorf(sdk.CodeNotFound, "%s has no kind %q", m.man.Name, r.Kind)
	}
	return h.apply(ctx, m.ctx(sdk.Action{}), r)
}

// Delete implements sdk.Reconciler.
func (m *Module) Delete(ctx context.Context, kind, name string) error {
	h := m.kinds[kind]
	if h == nil {
		return sdk.Errorf(sdk.CodeNotFound, "%s has no kind %q", m.man.Name, kind)
	}
	if h.remove == nil {
		return nil
	}
	return h.remove(ctx, m.ctx(sdk.Action{}), name)
}

// Check implements sdk.Checker: a kind's dry run, or accepted when it has
// none (the schema was checked already).
func (m *Module) Check(ctx context.Context, r sdk.Resource) error {
	if h := m.kinds[r.Kind]; h != nil && h.check != nil {
		return h.check(ctx, m.ctx(sdk.Action{}), r)
	}
	return nil
}
