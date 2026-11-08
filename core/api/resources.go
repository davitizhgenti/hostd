package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/core/resources"
	"github.com/davitizhgenti/hostd/core/store"
	"github.com/davitizhgenti/hostd/sdk"
)

// Resources is the store of declared things, as the API uses it
// (core/resources.Store).
type Resources interface {
	List(ctx context.Context, kind string, params map[string]string) ([]resources.Item, error)
	Get(ctx context.Context, kind, name string) (resources.Item, error)
	Raw(kind, name string) ([]byte, error)
	Put(ctx context.Context, kind, name string, body []byte, check bool) (resources.Item, error)
	Delete(ctx context.Context, kind, name string) error
}

// The routes of a kind of resource, the same for every kind:
//
//	GET    /v1/<plural>               every resource, with its status
//	GET    /v1/<plural>/{name}        one
//	GET    /v1/<plural>/{name}/file   the file as written (TOML)
//	PUT    /v1/<plural>/{name}        create or replace; TOML or JSON body; ?check=1 only checks
//	DELETE /v1/<plural>/{name}
//	POST   /v1/<plural>/{name}/<verb> the kind's verbs
func (s *Server) kindRoutes(k core.KindOwner) error {
	base := "/v1/" + k.Plural
	write := k.WriteScope
	if write == "" {
		write = sdk.ScopeAdmin
	}
	type route struct {
		pattern, scope string
		h              func(http.ResponseWriter, *http.Request, store.Token) error
	}
	routes := []route{
		{"GET " + base, sdk.ScopeRead, func(w http.ResponseWriter, r *http.Request, _ store.Token) error {
			params := map[string]string{}
			for name, vals := range r.URL.Query() {
				params[name] = vals[len(vals)-1]
			}
			items, err := s.res.List(r.Context(), k.Kind, params)
			if err != nil {
				return err
			}
			if items == nil {
				items = []resources.Item{}
			}
			writeJSON(w, http.StatusOK, items)
			return nil
		}},
		{"GET " + base + "/{name}", sdk.ScopeRead, func(w http.ResponseWriter, r *http.Request, _ store.Token) error {
			it, err := s.res.Get(r.Context(), k.Kind, r.PathValue("name"))
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusOK, it)
			return nil
		}},
		{"GET " + base + "/{name}/file", sdk.ScopeRead, func(w http.ResponseWriter, r *http.Request, _ store.Token) error {
			b, err := s.res.Raw(k.Kind, r.PathValue("name"))
			if err != nil {
				return err
			}
			w.Header().Set("Content-Type", "application/toml; charset=utf-8")
			_, _ = w.Write(b)
			return nil
		}},
		{"PUT " + base + "/{name}", write, func(w http.ResponseWriter, r *http.Request, _ store.Token) error {
			body, err := readBody(r)
			if err != nil {
				return err
			}
			check := r.URL.Query().Get("check") != ""
			it, err := s.res.Put(r.Context(), k.Kind, r.PathValue("name"), body, check)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusOK, it)
			return nil
		}},
		{"DELETE " + base + "/{name}", write, func(w http.ResponseWriter, r *http.Request, _ store.Token) error {
			if err := s.res.Delete(r.Context(), k.Kind, r.PathValue("name")); err != nil {
				return err
			}
			w.WriteHeader(http.StatusNoContent)
			return nil
		}},
	}
	for _, v := range k.Verbs {
		spec := v
		routes = append(routes, route{"POST " + base + "/{name}/" + v.Name, "", s.verb(spec)})
	}
	for _, r := range routes {
		if err := s.handle(r.pattern, r.scope, r.h); err != nil {
			return fmt.Errorf("kind %q: %w", k.Kind, err)
		}
	}
	return nil
}

