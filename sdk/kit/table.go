package kit

import (
	"context"
	"fmt"
	"slices"

	"github.com/davitizhgenti/hostd/sdk"
)

// Actions is a module's actions as a table: each declared once, with its
// arguments as a Go type (the schema comes from it) and a typed handler.
// A module keeps one, lists it in its manifest (Specs) and hands Handle
// and Validate to it, instead of a schema in a string next to the struct
// and a switch on the action's type. kit.Module uses one; so can any
// module that implements sdk.Module itself.
type Actions struct {
	module     string
	specs      []sdk.ActionSpec
	handlers   map[string]func(context.Context, sdk.Action) (sdk.Result, error)
	validators map[string]func(context.Context, sdk.Action) error
}

// NewActions starts the table of a module's actions.
func NewActions(module string) *Actions {
	return &Actions{module: module,
		handlers:   map[string]func(context.Context, sdk.Action) (sdk.Result, error){},
		validators: map[string]func(context.Context, sdk.Action) error{}}
}

// On declares an action whose arguments are A and whose result data is R
// (struct{} for none). spec.Schema is filled from A unless it is set or A
// is struct{}. It panics on a type declared twice: a mistake in the
// module, found when it is built.
func On[A, R any](t *Actions, spec sdk.ActionSpec, h func(ctx context.Context, a sdk.Action, args A) (R, error)) {
	if _, dup := t.handlers[spec.Type]; dup {
		panic(fmt.Sprintf("kit: %s: action %q declared twice", t.module, spec.Type))
	}
	if spec.Schema == nil && !isNone[A]() {
		spec.Schema = sdk.SchemaOf[A]()
	}
	t.specs = append(t.specs, spec)
	t.handlers[spec.Type] = func(ctx context.Context, a sdk.Action) (sdk.Result, error) {
		var args A
		if err := a.DecodeArgs(&args); err != nil {
			return sdk.Result{}, err
		}
		out, err := h(ctx, a, args)
		if err != nil {
			return sdk.Result{}, err
		}
		return sdk.Result{Data: dataOf(out)}, nil
	}
}

// OnResult declares an action whose handler builds the whole sdk.Result
// itself (its data, and the events it reports with it); the arguments are
// still A, and the schema comes from A.
func OnResult[A any](t *Actions, spec sdk.ActionSpec, h func(ctx context.Context, a sdk.Action, args A) (sdk.Result, error)) {
	On(t, spec, func(context.Context, sdk.Action, A) (struct{}, error) { return struct{}{}, nil })
	t.handlers[spec.Type] = func(ctx context.Context, a sdk.Action) (sdk.Result, error) {
		var args A
		if err := a.DecodeArgs(&args); err != nil {
			return sdk.Result{}, err
		}
		return h(ctx, a, args)
	}
}

// Check adds the re-check of an action's targets that runs when it
// reaches the front of its queue (see sdk.Module.Validate).
func Check[A any](t *Actions, actionType string, f func(ctx context.Context, a sdk.Action, args A) error) {
	t.validators[actionType] = func(ctx context.Context, a sdk.Action) error {
		var args A
		if err := a.DecodeArgs(&args); err != nil {
			return err
		}
		return f(ctx, a, args)
	}
}

// Specs are the declared actions, for the manifest, in the order declared.
func (t *Actions) Specs() []sdk.ActionSpec { return slices.Clone(t.specs) }

// Has reports whether the table declares an action type.
func (t *Actions) Has(actionType string) bool { _, ok := t.handlers[actionType]; return ok }

// Handle runs an action's handler.
func (t *Actions) Handle(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	h := t.handlers[a.Type]
	if h == nil {
		return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "%s has no action %q", t.module, a.Type)
	}
	return h(ctx, a)
}

// Validate runs an action's check, if it has one.
func (t *Actions) Validate(ctx context.Context, a sdk.Action) error {
	if v := t.validators[a.Type]; v != nil {
		return v(ctx, a)
	}
	return nil
}

// Reads is a module's reads as a table, the way Actions is its actions.
type Reads struct {
	module string
	specs  []sdk.ReadSpec
	reads  map[string]func(context.Context, map[string]string) (any, error)
}

// NewReads starts the table of a module's reads.
func NewReads(module string) *Reads {
	return &Reads{module: module, reads: map[string]func(context.Context, map[string]string) (any, error){}}
}

// Serve declares a read: P is the type of its parameters (path and
// query), R what it returns. spec.Schema is filled from R unless it is
// set. Parameters are decoded as JSON, with numbers and booleans taken from
// their text where the field needs one, so P may be a struct of strings,
// ints and bools tagged like an action's arguments, or map[string]string.
func Serve[P, R any](t *Reads, spec sdk.ReadSpec, h func(ctx context.Context, p P) (R, error)) {
	if _, dup := t.reads[spec.Name]; dup {
		panic(fmt.Sprintf("kit: %s: read %q declared twice", t.module, spec.Name))
	}
	if spec.Schema == nil {
		spec.Schema = sdk.SchemaOf[R]()
	}
	t.specs = append(t.specs, spec)
	t.reads[spec.Name] = func(ctx context.Context, params map[string]string) (any, error) {
		var p P
		if err := decodeParams(params, &p); err != nil {
			return nil, sdk.Errorf(sdk.CodeInvalidArgs, "%s: %v", spec.Name, err)
		}
		return h(ctx, p)
	}
}

// Specs are the declared reads, for the manifest.
func (t *Reads) Specs() []sdk.ReadSpec { return slices.Clone(t.specs) }

// Read runs a read.
func (t *Reads) Read(ctx context.Context, name string, params map[string]string) (any, error) {
	r := t.reads[name]
	if r == nil {
		return nil, sdk.Errorf(sdk.CodeNotFound, "%s has no read %q", t.module, name)
	}
	return r(ctx, params)
}
