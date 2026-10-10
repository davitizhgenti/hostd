package sdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Scopes every token system knows, defined by the core rather than modules.
const (
	ScopeRead  = "read"  // all GET endpoints and the event stream
	ScopeAdmin = "admin" // tokens, config reload, everything else
)

// Manifest describes a module. The core generates API routes, CLI commands,
// token scopes and OpenAPI entries from it.
type Manifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`

	// Owns lists the namespaces this module owns, as type patterns
	// ("audio.*", or an exact type). Every action and event type the module
	// declares must match one; no two modules may overlap.
	Owns []string `json:"owns"`

	// Requires lists modules that must start before this one.
	Requires []string `json:"requires,omitempty"`

	// Scopes this module defines for tokens, besides the core's read and
	// admin.
	Scopes []ScopeSpec `json:"scopes,omitempty"`

	Actions []ActionSpec `json:"actions,omitempty"`
	Events  []EventSpec  `json:"events,omitempty"`

	// Reads are GET routes serving parts of the module's state, such as
	// the app catalog. The module implements Reader to answer them.
	Reads []ReadSpec `json:"reads,omitempty"`

	// Hooks are POST routes for systems that cannot hold a token, such as
	// a git host's webhook. No token is checked: the module implements
	// Hooker and must authenticate every request itself (a signature).
	Hooks []HookSpec `json:"hooks,omitempty"`
}

// HookSpec declares a hook route; its path starts with /v1/hooks/.
type HookSpec struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Path        string `json:"path"` // e.g. /v1/hooks/git/{app}
}

// ReadSpec declares a read-only route. Path parameters and query
// parameters reach Reader.Read; reading needs the read scope.
type ReadSpec struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Path        string `json:"path"` // e.g. /v1/apps/{id}
}

// ScopeSpec is a token scope a module defines.
type ScopeSpec struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ActionSpec declares one action a module handles.
type ActionSpec struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`

	// Schema is the JSON Schema of the arguments; nil means none.
	Schema json.RawMessage `json:"schema,omitempty"`

	// Keys are the resource keys the action locks, as templates over its
	// arguments. Actions on the same key run one at a time, in order.
	Keys []KeyTemplate `json:"keys,omitempty"`

	// Scope a token needs to send this action. ArgScopes adds scopes
	// needed when a boolean argument is true, such as
	// {"front": "display.front"}.
	Scope     string            `json:"scope"`
	ArgScopes map[string]string `json:"arg_scopes,omitempty"`

	// Secret names arguments whose values are never recorded (the audit
	// trail shows "[secret]"); hostctl reads them from standard input.
	Secret []string `json:"secret,omitempty"`

	// Timeout for Handle; zero means the core's default.
	Timeout Duration `json:"timeout,omitempty"`

	// Slow actions return 202 Accepted at once; the outcome follows as an
	// event with the action ID.
	Slow bool `json:"slow,omitempty"`

	// Route is the friendly API route for this action, if any. Path
	// parameters are merged into the arguments.
	Route *Route `json:"route,omitempty"`
}

// Route is an HTTP route such as POST /v1/apps/{id}/start.
type Route struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// EventSpec declares one event type a module emits.
type EventSpec struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

// Duration is a time.Duration that encodes as a string such as "30s".
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string such as \"30s\"")
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

