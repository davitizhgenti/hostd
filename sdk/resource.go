package sdk

import (
	"context"
	"encoding/json"
)

// A resource is something a person declares in a file: an app, a scene, a
// rule. Every kind of resource lives in a folder of its own
// (~/.config/hostd/<plural>/<name>.toml), is read the same way, checked the
// same way against the kind's schema, and managed through the same API
// (GET, PUT, DELETE on /v1/<plural>/<name>) and the same hostctl commands
// (get, apply, delete, edit). The module that owns the kind makes it real: it
// implements Reconciler.

// KindSpec declares a kind of resource a module owns.
type KindSpec struct {
	// Kind is the singular name ("rule"); Plural is the folder, the API
	// path and the hostctl word ("rules"). Both are lowercase words.
	Kind        string `json:"kind"`
	Plural      string `json:"plural"`
	Description string `json:"description,omitempty"`

	// Schema is the JSON Schema of a resource's spec: the file's content as
	// JSON. A file that does not fit is rejected before the module sees it.
	// Nil accepts any object (the module then checks it).
	Schema json.RawMessage `json:"schema,omitempty"`

	// WriteScope is the scope a token needs to apply or delete a resource
	// of this kind (default admin). Reading needs read.
	WriteScope string `json:"write_scope,omitempty"`

	// Columns make hostctl's list of these resources a table; each Path or
	// {field} is read from the item as the API shows it ("spec.on",
	// "status.data.fired"). Without them hostctl shows name, status and
	// problem. Empty is printed for no resources.
	Columns []Column `json:"columns,omitempty"`
	Empty   string   `json:"empty,omitempty"`

	// Params are the query parameters a list of this kind takes (?all=true),
	// given to the owner's Lister; hostctl offers them as switches.
	Params []Param `json:"params,omitempty"`

	// Verbs are the actions a resource of this kind takes, as
	// POST /v1/<plural>/<name>/<verb>.
	Verbs []VerbSpec `json:"verbs,omitempty"`
}

// VerbSpec maps a verb of a kind to one of the module's actions.
type VerbSpec struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Action is the action type it sends. Arg is the argument that takes
	// the resource's name (default "name").
	Action string `json:"action"`
	Arg    string `json:"arg,omitempty"`
}

// Resource is one declared thing, as the owning module is told of it.
type Resource struct {
	Kind string `json:"kind"`
	Name string `json:"name"` // the file name without .toml
	// Spec is the file's content as JSON, already checked against the
	// kind's schema. Raw is the file as written, for a module that parses
	// the TOML itself.
	Spec json.RawMessage `json:"spec"`
	Raw  []byte          `json:"-"`
	// Generation counts the changes to this resource since hostd started.
	Generation uint64 `json:"generation"`
}

// Condition says one thing about a resource's state, in the shape
// Kubernetes uses so that every kind reports problems the same way.
type Condition struct {
	// Type is what is described ("Ready", "Deploy").
	Type string `json:"type"`
	// Status is "True", "False" or "Unknown".
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// Condition statuses.
const (
	ConditionTrue    = "True"
	ConditionFalse   = "False"
	ConditionUnknown = "Unknown"
)

// ConditionReady is the condition every resource has: whether it is in use.
const ConditionReady = "Ready"

// ResourceStatus is what the owner knows about a resource besides its spec.
type ResourceStatus struct {
	Conditions []Condition `json:"conditions,omitempty"`
	// Data is anything else the kind reports (a rule's last result).
	Data json.RawMessage `json:"data,omitempty"`
}

// Ready reports whether the status has a Ready condition that is True.
func (s ResourceStatus) Ready() bool {
	for _, c := range s.Conditions {
		if c.Type == ConditionReady {
			return c.Status == ConditionTrue
		}
	}
	return false
}

// Reconciler is implemented by a module that owns kinds (Manifest.Kinds).
// The core calls it when a resource's file appears, changes or goes away,
// and once for every file when hostd starts.
type Reconciler interface {
	// Apply puts a resource in use. An error rejects it: the previous
	// version (if any) stays in use, and the error is shown on the resource
	// and sent as a resource.rejected event. A nil error with a status is
	// accepted; the status is shown beside the resource.
	Apply(ctx context.Context, r Resource) (ResourceStatus, error)
	// Delete takes a resource out of use.
	Delete(ctx context.Context, kind, name string) error
}

// Checker is implemented by a Reconciler that can say whether a resource
// would be accepted without putting it in use: what "hostctl apply --check"
// and a followed config's pre-check ask. Without it the core checks only
// the schema.
type Checker interface {
	Check(ctx context.Context, r Resource) error
}

// Listed is a resource the owner lists itself: one that is not (only) a
// file, such as an app found on the machine.
type Listed struct {
	// Name is the canonical name: the one a file would have.
	Name string
	// Spec is the resource as JSON. File is its file, "" when it has none.
	Spec json.RawMessage
	File string
	// Source says where it comes from ("desktop", "file").
	Source string
	Status ResourceStatus
}

// Lister is implemented by a Reconciler whose kind has resources that do not
// all come from files. The core asks it for lists and single items, and
// adds what it knows of the files (their generation, the problem of one that
// is not in use) to each. A file the owner rejected is still listed, from
// the core's own index.
type Lister interface {
	// List returns the resources of the kind; params are the query
	// parameters of the request.
	List(ctx context.Context, kind string, params map[string]string) ([]Listed, error)
	// Get returns one by name; the owner may know it by other names too
	// (an alias), and Listed.Name is then the canonical one.
	Get(ctx context.Context, kind, name string) (Listed, bool)
}

// StatusReporter is implemented by a Reconciler whose resources have a
// status that changes after Apply (a rule that fired, an app that runs).
// The core asks it when the resource is read.
type StatusReporter interface {
	StatusOf(ctx context.Context, kind, name string) (ResourceStatus, bool)
}

// Resource events, sent by the core (namespace resource.*).
const (
	EventResourceApplied  = "resource.applied"
	EventResourceRejected = "resource.rejected"
	EventResourceDeleted  = "resource.deleted"
)
