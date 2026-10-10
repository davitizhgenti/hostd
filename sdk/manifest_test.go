package sdk

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// validManifest is a small but complete manifest, shaped like the audio
// module's.
func validManifest() Manifest {
	return Manifest{
		Name:    "audio",
		Version: "0.1.0",
		Owns:    []string{"audio.*", "media.*"},
		Scopes:  []ScopeSpec{{Name: "audio", Description: "Volume, outputs, media"}},
		Actions: []ActionSpec{
			{
				Type:    "audio.volume.set",
				Schema:  json.RawMessage(volumeSchema),
				Keys:    []KeyTemplate{"audio.master"},
				Scope:   "audio",
				Timeout: Duration(5 * time.Second),
				Route:   &Route{Method: "POST", Path: "/v1/audio/volume"},
			},
			{
				Type:   "audio.app.volume.set",
				Schema: json.RawMessage(`{"type":"object","properties":{"instance":{"type":"string"},"percent":{"type":"integer"}},"required":["instance","percent"]}`),
				Keys:   []KeyTemplate{"audio.stream:{instance}"},
				Scope:  "audio",
				Route:  &Route{Method: "POST", Path: "/v1/audio/apps/{instance}/volume"},
			},
			{Type: "media.pause", Scope: "audio", Route: &Route{Method: "POST", Path: "/v1/media/pause"}},
		},
		Events: []EventSpec{{Type: "audio.volume.changed"}, {Type: "media.changed"}},
	}
}

func TestManifestValid(t *testing.T) {
	m := validManifest()
	m.Reads = []ReadSpec{{Name: "outputs", Path: "/v1/audio/outputs"}, {Name: "output", Path: "/v1/audio/outputs/{name}"}}
	if err := m.Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	if a, ok := m.Action("media.pause"); !ok || a.Scope != "audio" {
		t.Fatalf("Action lookup failed: %+v %v", a, ok)
	}
	if _, ok := m.Action("media.play"); ok {
		t.Fatal("Action found an undeclared type")
	}
}