var (
	reModule  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	reScope   = regexp.MustCompile(`^[a-z][a-z0-9_-]*(\.[a-z][a-z0-9_-]*)*$`)
	reType    = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	rePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*(\.\*)?$`)
	reParam   = regexp.MustCompile(`\{([^}]*)\}`)
)

var methods = map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}

// Validate checks the manifest on its own: names, ownership of every type,
// schemas, key templates and routes. Checks across modules, such as
// overlapping namespaces and whether every scope used is declared
// somewhere, are the core's job. All problems
// are reported together.
func (m *Manifest) Validate() error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if !reModule.MatchString(m.Name) {
		bad("name %q: use lowercase letters, digits and dashes", m.Name)
	}
	if m.Version == "" {
		bad("version is required")
	}

	if len(m.Owns) == 0 {
		bad("owns: a module must own at least one namespace")
	}
	for _, p := range m.Owns {
		if !rePattern.MatchString(p) {
			bad("owns %q: use a type such as \"audio.volume.set\" or a namespace such as \"audio.*\"", p)
		}
	}
	owned := func(typ string) bool {
		for _, p := range m.Owns {
			if MatchType(p, typ) {
				return true
			}
		}
		return false
	}

	for _, r := range m.Requires {
		if !reModule.MatchString(r) {
			bad("requires %q: not a module name", r)
		}
		if r == m.Name {
			bad("requires itself")
		}
	}

	scopes := map[string]bool{ScopeRead: true, ScopeAdmin: true}
	for _, s := range m.Scopes {
		switch {
		case !reScope.MatchString(s.Name):
			bad("scope %q: use lowercase letters, digits and dots", s.Name)
		case s.Name == ScopeRead || s.Name == ScopeAdmin:
			bad("scope %q is defined by the core", s.Name)
		case scopes[s.Name]:
			bad("scope %q declared twice", s.Name)
		}
		scopes[s.Name] = true
	}

	routes := map[string]string{}
	seen := map[string]bool{}
	for _, a := range m.Actions {
		where := fmt.Sprintf("action %q", a.Type)
		switch {
		case !reType.MatchString(a.Type):
			bad("%s: use dotted lowercase names such as \"audio.volume.set\"", where)
		case !owned(a.Type):
			bad("%s: outside the namespaces this module owns %v", where, m.Owns)
		case seen[a.Type]:
			bad("%s declared twice", where)
		}
		seen[a.Type] = true

		// Whether a scope exists is checked by the core across all modules
		// (a module may use another's scope, e.g. display actions need
		// "apps"); here only the form.
		if a.Scope == "" {
			bad("%s: scope is required", where)
		} else if !reScope.MatchString(a.Scope) {
			bad("%s: scope %q is not a scope name", where, a.Scope)
		}
		for arg, scope := range a.ArgScopes {
			if !reScope.MatchString(scope) {
				bad("%s: arg_scopes %q: %q is not a scope name", where, arg, scope)
			}
		}

		schema, err := CompileArgsSchema(a.Schema)
		if err != nil {
			bad("%s: schema: %v", where, err)
			schema = nil
		}
		for arg := range a.ArgScopes {
			if schema != nil && !schema.HasProperty(arg) {
				bad("%s: arg_scopes refers to argument %q, which the schema does not declare", where, arg)
			}
		}
		for _, k := range a.Keys {
			params, err := k.Params()
			if err != nil {
				bad("%s: %v", where, err)
				continue
			}
			for _, p := range params {
				if schema != nil && !schema.HasProperty(p) {
					bad("%s: key %q refers to argument %q, which the schema does not declare", where, k, p)
				}
			}
		}
		if a.Timeout < 0 {
			bad("%s: negative timeout", where)
		}
		if r := a.Route; r != nil {
			switch {
			case !methods[r.Method]:
				bad("%s: route method %q", where, r.Method)
			case !strings.HasPrefix(r.Path, "/v1/"):
				bad("%s: route path %q must start with /v1/", where, r.Path)
			}
			id := r.Method + " " + r.Path
			if prev, dup := routes[id]; dup {
				bad("%s: route %s is already used by %q", where, id, prev)
			}
			routes[id] = a.Type
			for _, p := range reParam.FindAllStringSubmatch(r.Path, -1) {
				if !isIdent(p[1]) {
					bad("%s: route parameter {%s}: use lowercase letters, digits and _", where, p[1])
				} else if schema != nil && !schema.HasProperty(p[1]) {
					bad("%s: route parameter {%s} is not an argument in the schema", where, p[1])
				}
			}
		}
	}

	hookNames := map[string]bool{}
	for _, h := range m.Hooks {
		where := fmt.Sprintf("hook %q", h.Name)
		switch {
		case !isIdent(h.Name):
			bad("%s: use lowercase letters, digits and _", where)
		case hookNames[h.Name]:
			bad("%s declared twice", where)
		case !strings.HasPrefix(h.Path, "/v1/hooks/"):
			bad("%s: path %q must start with /v1/hooks/", where, h.Path)
		}
		hookNames[h.Name] = true
		id := "POST " + h.Path
		if prev, dup := routes[id]; dup {
			bad("%s: route %s is already used by %q", where, id, prev)
		}
		routes[id] = h.Name
	}

	readNames := map[string]bool{}
	for _, rd := range m.Reads {
		where := fmt.Sprintf("read %q", rd.Name)
		switch {
		case !isIdent(rd.Name):
			bad("%s: use lowercase letters, digits and _", where)
		case readNames[rd.Name]:
			bad("%s declared twice", where)
		case !strings.HasPrefix(rd.Path, "/v1/"):
			bad("%s: path %q must start with /v1/", where, rd.Path)
		}
		readNames[rd.Name] = true
		id := "GET " + rd.Path
		if prev, dup := routes[id]; dup {
			bad("%s: route %s is already used by %q", where, id, prev)
		}
		routes[id] = rd.Name
		for _, p := range reParam.FindAllStringSubmatch(rd.Path, -1) {
			if !isIdent(p[1]) {
				bad("%s: path parameter {%s}: use lowercase letters, digits and _", where, p[1])
			}
		}
	}

	seen = map[string]bool{}
	for _, e := range m.Events {
		where := fmt.Sprintf("event %q", e.Type)
		switch {
		case !reType.MatchString(e.Type):
			bad("%s: use dotted lowercase names such as \"audio.volume.changed\"", where)
		case !owned(e.Type):
			bad("%s: outside the namespaces this module owns %v", where, m.Owns)
		case seen[e.Type]:
			bad("%s declared twice", where)
		}
		seen[e.Type] = true
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("module %q: %w", m.Name, errors.Join(errs...))
}

// Action returns the spec of an action type, if declared.
func (m *Manifest) Action(typ string) (ActionSpec, bool) {
	for _, a := range m.Actions {
		if a.Type == typ {
			return a, true
		}
	}
	return ActionSpec{}, false
}