// verb serves a kind's verb: the action it names, with the resource's name
// as the argument it takes and the JSON body (optional) as the rest.
func (s *Server) verb(v sdk.VerbSpec) func(http.ResponseWriter, *http.Request, store.Token) error {
	arg := v.Arg
	if arg == "" {
		arg = "name"
	}
	return func(w http.ResponseWriter, r *http.Request, tok store.Token) error {
		args := map[string]json.RawMessage{}
		body, err := readBody(r)
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(body)) > 0 {
			if err := json.Unmarshal(body, &args); err != nil {
				return sdk.Errorf(sdk.CodeInvalidArgs, "body must be a JSON object of arguments")
			}
		}
		name, _ := json.Marshal(r.PathValue("name"))
		if prev, ok := args[arg]; ok && !bytes.Equal(bytes.TrimSpace(prev), name) {
			return sdk.Errorf(sdk.CodeInvalidArgs, "argument %q is in both the path and the body, with different values", arg)
		}
		args[arg] = name
		raw, _ := json.Marshal(args)
		return s.submit(w, r, tok, sdk.Action{Type: v.Action, Args: raw})
	}
}

// kindOpenAPI describes a kind's routes (see kindRoutes) for the OpenAPI
// document.
func kindOpenAPI(module string, k sdk.KindSpec, add func(method, path string, op obj)) {
	base := "/v1/" + k.Plural
	write := k.WriteScope
	if write == "" {
		write = sdk.ScopeAdmin
	}
	bearer := []obj{{"bearer": []string{}}}
	spec := obj{}
	if k.Schema != nil {
		_ = json.Unmarshal(k.Schema, &spec)
	}
	item := obj{"type": "object", "properties": obj{
		"kind": obj{"type": "string"}, "name": obj{"type": "string"}, "file": obj{"type": "string"},
		"spec": spec, "generation": obj{"type": "integer"},
		"status": obj{"type": "object", "description": "Conditions (Ready...) and the kind's own data"},
		"error":  obj{"type": "string", "description": "why the file on disk is not in use; spec is then the version that is"},
	}}
	ok := func(schema obj) obj {
		return withErrors(obj{"200": obj{"description": "The resource", "content": obj{"application/json": obj{"schema": schema}}}})
	}
	tag := []string{module}
	add("GET", base, obj{"operationId": "list." + k.Plural, "tags": tag, "summary": "List " + k.Plural, "description": k.Description,
		"x-hostd-scope": sdk.ScopeRead, "security": bearer,
		"responses": ok(obj{"type": "array", "items": item})})
	add("GET", base+"/{name}", obj{"operationId": "get." + k.Kind, "tags": tag, "summary": "One " + k.Kind,
		"x-hostd-scope": sdk.ScopeRead, "security": bearer, "responses": ok(item)})
	add("GET", base+"/{name}/file", obj{"operationId": "file." + k.Kind, "tags": tag, "summary": "The " + k.Kind + "'s file as written",
		"x-hostd-scope": sdk.ScopeRead, "security": bearer,
		"responses": withErrors(obj{"200": obj{"description": "TOML", "content": obj{"application/toml": obj{"schema": obj{"type": "string"}}}}})})
	add("PUT", base+"/{name}", obj{"operationId": "apply." + k.Kind, "tags": tag,
		"summary":       "Create or replace a " + k.Kind,
		"description":   "The body is TOML, or a JSON object. A resource the module rejects is not written and the previous one stays. With ?check=1 nothing is applied or written.",
		"x-hostd-scope": write, "security": bearer,
		"parameters": []obj{{"name": "check", "in": "query", "required": false, "schema": obj{"type": "string"},
			"description": "only check that the resource would be accepted"}},
		"requestBody": obj{"required": true, "content": obj{
			"application/toml": obj{"schema": obj{"type": "string"}},
			"application/json": obj{"schema": spec}}},
		"responses": ok(item)})
	add("DELETE", base+"/{name}", obj{"operationId": "delete." + k.Kind, "tags": tag, "summary": "Delete a " + k.Kind,
		"x-hostd-scope": write, "security": bearer,
		"responses": withErrors(obj{"204": obj{"description": "Deleted"}})})
	for _, v := range k.Verbs {
		add("POST", base+"/{name}/"+v.Name, obj{"operationId": k.Kind + "." + v.Name, "tags": tag, "summary": v.Description,
			"x-hostd-action": v.Action, "security": bearer, "responses": actionResponses(false)})
	}
}
