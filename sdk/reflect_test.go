package sdk

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type reflectArgs struct {
	Lamp       string            `json:"lamp" desc:"lamp name" pattern:"^[a-z]+$" max:"20"`
	Brightness int               `json:"brightness" desc:"0-100" min:"0" max:"100"`
	Mode       string            `json:"mode,omitempty" enum:"day,night"`
	Fade       Duration          `json:"fade,omitempty"`
	Tags       []string          `json:"tags,omitempty" max:"3"`
	Env        map[string]string `json:"env,omitempty"`
	Body       string            `json:"body,omitempty" media:"text/plain"`
	Nested     *struct {
		On bool `json:"on"`
	} `json:"nested,omitempty"`
	Any     json.RawMessage `json:"any,omitempty"`
	Skipped string          `json:"-"`
	other   string
	At      time.Time `json:"at,omitzero"`
}

func TestSchemaOf(t *testing.T) {
	_ = reflectArgs{other: ""} // unexported: not a property, whatever its tag says
	raw := SchemaOf[reflectArgs]()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, raw)
	}
	if got["type"] != "object" || got["additionalProperties"] != false {
		t.Fatalf("root: %s", raw)
	}
	if req, _ := json.Marshal(got["required"]); string(req) != `["lamp","brightness"]` {
		t.Fatalf("required = %s", req)
	}
	// Properties keep the struct's order: hostctl takes positionals in it.
	if i, j := strings.Index(string(raw), `"lamp"`), strings.Index(string(raw), `"brightness"`); i > j {
		t.Fatalf("properties out of order: %s", raw)
	}
	props := got["properties"].(map[string]any)
	for _, absent := range []string{"Skipped", "other", "-"} {
		if _, ok := props[absent]; ok {
			t.Errorf("%q should not be a property", absent)
		}
	}
	for name, want := range map[string]string{
		"lamp":       `{"description":"lamp name","maxLength":20,"pattern":"^[a-z]+$","type":"string"}`,
		"brightness": `{"description":"0-100","maximum":100,"minimum":0,"type":"integer"}`,
		"mode":       `{"enum":["day","night"],"type":"string"}`,
		"fade":       `{"type":"string"}`,
		"tags":       `{"items":{"type":"string"},"maxItems":3,"type":"array"}`,
		"env":        `{"additionalProperties":{"type":"string"},"type":"object"}`,
		"body":       `{"contentMediaType":"text/plain","type":"string"}`,
		"nested":     `{"additionalProperties":false,"properties":{"on":{"type":"boolean"}},"required":["on"],"type":"object"}`,
		"any":        `{}`,
		"at":         `{"format":"date-time","type":"string"}`,
	} {
		b, _ := json.Marshal(props[name]) // Marshal sorts keys
		if string(b) != want {
			t.Errorf("%s = %s, want %s", name, b, want)
		}
	}
}

func TestSchemaOfValidates(t *testing.T) {
	s, err := CompileArgsSchema(SchemaOf[reflectArgs]())
	if err != nil {
		t.Fatal(err)
	}
	for args, ok := range map[string]bool{
		`{"lamp":"porch","brightness":40}`:                     true,
		`{"lamp":"porch","brightness":40,"mode":"night"}`:      true,
		`{"lamp":"porch","brightness":140}`:                    false,
		`{"lamp":"Porch","brightness":4}`:                      false,
		`{"lamp":"porch","brightness":4,"mode":"dusk"}`:        false,
		`{"lamp":"porch","brightness":4,"tags":[1,2,3,4]}`:     false,
		`{"lamp":"porch","brightness":4,"typo":1}`:             false,
		`{"brightness":4}`:                                     false,
		`{"lamp":"porch","brightness":4,"fade":"30s"}`:         true,
		`{"lamp":"porch","brightness":4,"nested":{"on":true}}`: true,
	} {
		if err := s.Validate(json.RawMessage(args)); (err == nil) != ok {
			t.Errorf("%s: err = %v, want ok = %v", args, err, ok)
		}
	}
}

func TestSchemaOfEmbeddedAndEmpty(t *testing.T) {
	type base struct {
		ID string `json:"id"`
	}
	type withBase struct {
		base
		Front bool `json:"front,omitempty"`
	}
	if got, want := string(SchemaOf[withBase]()), `{"type":"object","properties":{"id":{"type":"string"},"front":{"type":"boolean"}},"required":["id"],"additionalProperties":false}`; got != want {
		t.Errorf("embedded = %s\nwant      %s", got, want)
	}
	if got, want := string(SchemaOf[struct{}]()), `{"type":"object","additionalProperties":false}`; got != want {
		t.Errorf("empty = %s, want %s", got, want)
	}
	if got := string(SchemaOf[[]int]()); got != `{"type":"array","items":{"type":"integer"}}` {
		t.Errorf("slice = %s", got)
	}
}

func TestSchemaOfRefusesWhatItCannotDescribe(t *testing.T) {
	type self struct {
		Next []self `json:"next,omitempty"`
	}
	for name, f := range map[string]func(){
		"chan":      func() { SchemaOf[chan int]() },
		"int keys":  func() { SchemaOf[map[int]string]() },
		"recursive": func() { SchemaOf[self]() },
		"bad bound": func() {
			SchemaOf[struct {
				N int `json:"n" min:"x"`
			}]()
		},
		"bad enum": func() {
			SchemaOf[struct {
				N int `json:"n" enum:"a"`
			}]()
		},
		"duplicates": func() {
			type base struct {
				ID string `json:"id"`
			}
			SchemaOf[struct {
				base
				Other string `json:"id"`
			}]()
		},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			f()
		}()
	}
}
