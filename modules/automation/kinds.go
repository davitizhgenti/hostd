package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"time"

	"github.com/davitizhgenti/hostd/internal/tomlx"
	"github.com/davitizhgenti/hostd/sdk"
)

// The module's kinds of resource: a scene is a file in scenes/, a rule a file
// in rules/. The core reads the folders and tells the module (Apply, Delete);
// the module keeps what is in use.

func kinds() []sdk.KindSpec {
	return []sdk.KindSpec{
		{Kind: "scene", Plural: "scenes", Description: "A state to reach: audio, apps and display, and actions of any module",
			Columns: []sdk.Column{{Title: "SCENE", Path: "name"}, {Title: "NAME", Path: "spec.name"},
				{Title: "SETS", Path: "status.data.sets", Format: "list"}, {Title: "PROBLEM", Path: "error"}},
			Empty: "no scenes",
			Verbs: []sdk.VerbSpec{{Name: "apply", Description: "Reach the scene", Action: "scene.apply", Arg: "id"}}},
		{Kind: "script", Plural: "scripts", Description: "A program a rule runs, with a token of its own for the scopes it names",
			Columns: []sdk.Column{{Title: "SCRIPT", Path: "name"}, {Title: "COMMAND", Path: "spec.command", Format: "list"},
				{Title: "SCOPES", Path: "spec.scopes", Format: "list"}, {Title: "TIMEOUT", Path: "spec.timeout"}, {Title: "PROBLEM", Path: "error"}},
			Empty: "no scripts",
			Verbs: []sdk.VerbSpec{{Name: "run", Description: "Run the script now", Action: "script.run"}}},
		{Kind: "rule", Plural: "rules", Description: "When something happens, do something",
			Columns: []sdk.Column{{Title: "RULE", Path: "name"}, {Title: "ON", Path: "spec.on"},
				{Title: "DO", Text: []string{"script {spec.do.script} if spec.do.script", "action {spec.do.action} if spec.do.action", "scene {spec.do.scene} if spec.do.scene", "-"}},
				{Title: "FIRED", Path: "status.data.fired"},
				{Title: "LAST", Text: []string{"{status.data.last_fired:time} {status.data.last_result} if status.data.last_fired", "-"}},
				{Title: "STATE", Text: []string{"not in use if !spec", "active if !status.data.paused",
					"paused: {status.data.paused_why} (until {status.data.paused_until:hm}) if status.data.paused_until",
					"paused: {status.data.paused_why}"}},
				{Title: "PROBLEM", Path: "error"}},
			Empty: "no rules",
			Verbs: []sdk.VerbSpec{
				{Name: "pause", Description: "Stop the rule from firing until it is resumed", Action: "rule.pause"},
				{Name: "resume", Description: "Let a paused rule fire again", Action: "rule.resume"}}},
	}
}

// ruleDoc is a rule file: the rule's name is the file's.
type ruleDoc struct {
	On       string `toml:"on"`
	If       If     `toml:"if"`
	Do       Do     `toml:"do"`
	Cooldown string `toml:"cooldown"`
}

// ParseRule decodes and checks one rule file.
func ParseRule(name string, raw []byte) (Rule, error) {
	var d ruleDoc
	if _, err := tomlx.Decode(raw, &d); err != nil {
		return Rule{}, err
	}
	r := Rule{Name: name, On: d.On, If: d.If, Do: d.Do, Cooldown: d.Cooldown}
	switch {
	case r.On == "":
		return Rule{}, fmt.Errorf("needs on (an event type, or a namespace such as instance.*)")
	case r.Do.check() != nil:
		return Rule{}, r.Do.check()
	case r.If.Minutes < 0:
		return Rule{}, fmt.Errorf("if.minutes must be positive")
	}
	if r.If.App != "" {
		if _, err := path.Match(r.If.App, ""); err != nil {
			return Rule{}, fmt.Errorf("if.app %q: %w", r.If.App, err)
		}
	}
	if r.If.Between != "" {
		from, to, err := parseBetween(r.If.Between)
		if err != nil {
			return Rule{}, fmt.Errorf("if.between %q: %w", r.If.Between, err)
		}
		r.from, r.to = from, to
	}
	if r.Cooldown != "" {
		d, err := time.ParseDuration(r.Cooldown)
		if err != nil || d < 0 {
			return Rule{}, fmt.Errorf("cooldown %q: a duration such as 10m", r.Cooldown)
		}
		r.cooldown = d
	}
	return r, nil
}

// parse reads a resource of either kind.
func parseResource(r sdk.Resource) (any, error) {
	switch r.Kind {
	case "scene":
		return ParseScene(r.Name+".toml", r.Raw)
	case "rule":
		return ParseRule(r.Name, r.Raw)
	case "script":
		return ParseScript(r.Raw)
	}
	return nil, sdk.Errorf(sdk.CodeNotFound, "automation has no kind %q", r.Kind)
}

