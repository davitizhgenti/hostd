package sdk

import (
	"context"
	"encoding/json"
	"time"
)

// Module is implemented by every module, built-in or external.
type Module interface {
	// Manifest describes the module: its namespaces, actions, events,
	// scopes and dependencies. It must not change while the module runs.
	Manifest() Manifest

	// Start is called once, after the modules this one requires have
	// started. The module keeps core for the rest of its life.
	Start(ctx context.Context, core Core) error

	// Validate re-checks an action's targets when it reaches the front of
	// its queue, while its resource keys are held. It returns an *Error
	// (usually not_found or instance_not_running) to refuse the action
	// before Handle runs. Arguments have already passed the schema.
	Validate(ctx context.Context, a Action) error

	// Handle carries out an action. A returned error that is not an *Error
	// is reported with code internal.
	Handle(ctx context.Context, a Action) (Result, error)

	// Stop is called once at shutdown, in reverse start order.
	Stop(ctx context.Context) error
}

// CoreVersion numbers the Core interface below. It is frozen between
// versions: adding, removing or changing a method is a new version, recorded
// in the module protocol docs. Version 2 added Tokens, Limiter and
// Introspect, which in-process modules used to be wired with by hand.
const CoreVersion = 2

// Core is what the core gives every module (version CoreVersion).
type Core interface {
	// Emit publishes a state change. The core fills in ID and Time, links
	// the event to the action being handled (if any), and, when Resource is
	// set, bumps that resource's version and records Version.
	Emit(e Event)

	// Do sends an action to its owning module through the full pipeline,
	// so priority, permissions and the audit trail apply. Called with the
	// ctx given to Handle, the action becomes a child of the one being
	// handled (Parent is set) and may reuse the keys its parents hold.
	Do(ctx context.Context, a Action) (Result, error)

	// Subscribe delivers events whose type matches filter (see MatchType)
	// until ctx is done, then closes the channel. A subscriber that falls
	// too far behind loses events: it then gets an event of type
	// EventLagged, and the stream goes on. On that event, a module that
	// keeps state built from events reads it again.
	Subscribe(ctx context.Context, filter string) <-chan Event

	// Handles reports whether some loaded module handles an action type,
	// so a module can skip optional work (focusing a window on a machine
	// without a display) instead of sending an action that must fail.
	Handles(actionType string) bool

	// Read answers another module's declared read (see Manifest.Reads) as
	// JSON, so modules share data without importing each other's types.
	Read(ctx context.Context, module, read string, params map[string]string) (json.RawMessage, error)

	// Tokens mints short-lived tokens for programs a module runs on behalf
	// of automation (a rule's script). It is nil where hostd offers none
	// (and for external modules, for now).
	Tokens() TokenMinter

	// Limiter is the core's guard against a rule that fires too often.
	// Nil where hostd offers none.
	Limiter() Limiter

	// Introspect shows what is running and which scopes exist. Only hostd's
	// own module gets it; every other module gets nil.
	Introspect() Introspector
}

// TokenMinter creates tokens that end with a run.
type TokenMinter interface {
	// Mint returns a secret for a token of kind automation with the given
	// scopes, valid for ttl, and the function that revokes it early.
	Mint(ctx context.Context, name string, scopes []string, ttl time.Duration) (secret string, revoke func(), err error)
}

// Limiter pauses a rule that fires too often.
type Limiter interface {
	// Allow records a firing of rule. When ok is false the rule is paused
	// until the given time; tripped says this firing caused the pause.
	Allow(rule string) (ok bool, until time.Time, tripped bool)
}

// Introspector is the core looking at itself.
type Introspector interface {
	// Running lists the actions being handled, oldest first.
	Running() []RunningAction
	// Scopes lists every token scope some module declares, besides the
	// core's own, sorted.
	Scopes() []string
}

// RunningAction is an action being handled.
type RunningAction struct {
	ID      string    `json:"id"`
	Type    string    `json:"type"`
	Source  Source    `json:"source"`
	Parent  string    `json:"parent,omitempty"`
	Started time.Time `json:"started"`
}

// StateReporter is implemented by modules that expose their current state
// (the catalog, running instances, volume...). The API serves it at
// GET /v1/state/<module>.
type StateReporter interface {
	State(ctx context.Context) (any, error)
}

// Hooker is implemented by modules that declare Hooks. The request comes
// from outside with no token: Hook must authenticate it. The result is
// the response body (status 200); an *Error sets the status as for
// actions.
type Hooker interface {
	Hook(ctx context.Context, name string, req HookRequest) (any, error)
}

// HookRequest is a hook's HTTP request.
type HookRequest struct {
	Params map[string]string   // path parameters
	Header map[string][]string // canonical header names (http.Header)
	Body   []byte
}

// Reader is implemented by modules that declare Reads in their manifest.
// params holds the path parameters and the query string (path wins).
// Return an *Error with not_found for a missing item.
type Reader interface {
	Read(ctx context.Context, name string, params map[string]string) (any, error)
}
