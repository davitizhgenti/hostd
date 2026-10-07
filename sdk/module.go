package sdk

import "context"

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

// Core is what the core gives every module.
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
	// until ctx is done, then closes the channel.
	Subscribe(ctx context.Context, filter string) <-chan Event
}

// StateReporter is implemented by modules that expose their current state
// (the catalog, running instances, volume...). The API serves it at
// GET /v1/state/<module>.
type StateReporter interface {
	State(ctx context.Context) (any, error)
}
