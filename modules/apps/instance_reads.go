package apps

import (
	"sort"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/sdk"
)

func instanceReads() []sdk.ReadSpec {
	return []sdk.ReadSpec{
		{Name: contract.Instances.Name, Description: "Running instances (?all=true adds recently ended ones)", Path: "/v1/instances",
			CLI: &sdk.CLI{Alias: []string{"ps"}, Empty: "nothing is running",
				Params: []sdk.Param{{Name: "all", Description: "also the instances that ended lately"}},
				Columns: []sdk.Column{{Title: "INSTANCE", Path: "id"}, {Title: "APP", Path: "app"}, {Title: "RUNNER", Path: "runner"},
					{Title: "STATE", Text: []string{"{state} ({exit_code}) if exit_code", "{state} (0) if exit_code=0", "{state}"}},
					{Title: "PID", Text: []string{"- if ended", "{pid} if pid", "-"}},
					{Title: "SINCE", Path: "ended|started", Format: "time"}}}}, // when it started, or ended
		{Name: "instance", Description: "One instance", Path: "/v1/instances/{id}"},
		{Name: "logs", Description: "An instance's latest log lines (?lines=200, ?plain=true for the messages only)", Path: "/v1/instances/{id}/logs"},
	}
}

func (m *Module) readInstances(params map[string]string) []Instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Instance, 0, len(m.instances))
	for _, in := range m.instances {
		out = append(out, *in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if params["all"] == "true" || params["all"] == "1" {
		out = append(out, m.ended...)
	}
	return out
}

func (m *Module) readInstance(id string) (Instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if in, ok := m.instances[id]; ok {
		return *in, nil
	}
	for _, e := range m.ended {
		if e.ID == id {
			return e, nil
		}
	}
	return Instance{}, sdk.Errorf(sdk.CodeNotFound, "no instance %q", id)
}

// matchRules returns the app's match rules for its instances, or nil.
func matchRules(a *App) *Match {
	if a.Match.IsZero() {
		return nil
	}
	m := a.Match
	return &m
}
