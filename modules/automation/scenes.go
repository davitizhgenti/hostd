package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/davitizhgenti/hostd/sdk"
)

// Scene is a state to reach: a file in the scenes folder, named by its
// ID (scenes/movie.toml is "movie"). Besides its name and its [[do]] actions
// a scene is sections, one for each facet a module plans ([audio], [apps],
// [display], or an external module's own): automation does not know what
// they say, it hands each to the module that plans it and runs what comes
// back.
type Scene struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Facets are the sections, by facet, as JSON.
	Facets map[string]json.RawMessage `json:"facets,omitempty"`
	// Do are actions of any module, sent last and in file order. Unlike the
	// sections they are not compared with the current state first: the
	// scene cannot know what an action of another module changes, so each
	// is sent every time the scene is applied.
	Do []SceneDo `json:"do,omitempty"`
}

// sets are the parts the scene has: its facets by name, then "do".
func (s Scene) sets() []string {
	out := make([]string, 0, len(s.Facets)+1)
	for name := range s.Facets {
		out = append(out, name)
	}
	sort.Strings(out)
	if len(s.Do) > 0 {
		out = append(out, "do")
	}
	return out
}

// SceneDo is one action a scene sends as it stands.
type SceneDo struct {
	Action string         `json:"action" toml:"action"`
	Args   map[string]any `json:"args,omitempty" toml:"args"`
}

var sceneID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ParseScene decodes one scene file and checks what automation can check:
// its name, its [[do]] actions, and that every other key is a section. Whether
// a section is right is for the module that plans it (the core asks it, when
// the scene is applied as a resource).
func ParseScene(path string, data []byte) (Scene, error) {
	var doc map[string]any
	if _, err := toml.Decode(string(data), &doc); err != nil {
		return Scene{}, err
	}
	s := Scene{ID: strings.TrimSuffix(filepath.Base(path), ".toml")}
	if !sceneID.MatchString(s.ID) {
		return Scene{}, fmt.Errorf("file name %q: use lowercase letters, digits, '.', '_' and '-'", filepath.Base(path))
	}
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch v := doc[k].(type) {
		case string:
			if k != "name" {
				return Scene{}, fmt.Errorf("unknown key %s (a scene's parts are sections such as [audio], and name)", k)
			}
			s.Name = v
		case map[string]any:
			if k == "name" || k == "do" {
				return Scene{}, fmt.Errorf("%s: not a section", k)
			}
			raw, err := json.Marshal(v)
			if err != nil {
				return Scene{}, fmt.Errorf("[%s]: %w", k, err)
			}
			if s.Facets == nil {
				s.Facets = map[string]json.RawMessage{}
			}
			s.Facets[k] = raw
		case []map[string]any, []any:
			if k != "do" {
				return Scene{}, fmt.Errorf("unknown key %s (a scene's parts are sections such as [audio], and [[do]])", k)
			}
			raw, _ := json.Marshal(v)
			dec := json.NewDecoder(strings.NewReader(string(raw)))
			dec.DisallowUnknownFields()
			// json tags are the toml names here, so this checks do's keys.
			var list []SceneDo
			if err := dec.Decode(&list); err != nil {
				return Scene{}, fmt.Errorf("do: unknown key or wrong type: %w", err)
			}
			s.Do = list
		default:
			return Scene{}, fmt.Errorf("unknown key %s (a scene's parts are sections such as [audio], and name)", k)
		}
	}
	for i, d := range s.Do {
		switch {
		case !reActionType.MatchString(d.Action):
			return Scene{}, fmt.Errorf("do %d: action %q: an action type such as lights.scene.set", i+1, d.Action)
		case strings.HasPrefix(d.Action, "scene."):
			return Scene{}, fmt.Errorf("do %d: a scene does not apply scenes (%s); name the other scene's parts here, or use a rule", i+1, d.Action)
		}
	}
	return s, nil
}

// checkFacets asks the core whether the module that plans each section
// accepts it. A section no module plans is refused, naming the ones there are.
func (m *Module) checkFacets(s Scene) error {
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	if core == nil {
		return nil
	}
	known := map[string]bool{}
	var names []string
	for _, f := range core.Facets() {
		known[f.Name] = true
		names = append(names, f.Name)
	}
	sort.Strings(names)
	for _, name := range s.sortedFacets() {
		if !known[name] {
			return fmt.Errorf("[%s]: no module plans it (the sections are: %s)", name, strings.Join(names, ", "))
		}
		if err := core.CheckFacet(name, s.Facets[name]); err != nil {
			return fmt.Errorf("[%s]: %s", name, strings.TrimPrefix(sdk.AsError(err).Message, "/: "))
		}
	}
	return nil
}

func (s Scene) sortedFacets() []string {
	names := make([]string, 0, len(s.Facets))
	for name := range s.Facets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Step is one action a scene needed, and what became of it.
type Step struct {
	Step   string          `json:"step"` // stop, output, volume, mute, start, focus, power, do; or a section's name for one that failed to plan
	Action string          `json:"action"`
	Args   json.RawMessage `json:"args"`
	// Status: applied, skipped (a higher-priority source holds the
	// resource), failed.
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Applied is a scene's result.
type Applied struct {
	Scene string `json:"scene"`
	Steps []Step `json:"steps"` // empty: it was reached already
}

// planned is a step with the position of its facet, for ordering.
type planned struct {
	sdk.PlannedAction
	facet int
}

// apply reaches the scene: it asks the module of each section for the
// actions still needed, puts the steps of all sections in their order, and
// sends them, then the scene's own do actions. A failed step does not stop
// the later ones, nor undo the earlier ones. A section whose module cannot
// say (it is not running, or cannot read its state) is one failed step.
func (m *Module) apply(ctx context.Context, s Scene) Applied {
	out := Applied{Scene: s.ID, Steps: []Step{}}
	run := func(step, action string, args any) {
		st := Step{Step: step, Action: action, Args: sdk.MustJSON(args)}
		res, err := m.core.Do(ctx, sdk.Action{Type: action, Args: st.Args})
		switch {
		case err != nil:
			st.Status, st.Error = "failed", err.Error()
		case res.Status == sdk.StatusSkipped:
			st.Status, st.Reason = "skipped", res.Reason
			if res.HeldBy != "" {
				st.Reason = "held by " + string(res.HeldBy)
			}
		default:
			st.Status = "applied"
		}
		out.Steps = append(out.Steps, st)
	}

	// The sections in the order the modules that plan them are in, so
	// that equal steps come out the same way every time.
	position := map[string]int{}
	for i, f := range m.core.Facets() {
		position[f.Name] = i
	}
	names := s.sortedFacets()
	sort.SliceStable(names, func(i, j int) bool { return position[names[i]] < position[names[j]] })
	var steps []planned
	for i, name := range names {
		acts, err := m.core.Plan(ctx, name, s.Facets[name])
		for _, a := range acts {
			steps = append(steps, planned{a, i})
		}
		if err != nil {
			out.Steps = append(out.Steps, Step{Step: name, Status: "failed", Error: strings.TrimPrefix(sdk.AsError(err).Message, "/: "), Args: json.RawMessage("null")})
		}
	}
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].Order < steps[j].Order })
	for _, st := range steps {
		var args any = json.RawMessage("{}")
		if len(st.Args) > 0 {
			args = st.Args
		}
		run(st.Step, st.Action, args)
	}

	for _, d := range s.Do {
		// The module may not be loaded: the core says so, and that step fails.
		args := d.Args
		if args == nil {
			args = map[string]any{}
		}
		run("do", d.Action, args)
	}
	return out
}
