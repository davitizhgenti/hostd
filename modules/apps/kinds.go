package apps

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/modules/apps/catalog"
	"github.com/davitizhgenti/hostd/sdk"
)

// An app is a resource: the kind "app" lists every app hostd knows, the ones
// found on the machine (.desktop entries, read-only) and the ones that are
// files in apps/. Writing one writes its file, and a file for the ID of an
// app found on the machine overlays it (extends it).

func appKind() sdk.KindSpec {
	return sdk.KindSpec{
		Kind: "app", Plural: "apps", Description: "An app: what to run, where it shows, how it is told apart",
		WriteScope: contract.ScopeRun, // an app file can name any command
		Params:     []sdk.Param{{Name: "all", Description: "include hidden apps (settings, terminals...)"}},
		Columns: []sdk.Column{{Title: "ID", Path: "name"}, {Title: "NAME", Path: "spec.name"}, {Title: "RUNNER", Path: "spec.runner.type"},
			{Title: "SURFACE", Path: "spec.surface"}, {Title: "FROM", Text: []string{"{spec.source} (hidden) if spec.hidden", "{spec.source}"}},
			{Title: "PROBLEM", Path: "error"}},
		Empty: "no apps",
		Verbs: []sdk.VerbSpec{{Name: "open", Description: "Open the app: start it, or bring it forward if it runs", Action: contract.ActionAppOpen, Arg: "id"}},
	}
}

// path is where an app's file is (or would be).
func (m *Module) path(name string) string { return filepath.Join(m.opts.AppsDir, name+".toml") }

// checkFile parses an app file and builds the catalog as it would be with it,
// refusing it if the catalog finds it wrong: what the file would make.
func (m *Module) checkFile(r sdk.Resource) (catalog.AppFile, error) {
	path := m.path(r.Name)
	f, err := catalog.ParseAppFile(path, r.Raw)
	if err != nil {
		return f, err
	}
	if f.Extends == "" && f.ID != r.Name {
		return f, fmt.Errorf("the file defines app %q, not %q", f.ID, r.Name)
	}
	m.mu.Lock()
	files := make([]catalog.AppFile, 0, len(m.files)+1)
	for name, o := range m.files {
		if name != r.Name {
			files = append(files, o)
		}
	}
	discovered := m.discovered
	m.mu.Unlock()
	for _, p := range catalog.Build(discovered, append(files, f), nil).Problems {
		if p.File == path {
			return f, fmt.Errorf("%s", p.Error)
		}
	}
	return f, nil
}

// Apply implements sdk.Reconciler: an app file is put in the catalog.
func (m *Module) Apply(_ context.Context, r sdk.Resource) (sdk.ResourceStatus, error) {
	f, err := m.checkFile(r)
	if err != nil {
		return sdk.ResourceStatus{}, err
	}
	m.mu.Lock()
	m.files[r.Name] = f
	m.mu.Unlock()
	m.rebuild()
	return sdk.ResourceStatus{Conditions: []sdk.Condition{{Type: sdk.ConditionReady, Status: sdk.ConditionTrue}}}, nil
}

// Check implements sdk.Checker.
func (m *Module) Check(_ context.Context, r sdk.Resource) error {
	_, err := m.checkFile(r)
	return err
}

// Delete implements sdk.Reconciler: the app of a file that is gone is gone,
// or, if the machine has it, is as the machine has it.
func (m *Module) Delete(_ context.Context, _, name string) error {
	m.mu.Lock()
	delete(m.files, name)
	m.mu.Unlock()
	m.rebuild()
	return nil
}

// listed makes an app of the catalog a resource.
func (m *Module) listed(a *App) sdk.Listed {
	l := sdk.Listed{Name: a.ID, Spec: sdk.MustJSON(a), Source: a.Source,
		Status: sdk.ResourceStatus{Conditions: []sdk.Condition{{Type: sdk.ConditionReady, Status: sdk.ConditionTrue}}}}
	dir := filepath.Clean(m.opts.AppsDir)
	for _, f := range a.Files { // the file in apps/, not a .desktop entry
		if filepath.Dir(f) == dir {
			l.File = f
		}
	}
	return l
}

// List implements sdk.Lister: every app, hidden ones with ?all=true.
func (m *Module) List(_ context.Context, _ string, params map[string]string) ([]sdk.Listed, error) {
	all := params["all"] == "true" || params["all"] == "1"
	apps := m.catalog().List(all)
	out := make([]sdk.Listed, 0, len(apps))
	for _, a := range apps {
		out = append(out, m.listed(a))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get implements sdk.Lister: an app by ID or alias.
func (m *Module) Get(_ context.Context, _, name string) (sdk.Listed, bool) {
	a, ok := m.catalog().Get(name)
	if !ok {
		return sdk.Listed{}, false
	}
	return m.listed(a), true
}

// catalogAnswer is what the internal read "apps" returns (modules and
// scripts read the catalog this way, as JSON: {"apps": [...], "problems": []}).
func (m *Module) catalogAnswer(all bool) AppList {
	cat := m.catalog()
	problems := cat.Problems
	if problems == nil {
		problems = []catalog.Problem{}
	}
	return AppList{Apps: cat.List(all), Problems: problems}
}