var ready = sdk.Condition{Type: sdk.ConditionReady, Status: sdk.ConditionTrue}

// Apply implements sdk.Reconciler.
func (m *Module) Apply(_ context.Context, r sdk.Resource) (sdk.ResourceStatus, error) {
	v, err := parseResource(r)
	if err != nil {
		return sdk.ResourceStatus{}, err
	}
	switch v := v.(type) {
	case Scene:
		if err := m.checkFacets(v); err != nil {
			return sdk.ResourceStatus{}, err
		}
		m.mu.Lock()
		m.scenes[v.ID] = v
		m.mu.Unlock()
		return sdk.ResourceStatus{Conditions: []sdk.Condition{ready}, Data: sdk.MustJSON(map[string]any{"sets": v.sets()})}, nil
	case scriptSpec:
		m.mu.Lock()
		m.scripts[r.Name] = v
		m.mu.Unlock()
		return sdk.ResourceStatus{Conditions: []sdk.Condition{ready}}, nil
	case Rule:
		m.mu.Lock()
		replaced := false
		for i := range m.rules {
			if m.rules[i].Name == v.Name {
				m.rules[i], replaced = v, true
			}
		}
		if !replaced {
			m.rules = append(m.rules, v)
			sort.Slice(m.rules, func(i, j int) bool { return m.rules[i].Name < m.rules[j].Name })
		}
		if m.runs[v.Name] == nil { // an edited rule keeps its count and its pause
			m.runs[v.Name] = &ruleRun{}
		}
		core := m.core
		m.mu.Unlock()
		if core != nil && v.Do.Action != "" && !core.Handles(v.Do.Action) {
			m.log.Warn("a rule's action is not handled by any loaded module (yet)", "rule", v.Name, "action", v.Do.Action)
		}
		return m.ruleStatus(v.Name), nil
	}
	return sdk.ResourceStatus{}, nil
}

// Check implements sdk.Checker: the file would be accepted, nothing is kept.
func (m *Module) Check(_ context.Context, r sdk.Resource) error {
	v, err := parseResource(r)
	if sc, ok := v.(Scene); ok && err == nil {
		err = m.checkFacets(sc)
	}
	return err
}

// Delete implements sdk.Reconciler.
func (m *Module) Delete(_ context.Context, kind, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch kind {
	case "scene":
		delete(m.scenes, name)
	case "script":
		delete(m.scripts, name)
	case "rule":
		for i := range m.rules {
			if m.rules[i].Name == name {
				m.rules = append(m.rules[:i], m.rules[i+1:]...)
				break
			}
		}
		delete(m.runs, name)
	}
	return nil
}

// StatusOf implements sdk.StatusReporter: a rule's live state.
func (m *Module) StatusOf(_ context.Context, kind, name string) (sdk.ResourceStatus, bool) {
	if kind != "rule" {
		return sdk.ResourceStatus{}, false
	}
	m.mu.Lock()
	_, ok := m.runs[name]
	m.mu.Unlock()
	if !ok {
		return sdk.ResourceStatus{}, false
	}
	return m.ruleStatus(name), true
}

// RuleStatus is a rule's live state, the data of its status.
type RuleStatus struct {
	Fired       int        `json:"fired"`
	LastFired   *time.Time `json:"last_fired,omitempty"`
	LastResult  string     `json:"last_result,omitempty"`
	Paused      bool       `json:"paused,omitempty"`
	PausedUntil *time.Time `json:"paused_until,omitempty"` // none: paused by hand
	PausedWhy   string     `json:"paused_why,omitempty"`
}

// ruleStatus reports a rule's runs and whether it is paused.
func (m *Module) ruleStatus(name string) sdk.ResourceStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := RuleStatus{}
	conds := []sdk.Condition{ready}
	if run := m.runs[name]; run != nil {
		st.Fired, st.LastResult = run.fired, run.lastResult
		if !run.lastFired.IsZero() {
			t := run.lastFired.UTC()
			st.LastFired = &t
		}
		if m.opts.Clock.Now().Before(run.pausedUntil) {
			st.Paused, st.PausedWhy = true, run.pausedWhy
			if run.pausedUntil.Year() < 9999 { // paused by hand has no end
				t := run.pausedUntil.UTC()
				st.PausedUntil = &t
			}
			conds = append(conds, sdk.Condition{Type: "Paused", Status: sdk.ConditionTrue, Reason: "Paused", Message: run.pausedWhy})
		}
	}
	data, _ := json.Marshal(st)
	return sdk.ResourceStatus{Conditions: conds, Data: data}
}
