package system

import (
	"bytes"
	"context"
	"log/slog"

	"github.com/davitizhgenti/hostd/internal/extmod"
	"github.com/davitizhgenti/hostd/sdk"
)

// The kind "module": an external module, one file in modules/. hostd starts
// the external modules when it starts, so a change to a file takes effect at
// the next start; until then the resource says so.

// ExternalState is an external module as hostd found it when it started: its
// file as it was read, and why it is not running, if it is not.
type ExternalState struct {
	Name string
	Raw  []byte
	Err  string
}

func moduleKind() sdk.KindSpec {
	return sdk.KindSpec{
		Kind: "module", Plural: "modules",
		Description: "An external module: a program that speaks the module protocol, started with hostd",
		Schema:      sdk.SchemaOf[extmod.File](),
		Columns: []sdk.Column{{Title: "MODULE", Path: "name"}, {Title: "COMMAND", Path: "spec.command", Format: "list"},
			{Title: "TRANSPORT", Text: []string{"{spec.transport} if spec.transport", "socket"}},
			{Title: "STATE", Path: "status.data.state"}, {Title: "PROBLEM", Path: "error"}},
		Empty: "no external modules",
	}
}

// Apply implements sdk.Reconciler for the kind "module": it checks the file
// and reports whether the module runs as the file says.
func (m *Module) Apply(_ context.Context, r sdk.Resource) (sdk.ResourceStatus, error) {
	if r.Kind != "module" {
		return sdk.ResourceStatus{}, sdk.Errorf(sdk.CodeNotFound, "hostd has no kind %q", r.Kind)
	}
	if _, err := extmod.ParseFile(r.Name, r.Raw, m.o.Reserved); err != nil {
		return sdk.ResourceStatus{}, err
	}
	return m.moduleStatus(r.Name, r.Raw), nil
}

// Check implements sdk.Checker.
func (m *Module) Check(_ context.Context, r sdk.Resource) error {
	_, err := extmod.ParseFile(r.Name, r.Raw, m.o.Reserved)
	return err
}

// Delete implements sdk.Reconciler: a module that runs goes on running until
// hostd restarts.
func (m *Module) Delete(_ context.Context, _, name string) error {
	for _, e := range m.o.Externals {
		if e.Name == name && e.Err == "" {
			m.log().Info("an external module's file is gone; it runs until hostd restarts", "module", name)
		}
	}
	return nil
}

// moduleStatus compares a file with what hostd started with.
func (m *Module) moduleStatus(name string, raw []byte) sdk.ResourceStatus {
	state, cond := "restart required", sdk.Condition{Type: sdk.ConditionReady, Status: sdk.ConditionFalse, Reason: "RestartRequired",
		Message: "hostd starts external modules when it starts: restart it for this file to take effect"}
	for _, e := range m.o.Externals {
		if e.Name != name {
			continue
		}
		switch {
		case e.Err != "":
			state = "failed: " + e.Err
			cond = sdk.Condition{Type: sdk.ConditionReady, Status: sdk.ConditionFalse, Reason: "LoadFailed", Message: e.Err}
		case bytes.Equal(e.Raw, raw):
			state = "running"
			cond = sdk.Condition{Type: sdk.ConditionReady, Status: sdk.ConditionTrue}
		}
	}
	return sdk.ResourceStatus{Conditions: []sdk.Condition{cond}, Data: sdk.MustJSON(map[string]string{"state": state})}
}

func (m *Module) log() *slog.Logger {
	if m.o.Logger != nil {
		return m.o.Logger
	}
	return slog.Default()
}
