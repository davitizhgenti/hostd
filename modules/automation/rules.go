package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/davitizhgenti/hostd/sdk"
)

// Rules decide when to act: on an event (a type or a namespace glob),
// if its conditions hold, apply a scene or run a script. They live in
// rules.toml. Work a rule starts runs at automation priority with the
// rule's name as its source, and with the event as its cause, so the
// core's chain depth stops two rules that trigger each other.

// Rule is one [[rule]] in rules.toml.
type Rule struct {
	Name     string `json:"name" toml:"name"`
	On       string `json:"on" toml:"on"`
	If       If     `json:"if,omitzero" toml:"if"`
	Do       Do     `json:"do" toml:"do"`
	Cooldown string `json:"cooldown,omitempty" toml:"cooldown"`

	cooldown time.Duration
	from, to int // minutes after midnight, for If.Between
}

// If are a rule's conditions; all that are set must hold.
type If struct {
	// App: a glob on the event's app (an instance event's app, or the
	// app of its "instance").
	App string `json:"app,omitempty" toml:"app"`
	// Between: local time of day, "22:00-06:00" (may cross midnight).
	Between string `json:"between,omitempty" toml:"between"`
	// Minutes: nobody has used the screen for this long. A rule waits
	// for it (and fires then, if the screen is still unused).
	Minutes int `json:"minutes,omitempty" toml:"minutes"`
}

// Do is what a rule does: one of them.
type Do struct {
	Scene  string `json:"scene,omitempty" toml:"scene"`
	Script string `json:"script,omitempty" toml:"script"`
}

// RuleState is a rule as GET /v1/rules shows it.
type RuleState struct {
	Rule
	Fired       int        `json:"fired"`
	LastFired   *time.Time `json:"last_fired,omitempty"`
	LastResult  string     `json:"last_result,omitempty"`
	PausedUntil *time.Time `json:"paused_until,omitempty"`
	PausedWhy   string     `json:"paused_why,omitempty"`
}

// RuleList is GET /v1/rules.
type RuleList struct {
	Rules    []RuleState `json:"rules"`
	Problems []Problem   `json:"problems"`
}

// Limiter pauses a rule that fires too often (the core's RuleLimiter).
type Limiter interface {
	Allow(rule string) (ok bool, until time.Time, tripped bool)
}

// ParseRules decodes and checks rules.toml.
func ParseRules(data []byte) ([]Rule, error) {
	var f struct {
		Rule []Rule `toml:"rule"`
	}
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return nil, err
	}
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("unknown key(s): %s", strings.Join(keys, ", "))
	}
	seen := map[string]bool{}
	for i := range f.Rule {
		r := &f.Rule[i]
		where := fmt.Sprintf("rule %d", i+1)
		if r.Name != "" {
			where = fmt.Sprintf("rule %q", r.Name)
		}
		switch {
		case r.Name == "":
			return nil, fmt.Errorf("%s: needs a name", where)
		case seen[r.Name]:
			return nil, fmt.Errorf("%s is defined twice", where)
		case r.On == "":
			return nil, fmt.Errorf("%s: needs on (an event type, or a namespace such as instance.*)", where)
		case (r.Do.Scene == "") == (r.Do.Script == ""):
			return nil, fmt.Errorf("%s: do needs a scene or a script (one of them)", where)
		case r.If.Minutes < 0:
			return nil, fmt.Errorf("%s: if.minutes must be positive", where)
		}
		seen[r.Name] = true
		if r.If.App != "" {
			if _, err := path.Match(r.If.App, ""); err != nil {
				return nil, fmt.Errorf("%s: if.app %q: %w", where, r.If.App, err)
			}
		}
		if r.If.Between != "" {
			from, to, err := parseBetween(r.If.Between)
			if err != nil {
				return nil, fmt.Errorf("%s: if.between %q: %w", where, r.If.Between, err)
			}
			r.from, r.to = from, to
		}
		if r.Cooldown != "" {
			d, err := time.ParseDuration(r.Cooldown)
			if err != nil || d < 0 {
				return nil, fmt.Errorf("%s: cooldown %q: a duration such as 10m", where, r.Cooldown)
			}
			r.cooldown = d
		}
	}
	return f.Rule, nil
}