func TestManifestInvalid(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Manifest)
		want   string
	}{
		{"bad name", func(m *Manifest) { m.Name = "Audio" }, `name "Audio"`},
		{"no version", func(m *Manifest) { m.Version = "" }, "version is required"},
		{"owns nothing", func(m *Manifest) { m.Owns = nil }, "at least one namespace"},
		{"owns everything", func(m *Manifest) { m.Owns = []string{"*"} }, `owns "*"`},
		{"bad owns pattern", func(m *Manifest) { m.Owns = append(m.Owns, "audio.*.set") }, `owns "audio.*.set"`},
		{"requires itself", func(m *Manifest) { m.Requires = []string{"audio"} }, "requires itself"},
		{"redefines core scope", func(m *Manifest) { m.Scopes = append(m.Scopes, ScopeSpec{Name: "admin"}) }, "defined by the core"},
		{"duplicate scope", func(m *Manifest) { m.Scopes = append(m.Scopes, ScopeSpec{Name: "audio"}) }, "declared twice"},
		{"action outside namespace", func(m *Manifest) { m.Actions[2].Type = "window.focus" }, "outside the namespaces"},
		{"one-segment action type", func(m *Manifest) { m.Actions[2].Type = "audio" }, "dotted lowercase"},
		{"duplicate action", func(m *Manifest) { m.Actions[2] = m.Actions[0]; m.Actions[2].Route = nil }, "declared twice"},
		{"missing scope", func(m *Manifest) { m.Actions[0].Scope = "" }, "scope is required"},
		{"bad scope name", func(m *Manifest) { m.Actions[0].Scope = "Sound!" }, `scope "Sound!" is not a scope name`},
		{"bad arg scope name", func(m *Manifest) { m.Actions[0].ArgScopes = map[string]string{"relative": "A B"} }, `"A B" is not a scope name`},
		{"arg scope on unknown arg", func(m *Manifest) { m.Actions[0].ArgScopes = map[string]string{"front": "audio"} }, `argument "front"`},
		{"broken schema", func(m *Manifest) { m.Actions[0].Schema = json.RawMessage(`{"type": 5}`) }, "schema"},
		{"key on unknown arg", func(m *Manifest) { m.Actions[1].Keys = []KeyTemplate{"audio.stream:{id}"} }, `argument "id"`},
		{"broken key", func(m *Manifest) { m.Actions[1].Keys = []KeyTemplate{"audio.stream:{instance"} }, "unbalanced"},
		{"negative timeout", func(m *Manifest) { m.Actions[0].Timeout = -1 }, "negative timeout"},
		{"bad method", func(m *Manifest) { m.Actions[0].Route.Method = "post" }, "route method"},
		{"path outside /v1", func(m *Manifest) { m.Actions[0].Route.Path = "/audio/volume" }, "must start with /v1/"},
		{"route param not in schema", func(m *Manifest) { m.Actions[1].Route.Path = "/v1/audio/apps/{app}/volume" }, "{app}"},
		{"duplicate route", func(m *Manifest) { m.Actions[2].Route.Path = "/v1/audio/volume" }, "already used"},
		{"read with bad name", func(m *Manifest) { m.Reads = []ReadSpec{{Name: "Apps", Path: "/v1/x"}} }, `read "Apps"`},
		{"read outside /v1", func(m *Manifest) { m.Reads = []ReadSpec{{Name: "x", Path: "/x"}} }, "must start with /v1/"},
		{"hook outside /v1/hooks", func(m *Manifest) { m.Hooks = []HookSpec{{Name: "git", Path: "/v1/git"}} }, "must start with /v1/hooks/"},
		{"hook twice", func(m *Manifest) {
			m.Hooks = []HookSpec{{Name: "git", Path: "/v1/hooks/a"}, {Name: "git", Path: "/v1/hooks/b"}}
		}, "declared twice"},
		{"read twice", func(m *Manifest) { m.Reads = []ReadSpec{{Name: "x", Path: "/v1/x"}, {Name: "x", Path: "/v1/y"}} }, "declared twice"},
		{"read route taken", func(m *Manifest) {
			m.Reads = []ReadSpec{{Name: "x", Path: "/v1/same"}, {Name: "y", Path: "/v1/same"}}
		}, "already used"},
		{"read bad param", func(m *Manifest) { m.Reads = []ReadSpec{{Name: "x", Path: "/v1/x/{ID}"}} }, "path parameter {ID}"},
		{"event outside namespace", func(m *Manifest) { m.Events[0].Type = "display.idle" }, "outside the namespaces"},
		{"duplicate event", func(m *Manifest) { m.Events[1] = m.Events[0] }, "declared twice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := validManifest()
			tc.mutate(&m)
			err := m.Validate()
			if err == nil {
				t.Fatal("invalid manifest accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestManifestReportsAllProblems(t *testing.T) {
	m := validManifest()
	m.Version = ""
	m.Actions[0].Scope = ""
	m.Events[0].Type = "display.idle"
	err := m.Validate()
	if err == nil {
		t.Fatal("accepted")
	}
	for _, want := range []string{"version is required", "scope is required", "display.idle"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
}

func TestDurationJSON(t *testing.T) {
	b, err := json.Marshal(Duration(90 * time.Second))
	if err != nil || string(b) != `"1m30s"` {
		t.Fatalf("marshal = %s, %v", b, err)
	}
	var d Duration
	if err := json.Unmarshal([]byte(`"250ms"`), &d); err != nil || time.Duration(d) != 250*time.Millisecond {
		t.Fatalf("unmarshal = %v, %v", time.Duration(d), err)
	}
	for _, bad := range []string{`30`, `"thirty seconds"`, `null`} {
		if err := json.Unmarshal([]byte(bad), &d); err == nil {
			t.Errorf("unmarshal(%s) accepted", bad)
		}
	}
}
