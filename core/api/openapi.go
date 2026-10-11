package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/davitizhgenti/hostd/core/store"
	"github.com/davitizhgenti/hostd/internal/version"
	"github.com/davitizhgenti/hostd/sdk"
)

// The OpenAPI description is generated from the same manifests that make
// the routes, so it cannot drift from them: the core's fixed routes, one
// route per action that declares one, every read and every hook.

type obj = map[string]any

// OpenAPI returns the OpenAPI 3.1 description of hostd's API with these
// modules.
func OpenAPI(manifests []sdk.Manifest, version string) obj {
	paths := obj{}
	add := func(method, path string, op obj) {
		item, _ := paths[path].(obj)
		if item == nil {
			item = obj{}
			paths[path] = item
		}
		if ps := pathParams(path); len(ps) > 0 {
			var params []obj
			for _, p := range ps {
				params = append(params, obj{"name": p, "in": "path", "required": true, "schema": obj{"type": "string"}})
			}
			op["parameters"] = append(params, paramsOf(op)...)
		}
		item[strings.ToLower(method)] = op
	}
	for _, r := range coreRoutes() {
		add(r.method, r.path, r.op)
	}

	var tags []obj
	for _, m := range manifests {
		tags = append(tags, obj{"name": m.Name, "description": "Module " + m.Name + " " + m.Version})
		for _, a := range m.Actions {
			if a.Route == nil {
				continue
			}
			desc := a.Description
			if a.Slow {
				desc += " Slow: answers 202 at once; the outcome follows as an event."
			}
			op := obj{
				"operationId": a.Type, "tags": []string{m.Name}, "summary": a.Description, "description": desc,
				"x-hostd-action": a.Type, "x-hostd-scope": a.Scope,
				"security":  []obj{{"bearer": []string{}}},
				"responses": actionResponses(a.Slow),
				"parameters": []obj{{"name": "expect_version", "in": "query", "required": false,
					"description": "refuse with precondition_failed if the resource changed since this version",
					"schema":      obj{"type": "integer", "minimum": 0}}},
			}
			if len(a.ArgScopes) > 0 {
				op["x-hostd-arg-scopes"] = a.ArgScopes
			}
			if body := bodySchema(a.Schema, pathParams(a.Route.Path)); body != nil {
				op["requestBody"] = obj{"required": false, "content": obj{"application/json": obj{"schema": body}}}
			}
			add(a.Route.Method, a.Route.Path, op)
		}
		for _, rd := range m.Reads {
			add(http.MethodGet, rd.Path, obj{
				"operationId": m.Name + "." + rd.Name, "tags": []string{m.Name}, "summary": rd.Description,
				"x-hostd-scope": sdk.ScopeRead, "security": []obj{{"bearer": []string{}}},
				"responses": withErrors(obj{"200": obj{"description": "The module's answer",
					"content": obj{"application/json": obj{"schema": obj{}}}}}),
			})
		}
		for _, h := range m.Hooks {
			add(http.MethodPost, h.Path, obj{
				"operationId": m.Name + ".hook." + h.Name, "tags": []string{m.Name}, "summary": h.Description,
				"description": "No token: the module authenticates the request itself (a signature).",
				"security":    []obj{},
				"requestBody": obj{"required": true, "content": obj{"application/json": obj{"schema": obj{}}}},
				"responses": withErrors(obj{"200": obj{"description": "Handled",
					"content": obj{"application/json": obj{"schema": obj{}}}}}),
			})
		}
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i]["name"].(string) < tags[j]["name"].(string) })
	tags = append([]obj{{"name": "core", "description": "The core: actions, events, audit, tokens, updates"}}, tags...)

	return obj{
		"openapi": "3.1.0",
		"info": obj{
			"title":       "hostd",
			"version":     version,
			"description": "Every change is an action: validated, authorized, ordered per resource, held by priority, and audited. Reads are plain GETs; events stream over a WebSocket.",
			"license":     obj{"name": "Apache-2.0", "identifier": "Apache-2.0"},
		},
		"servers":  []obj{{"url": "/", "description": "the unix socket, or the machine's address on the home network (port 7300)"}},
		"tags":     tags,
		"security": []obj{{"bearer": []string{}}},
		"paths":    paths,
		"components": obj{
			"responses": errorResponses,
			"securitySchemes": obj{"bearer": obj{"type": "http", "scheme": "bearer",
				"description": "A token from hostctl token create (hostd_...)"}},
			"schemas": obj{
				"Error": obj{"type": "object", "required": []string{"error"}, "properties": obj{"error": obj{
					"type": "object", "required": []string{"code", "message"}, "properties": obj{
						"code": obj{"type": "string", "enum": []string{
							string(sdk.CodeInvalidArgs), string(sdk.CodeNotFound), string(sdk.CodeInstanceNotRunning),
							string(sdk.CodePreconditionFailed), string(sdk.CodeForbidden), string(sdk.CodeTimeout),
							string(sdk.CodeModuleUnavailable), string(sdk.CodeLoopDetected), string(sdk.CodeUnauthorized),
							string(sdk.CodeInternal)}},
						"message": obj{"type": "string"},
						"action":  obj{"type": "string"},
					}}}},
				"Result": obj{"type": "object", "required": []string{"action", "status"}, "properties": obj{
					"action":  obj{"type": "string", "description": "the action's ID"},
					"status":  obj{"type": "string", "enum": []string{"applied", "skipped", "accepted"}},
					"version": obj{"type": "integer", "description": "the resource's version after the action"},
					"reason":  obj{"type": "string", "description": "skipped: why (held)"},
					"held_by": obj{"type": "string", "description": "skipped: the source kind holding the resource"},
					"until":   obj{"type": "string", "format": "date-time", "description": "skipped: when the hold ends"},
					"data":    obj{"description": "the module's payload"},
				}},
			},
		},
	}
}

