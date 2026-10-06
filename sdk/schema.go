package sdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// noArgs is the schema of actions that declare none: an empty object.
var noArgs = json.RawMessage(`{"type": "object", "additionalProperties": false}`)

// ArgsSchema validates action arguments against a JSON Schema (draft
// 2020-12).
type ArgsSchema struct {
	schema *jsonschema.Schema
	props  map[string]bool // top-level properties, nil if not declared
}

// CompileArgsSchema compiles an action's argument schema. A nil schema
// means the action takes no arguments. An object schema that does not say
// otherwise rejects unknown properties, so a typo in an argument name is an
// error instead of being silently ignored.
func CompileArgsSchema(raw json.RawMessage) (*ArgsSchema, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = noArgs
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("schema is not valid JSON: %w", err)
	}
	s := &ArgsSchema{}
	if m, ok := doc.(map[string]any); ok {
		_, hasAdditional := m["additionalProperties"]
		_, hasUnevaluated := m["unevaluatedProperties"]
		props, hasProps := m["properties"].(map[string]any)
		if (m["type"] == "object" || hasProps) && !hasAdditional && !hasUnevaluated {
			m["additionalProperties"] = false
		}
		if hasProps {
			s.props = make(map[string]bool, len(props))
			for name := range props {
				s.props[name] = true
			}
		}
	}

	const url = "hostd:///args.json"
	c := jsonschema.NewCompiler()
	c.UseLoader(noLoader{}) // never fetch $ref targets from disk or network
	if err := c.AddResource(url, doc); err != nil {
		return nil, err
	}
	if s.schema, err = c.Compile(url); err != nil {
		return nil, err
	}
	return s, nil
}

// HasProperty reports whether the schema declares a top-level property.
// Schemas without a properties list report true for any name.
func (s *ArgsSchema) HasProperty(name string) bool {
	return s.props == nil || s.props[name]
}

// Validate checks args, returning an invalid_args *Error that lists every
// problem. Empty args count as an empty object.
func (s *ArgsSchema) Validate(args json.RawMessage) error {
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage("{}")
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(args))
	if err != nil {
		return Errorf(CodeInvalidArgs, "arguments are not valid JSON")
	}
	if _, ok := inst.(map[string]any); !ok {
		return Errorf(CodeInvalidArgs, "arguments must be a JSON object")
	}
	err = s.schema.Validate(inst)
	if err == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return Errorf(CodeInvalidArgs, "%v", err)
	}
	return Errorf(CodeInvalidArgs, "%s", strings.Join(problems(ve), "; "))
}

var printer = message.NewPrinter(language.English)

// problems flattens a validation error into one line per failing value,
// such as "/percent: maximum: got 200, want 150".
func problems(ve *jsonschema.ValidationError) []string {
	var out []string
	var walk func(*jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			out = append(out, "/"+strings.Join(e.InstanceLocation, "/")+": "+e.ErrorKind.LocalizedString(printer))
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	sort.Strings(out)
	return out
}

type noLoader struct{}

func (noLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("schema references %s; external references are not allowed", url)
}
