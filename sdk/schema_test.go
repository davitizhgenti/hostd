package sdk

import (
	"encoding/json"
	"strings"
	"testing"
)

const volumeSchema = `{
	"type": "object",
	"properties": {
		"percent": {"type": "integer", "minimum": 0, "maximum": 150},
		"relative": {"type": "boolean"}
	},
	"required": ["percent"]
}`

func mustCompile(t *testing.T, schema string) *ArgsSchema {
	t.Helper()
	s, err := CompileArgsSchema(json.RawMessage(schema))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return s
}

func TestArgsSchemaValidate(t *testing.T) {
	s := mustCompile(t, volumeSchema)
	for _, tc := range []struct {
		name    string
		args    string
		wantErr string // substring of the message; "" means valid
	}{
		{"valid", `{"percent": 40}`, ""},
		{"valid with optional", `{"percent": 40, "relative": false}`, ""},
		{"lower bound", `{"percent": 0}`, ""},
		{"upper bound", `{"percent": 150}`, ""},
		{"above range", `{"percent": 151}`, "/percent"},
		{"below range", `{"percent": -1}`, "/percent"},
		{"wrong type", `{"percent": "40"}`, "/percent"},
		{"not an integer", `{"percent": 40.5}`, "/percent"},
		{"missing required", `{}`, "percent"},
		{"empty args are an empty object", ``, "percent"},
		{"unknown field rejected by default", `{"percent": 40, "pecrent": 41}`, "pecrent"},
		{"not an object", `[40]`, "must be a JSON object"},
		{"not JSON", `{percent: 40}`, "not valid JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := s.Validate(json.RawMessage(tc.args))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if CodeOf(err) != CodeInvalidArgs {
				t.Fatalf("err = %v, want invalid_args", err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestArgsSchemaListsEveryProblem(t *testing.T) {
	s := mustCompile(t, volumeSchema)
	err := s.Validate(json.RawMessage(`{"percent": 999, "relative": "yes"}`))
	if err == nil || !strings.Contains(err.Error(), "/percent") || !strings.Contains(err.Error(), "/relative") {
		t.Fatalf("want both problems reported, got %v", err)
	}
}

func TestArgsSchemaNilMeansNoArgs(t *testing.T) {
	s, err := CompileArgsSchema(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, ok := range []string{``, `{}`, ` {} `} {
		if err := s.Validate(json.RawMessage(ok)); err != nil {
			t.Errorf("Validate(%q) = %v", ok, err)
		}
	}
	if err := s.Validate(json.RawMessage(`{"x": 1}`)); CodeOf(err) != CodeInvalidArgs {
		t.Errorf("arguments to a no-argument action: err = %v", err)
	}
}

func TestArgsSchemaExplicitAdditionalPropertiesKept(t *testing.T) {
	s := mustCompile(t, `{"type": "object", "additionalProperties": true}`)
	if err := s.Validate(json.RawMessage(`{"anything": 1}`)); err != nil {
		t.Fatalf("explicit additionalProperties: true should allow extra fields: %v", err)
	}
}

func TestArgsSchemaHasProperty(t *testing.T) {
	s := mustCompile(t, volumeSchema)
	if !s.HasProperty("percent") || s.HasProperty("id") {
		t.Fatal("HasProperty wrong for declared schema")
	}
	open := mustCompile(t, `{"type": "object", "additionalProperties": true}`)
	if !open.HasProperty("whatever") {
		t.Fatal("schema without properties should accept any name")
	}
}

func TestArgsSchemaCompileErrors(t *testing.T) {
	for name, schema := range map[string]string{
		"not JSON":          `{"type": `,
		"bad keyword value": `{"type": "object", "minProperties": "two"}`,
		"external $ref":     `{"$ref": "https://example.com/schema.json"}`,
		"file $ref":         `{"$ref": "file:///etc/passwd"}`,
	} {
		if _, err := CompileArgsSchema(json.RawMessage(schema)); err == nil {
			t.Errorf("%s: compiled without error", name)
		}
	}
}