func paramsOf(op obj) []obj {
	ps, _ := op["parameters"].([]obj)
	return ps
}

// bodySchema is an action's argument schema without the path parameters.
func bodySchema(schema json.RawMessage, pathArgs []string) obj {
	if len(schema) == 0 {
		return nil
	}
	var s obj
	if json.Unmarshal(schema, &s) != nil {
		return nil
	}
	if len(pathArgs) == 0 {
		return s
	}
	if props, ok := s["properties"].(map[string]any); ok {
		for _, p := range pathArgs {
			delete(props, p)
		}
		if len(props) == 0 {
			return nil
		}
	}
	if req, ok := s["required"].([]any); ok {
		var keep []any
		for _, r := range req {
			if !contains(pathArgs, r) {
				keep = append(keep, r)
			}
		}
		if len(keep) == 0 {
			delete(s, "required")
		} else {
			s["required"] = keep
		}
	}
	return s
}

func contains(list []string, v any) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func ref(name string) obj { return obj{"$ref": "#/components/schemas/" + name} }

func errorResponse(desc string) obj {
	return obj{"description": desc, "content": obj{"application/json": obj{"schema": ref("Error")}}}
}

// errorResponses are the shared error answers, by name.
var errorResponses = obj{
	"InvalidArgs":        errorResponse("invalid_args: the arguments do not fit the schema"),
	"Unauthorized":       errorResponse("unauthorized: no token, or an invalid, expired or revoked one"),
	"Forbidden":          errorResponse("forbidden: the token lacks the scope"),
	"NotFound":           errorResponse("not_found"),
	"Conflict":           errorResponse("instance_not_running, or loop_detected"),
	"PreconditionFailed": errorResponse("precondition_failed: expect_version did not match"),
	"Unavailable":        errorResponse("module_unavailable: the owning module is not running"),
	"Timeout":            errorResponse("timeout: the outcome follows as an event"),
}

func respRef(name string) obj { return obj{"$ref": "#/components/responses/" + name} }

// withErrors adds the error answers every authenticated route can give.
func withErrors(r obj) obj {
	r["400"] = respRef("InvalidArgs")
	r["401"] = respRef("Unauthorized")
	r["403"] = respRef("Forbidden")
	r["404"] = respRef("NotFound")
	r["503"] = respRef("Unavailable")
	return r
}

func actionResponses(slow bool) obj {
	r := obj{"200": obj{"description": "applied, or skipped (a higher-priority hold; not an error)",
		"content": obj{"application/json": obj{"schema": ref("Result")}}}}
	if slow {
		r["202"] = obj{"description": "accepted: the outcome follows as an event with the action's ID",
			"content": obj{"application/json": obj{"schema": ref("Result")}}}
	}
	r = withErrors(r)
	r["409"] = respRef("Conflict")
	r["412"] = respRef("PreconditionFailed")
	r["504"] = respRef("Timeout")
	return r
}