func parseBetween(s string) (from, to int, err error) {
	a, b, ok := strings.Cut(s, "-")
	if !ok {
		return 0, 0, errors.New(`use "HH:MM-HH:MM"`)
	}
	parse := func(x string) (int, error) {
		t, err := time.Parse("15:04", strings.TrimSpace(x))
		if err != nil {
			return 0, errors.New(`use "HH:MM-HH:MM"`)
		}
		return t.Hour()*60 + t.Minute(), nil
	}
	if from, err = parse(a); err != nil {
		return 0, 0, err
	}
	if to, err = parse(b); err != nil {
		return 0, 0, err
	}
	return from, to, nil
}

// inTime reports whether t's time of day is in [from, to), which may wrap
// past midnight.
func inTime(t time.Time, from, to int) bool {
	m := t.Hour()*60 + t.Minute()
	if from <= to {
		return m >= from && m < to
	}
	return m >= from || m < to
}

// eventApp is the app an event is about.
func eventApp(ev sdk.Event) string {
	var d map[string]any
	_ = json.Unmarshal(ev.Data, &d)
	if app, ok := d["app"].(string); ok && app != "" {
		return app
	}
	if inst, ok := d["instance"].(string); ok {
		return appOf(inst)
	}
	return ""
}

// ruleRun is a rule's live state.
type ruleRun struct {
	fired       int
	lastFired   time.Time
	lastResult  string
	pausedUntil time.Time
	pausedWhy   string
	waiting     bool // for If.Minutes
}

