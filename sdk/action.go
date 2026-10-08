package sdk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// Action is a request to change something, owned by one module.
type Action struct {
	ID   string          `json:"id"`
	Type string          `json:"type"`
	Args json.RawMessage `json:"args,omitempty"`

	// ExpectVersion, when set, refuses the action with precondition_failed
	// if the resource changed since the caller read this version.
	ExpectVersion *uint64 `json:"expect_version,omitempty"`

	Source Source `json:"source"`

	// Cause is the event this action reacts to (set for rule-triggered
	// actions). Parent is the action whose handler sent this one through
	// Core.Do. Only Parent lets an action reuse held resource keys.
	Cause  string `json:"cause,omitempty"`
	Parent string `json:"parent,omitempty"`
}

// DecodeArgs unmarshals the action's arguments into v. Missing arguments
// decode as an empty object.
func (a Action) DecodeArgs(v any) error {
	args := a.Args
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage("{}")
	}
	if err := json.Unmarshal(args, v); err != nil {
		return Errorf(CodeInvalidArgs, "%s: %v", a.Type, err)
	}
	return nil
}

// SourceKind says where an action came from, which sets its priority.
type SourceKind string

const (
	// SourceLocal is someone at the screen: keyboard, mouse, controller,
	// the on-screen menu.
	SourceLocal SourceKind = "local"
	// SourceManual is a person using the CLI, a phone or an HTTP client
	// with a device token.
	SourceManual SourceKind = "manual"
	// SourceAutomation is a rule, or a script running with a script token.
	SourceAutomation SourceKind = "automation"
	// SourceExternal is a change hostd observed but did not make, such as a
	// game changing its own volume. It never creates a hold.
	SourceExternal SourceKind = "external"
)

// Priority of actions from this source: local 3, manual 2, automation 1,
// external 0. Unknown kinds get 0.
func (k SourceKind) Priority() int {
	switch k {
	case SourceLocal:
		return 3
	case SourceManual:
		return 2
	case SourceAutomation:
		return 1
	default:
		return 0
	}
}

// Valid reports whether k is one of the defined kinds.
func (k SourceKind) Valid() bool {
	switch k {
	case SourceLocal, SourceManual, SourceAutomation, SourceExternal:
		return true
	}
	return false
}

// Source identifies who sent an action or caused an event.
type Source struct {
	Kind SourceKind `json:"kind"`
	// Name is a human label: the token's name ("phone"), the rule or
	// script name ("evening.sh"), or the system ("pipewire").
	Name string `json:"name,omitempty"`
	// Token is the ID of the token used, never the token itself.
	Token string `json:"token,omitempty"`
	// Module is set when a module sent the action through Core.Do.
	Module string `json:"module,omitempty"`
}

func (s Source) String() string {
	if s.Name == "" {
		return string(s.Kind)
	}
	return fmt.Sprintf("%s (%s)", s.Kind, s.Name)
}

// Status is the outcome of an action that did not fail.
type Status string

const (
	// StatusApplied: the change was made.
	StatusApplied Status = "applied"
	// StatusSkipped: a higher-priority hold is in place; not an error.
	StatusSkipped Status = "skipped"
	// StatusObserved: an outside change hostd recorded (audit trail only).
	StatusObserved Status = "observed"
	// StatusAccepted: a slow action was queued; its outcome arrives later
	// as an event with the action's ID.
	StatusAccepted Status = "accepted"
)

// Result is what a sender gets back for an action that did not fail.
type Result struct {
	Action string `json:"action"`
	Status Status `json:"status"`
	// Version of the changed resource after the action, when it has one.
	Version uint64 `json:"version,omitempty"`

	// Set when Status is skipped: why ("held"), by which source kind, and
	// until when.
	Reason string     `json:"reason,omitempty"`
	HeldBy SourceKind `json:"held_by,omitempty"`
	Until  *time.Time `json:"until,omitempty"`

	// Data is a module-specific payload, such as the started instance ID.
	Data json.RawMessage `json:"data,omitempty"`
}

// MustJSON marshals v, which must be marshalable (a map or struct built
// by the caller): a failure is a programming error, so it panics.
func MustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("sdk.MustJSON: %v", err))
	}
	return b
}

// EventLagged tells a subscriber that it fell behind and missed events.
const EventLagged = "bus.lagged"

// Event is a notice that something changed, emitted by a module (or by the
// core for action.done and action.skipped).
type Event struct {
	ID   string          `json:"id"`
	Type string          `json:"type"`
	Time time.Time       `json:"time"`
	Data json.RawMessage `json:"data,omitempty"`

	// Action that caused the event, and its source; empty for outside
	// changes, which carry Source{Kind: external} instead.
	Action string  `json:"action,omitempty"`
	Source *Source `json:"source,omitempty"`

	// Resource whose state changed, and its version afterwards.
	Resource string `json:"resource,omitempty"`
	Version  uint64 `json:"version,omitempty"`
}