type coreRoute struct {
	method, path string
	op           obj
}

func coreRoutes() []coreRoute {
	json200 := func(desc string, schema obj) obj {
		return withErrors(obj{"200": obj{"description": desc, "content": obj{"application/json": obj{"schema": schema}}}})
	}
	op := func(id, summary string, responses obj) obj {
		return obj{"operationId": id, "tags": []string{"core"}, "summary": summary, "responses": responses}
	}
	actions := op("core.actions.submit", "Send any action (the generic form of every action route)",
		actionResponses(true))
	actions["requestBody"] = obj{"required": true, "content": obj{"application/json": obj{"schema": obj{
		"type": "object", "required": []string{"type"}, "properties": obj{
			"type":           obj{"type": "string", "description": "the action type, e.g. audio.volume.set"},
			"args":           obj{"type": "object", "description": "its arguments (the action's schema)"},
			"expect_version": obj{"type": "integer"},
			"cause":          obj{"type": "string", "description": "the event this action reacts to"},
		}}}}}
	audit := op("core.actions.audit", "The audit trail, newest first", json200("Audit entries", obj{"type": "array"}))
	audit["parameters"] = []obj{
		{"name": "limit", "in": "query", "schema": obj{"type": "integer"}},
		{"name": "before", "in": "query", "schema": obj{"type": "integer"}, "description": "entries before this sequence number"},
	}
	events := op("core.events", "Events as a WebSocket stream (?since=<event id> replays what came after it)",
		withErrors(obj{"101": obj{"description": "switching to the WebSocket"}}))
	events["parameters"] = []obj{
		{"name": "type", "in": "query", "schema": obj{"type": "string"}, "description": "an event type or namespace (audio.*)"},
		{"name": "since", "in": "query", "schema": obj{"type": "string"}, "description": "replay events after this one"},
	}
	update := op("core.update", "Upload a new hostd binary: staged, switched to, rolled back if it does not start",
		json200("The staged update", obj{"type": "object"}))
	update["requestBody"] = obj{"required": true, "content": obj{"application/octet-stream": obj{"schema": obj{"type": "string", "format": "binary"}}}}
	createToken := op("core.tokens.create", "Create a token (its secret is shown only now)", json200("The token, with its secret", obj{"type": "object"}))
	createToken["requestBody"] = obj{"required": true, "content": obj{"application/json": obj{"schema": obj{
		"type": "object", "required": []string{"name"}, "properties": obj{
			"name": obj{"type": "string"}, "scopes": obj{"type": "array", "items": obj{"type": "string"}},
			"kind": obj{"type": "string", "enum": []string{"manual", "automation", "local"}},
			"ttl":  obj{"type": "string", "description": "e.g. 720h; empty: no expiry"},
		}}}}}
	return []coreRoute{
		{"GET", "/v1/version", op("core.version", "hostd's version", json200("Version", obj{"type": "object"}))},
		{"GET", "/v1/manifests", op("core.manifests", "Every module's manifest: its actions, events, reads and scopes", json200("Manifests", obj{"type": "array"}))},
		{"GET", "/v1/openapi.json", op("core.openapi", "This description", json200("OpenAPI 3.1", obj{"type": "object"}))},
		{"GET", "/v1/state/{module}", op("core.state", "A module's whole state", json200("State", obj{"type": "object"}))},
		{"POST", "/v1/actions", actions},
		{"GET", "/v1/actions", audit},
		{"GET", "/v1/actions/running", op("core.actions.running", "The actions being handled now, oldest first", json200("Running actions", obj{"type": "array"}))},
		{"GET", "/v1/events", events},
		{"GET", "/v1/tokens", op("core.tokens.list", "The tokens (never their secrets)", json200("Tokens", obj{"type": "array"}))},
		{"POST", "/v1/tokens", createToken},
		{"DELETE", "/v1/tokens/{id}", op("core.tokens.revoke", "Revoke a token", withErrors(obj{"204": obj{"description": "revoked"}}))},
		{"POST", "/v1/update", update},
	}
}

func (s *Server) getOpenAPI(w http.ResponseWriter, _ *http.Request, _ store.Token) error {
	writeJSON(w, http.StatusOK, OpenAPI(s.engine.Registry().Manifests(), version.Version))
	return nil
}