// loadRules reads rules.toml when it changed. A broken file keeps the
// rules in use and is reported.
func (m *Module) loadRules() {
	fi, err := os.Stat(m.opts.RulesFile)
	var mod time.Time
	if err == nil {
		mod = fi.ModTime()
	}
	m.mu.Lock()
	same := mod.Equal(m.rulesMod) && m.rulesLoaded
	m.mu.Unlock()
	if same {
		return
	}
	var rules []Rule
	var problem *Problem
	switch b, err := os.ReadFile(m.opts.RulesFile); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		problem = &Problem{File: m.opts.RulesFile, Error: err.Error()}
	default:
		if rules, err = ParseRules(b); err != nil {
			problem = &Problem{File: m.opts.RulesFile, Error: err.Error()}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rulesMod, m.rulesLoaded = mod, true
	if problem != nil {
		if m.rulesProblem == nil || *m.rulesProblem != *problem {
			m.log.Warn("rules.toml not used; the rules before it stay", "err", problem.Error)
		}
		m.rulesProblem = problem
		return
	}
	m.rulesProblem = nil
	m.rules = rules
	for _, r := range rules {
		if m.runs[r.Name] == nil {
			m.runs[r.Name] = &ruleRun{}
		}
	}
}

// followRules matches every event against the rules.
func (m *Module) followRules(ctx context.Context, events <-chan sdk.Event) {
	for ev := range events {
		m.loadRules()
		m.mu.Lock()
		rules := append([]Rule(nil), m.rules...)
		m.mu.Unlock()
		for _, r := range rules {
			if sdk.MatchType(r.On, ev.Type) {
				m.consider(ctx, r, ev)
			}
		}
	}
}

// consider fires r for ev if its conditions hold.
func (m *Module) consider(ctx context.Context, r Rule, ev sdk.Event) {
	if r.If.App != "" {
		if ok, _ := path.Match(r.If.App, eventApp(ev)); !ok {
			return
		}
	}
	now := m.opts.Clock.Now()
	if r.If.Between != "" && !inTime(now.In(m.opts.Location), r.from, r.to) {
		return
	}
	if r.If.Minutes > 0 {
		idle, err := m.idleFor(ctx)
		if err != nil {
			return
		}
		need := time.Duration(r.If.Minutes) * time.Minute
		if idle < need {
			m.waitIdle(ctx, r, ev, need-idle)
			return
		}
	}
	m.done.Add(1)
	go func() { // a scene can take minutes; other events keep coming
		defer m.done.Done()
		m.fire(ctx, r, ev)
	}()
}

// idleFor is how long nobody has used the screen.
func (m *Module) idleFor(ctx context.Context) (time.Duration, error) {
	var st struct {
		Present   bool      `json:"present"`
		LastInput time.Time `json:"last_input"`
	}
	if err := m.read(ctx, "display", "display", &st); err != nil {
		return 0, err
	}
	if st.LastInput.IsZero() {
		return time.Duration(1<<62 - 1), nil // no input since hostd started
	}
	return m.opts.Clock.Since(st.LastInput), nil
}

// waitIdle checks again once the screen could have been unused long
// enough, and fires then if it was.
func (m *Module) waitIdle(ctx context.Context, r Rule, ev sdk.Event, d time.Duration) {
	m.mu.Lock()
	run := m.runs[r.Name]
	if run == nil || run.waiting {
		m.mu.Unlock()
		return
	}
	run.waiting = true
	m.mu.Unlock()
	m.opts.Clock.AfterFunc(d, func() {
		m.mu.Lock()
		run.waiting = false
		m.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if idle, err := m.idleFor(ctx); err == nil && idle >= time.Duration(r.If.Minutes)*time.Minute {
			m.fire(ctx, r, ev)
		}
	})
}

// fire runs a rule's do, unless it is paused or cooling down.
func (m *Module) fire(ctx context.Context, r Rule, ev sdk.Event) {
	now := m.opts.Clock.Now()
	m.mu.Lock()
	run := m.runs[r.Name]
	if run == nil {
		run = &ruleRun{}
		m.runs[r.Name] = run
	}
	if now.Before(run.pausedUntil) || (r.cooldown > 0 && !run.lastFired.IsZero() && now.Sub(run.lastFired) < r.cooldown) {
		m.mu.Unlock()
		return
	}
	run.lastFired = now // claimed: a burst of events fires it once per cooldown
	m.mu.Unlock()
	if m.opts.Limiter != nil {
		if ok, until, tripped := m.opts.Limiter.Allow(r.Name); !ok {
			m.pause(r.Name, until, "it fired too often")
			if tripped {
				m.log.Warn("a rule fired too often and is paused", "rule", r.Name, "until", until)
			}
			return
		}
	}
	m.mu.Lock()
	run.fired++
	m.mu.Unlock()

	src := sdk.Source{Kind: sdk.SourceAutomation, Name: r.Name}
	var result string
	if r.Do.Scene != "" {
		res, err := m.core.Do(ctx, sdk.Action{Type: "scene.apply", Args: sdk.MustJSON(map[string]string{"id": r.Do.Scene}),
			Source: src, Cause: ev.ID})
		result = describe(res, err)
		if sdk.CodeOf(err) == sdk.CodeLoopDetected {
			m.pause(r.Name, now.Add(10*time.Minute), "a loop: its work set off rules that set it off again")
		}
	} else {
		result = m.runScript(ctx, r, ev)
	}
	m.mu.Lock()
	run.lastResult = result
	m.mu.Unlock()
}

func describe(res sdk.Result, err error) string {
	switch {
	case err != nil:
		return "failed: " + err.Error()
	case res.Status == sdk.StatusSkipped:
		return "skipped: " + res.Reason
	}
	var a Applied
	if json.Unmarshal(res.Data, &a) == nil && a.Scene != "" {
		if len(a.Steps) == 0 {
			return "applied: nothing to change"
		}
		var parts []string
		for _, s := range a.Steps {
			parts = append(parts, s.Step+" "+s.Status)
		}
		return "applied: " + strings.Join(parts, ", ")
	}
	return "applied"
}

func (m *Module) pause(rule string, until time.Time, why string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if run := m.runs[rule]; run != nil {
		run.pausedUntil, run.pausedWhy = until, why
	}
}

func (m *Module) handleRule(a sdk.Action) (sdk.Result, error) {
	var args struct{ Name string }
	if err := a.DecodeArgs(&args); err != nil {
		return sdk.Result{}, err
	}
	m.loadRules()
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[args.Name]
	found := false
	for _, r := range m.rules {
		found = found || r.Name == args.Name
	}
	if !found || run == nil {
		return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "no rule %q in %s", args.Name, m.opts.RulesFile)
	}
	if a.Type == "rule.pause" {
		run.pausedUntil, run.pausedWhy = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), "paused by hand"
	} else {
		run.pausedUntil, run.pausedWhy = time.Time{}, ""
	}
	return sdk.Result{}, nil
}

func (m *Module) readRules() RuleList {
	m.loadRules()
	m.mu.Lock()
	defer m.mu.Unlock()
	list := RuleList{Rules: []RuleState{}, Problems: []Problem{}}
	if m.rulesProblem != nil {
		list.Problems = append(list.Problems, *m.rulesProblem)
	}
	now := m.opts.Clock.Now()
	for _, r := range m.rules {
		st := RuleState{Rule: r}
		if run := m.runs[r.Name]; run != nil {
			st.Fired, st.LastResult = run.fired, run.lastResult
			if !run.lastFired.IsZero() {
				t := run.lastFired.UTC()
				st.LastFired = &t
			}
			if now.Before(run.pausedUntil) {
				t := run.pausedUntil.UTC()
				st.PausedUntil, st.PausedWhy = &t, run.pausedWhy
			}
		}
		list.Rules = append(list.Rules, st)
	}
	return list
}
