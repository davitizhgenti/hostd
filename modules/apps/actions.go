package apps

import (
	"context"
	"time"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/sdk"
	"github.com/davitizhgenti/hostd/sdk/kit"
)

// The arguments and results of the module's actions (app.open's are
// contract.AppStart, which other modules send).
type (
	instanceArgs struct {
		ID string `json:"id" desc:"instance ID, e.g. firefox or firefox#2"`
	}
	switchArgs struct {
		ID    string `json:"id" desc:"instance ID, e.g. firefox or firefox#2"`
		Front bool   `json:"front,omitempty" desc:"switch even while someone is using the screen (needs scope display.front)"`
	}
	checkArgs struct {
		Dir string `json:"dir"`
	}
	secretArgs struct {
		Name  string `json:"name"`
		Value string `json:"value" max:"65536"`
	}
	secretName struct {
		Name string `json:"name"`
	}
	rescanned struct {
		Apps     int `json:"apps"`
		Problems int `json:"problems"`
	}
)

// declare is the module's actions.
func (m *Module) declare() *kit.Actions {
	t := kit.NewActions(contract.AppsModule)
	instance := []sdk.KeyTemplate{"instance:{id}"}

	kit.On(t, sdk.ActionSpec{Type: contract.ActionAppRescan, Description: "Read the installed apps (desktop entries) again", Scope: contract.ScopeApps,
		Route: &sdk.Route{Method: "POST", Path: "/v1/catalog/rescan"}},
		func(context.Context, sdk.Action, struct{}) (rescanned, error) {
			m.rescan()
			cat := m.catalog()
			return rescanned{Apps: cat.Len(), Problems: len(cat.Problems)}, nil
		})
	kit.OnResult(t, sdk.ActionSpec{Type: contract.ActionAppCheck, Description: "Check a folder of app files without using it (a followed config)", Scope: sdk.ScopeAdmin},
		m.handleCheck)

	kit.OnResult(t, sdk.ActionSpec{Type: contract.ActionAppOpen, Description: "Open an app: start it, or, if it already runs, bring it forward, start a new copy or restart it, per its settings",
		Keys: []sdk.KeyTemplate{"app:{id}"}, Scope: contract.ScopeApps,
		ArgScopes: map[string]string{"front": contract.ScopeFront, "dir": contract.ScopeDeploy,
			"env": contract.ScopeDeploy, "new": contract.ScopeDeploy, "image": contract.ScopeDeploy, "ports": contract.ScopeDeploy},
		Timeout: sdk.Duration(5 * time.Minute), // pulling a container image can take a while
		CLI: &sdk.CLI{Alias: []string{"open", "start"}, Flags: map[string]sdk.Flag{"args": {Name: "arg"}},
			Done: []string{"{instance} is already running if already_running", "started {instance} ({state})"}}},
		m.handleStart)
	kit.Check(t, contract.ActionAppOpen, m.validateStart)
	kit.OnResult(t, sdk.ActionSpec{Type: contract.ActionInstanceStop, Description: "Stop a running instance",
		Keys: instance, Scope: contract.ScopeApps, Timeout: sdk.Duration(time.Minute),
		Route: &sdk.Route{Method: "POST", Path: "/v1/instances/{id}/stop"},
		CLI:   &sdk.CLI{Alias: []string{"stop"}, Done: []string{"stopped {instance}"}}},
		m.handleStop)
	kit.OnResult(t, sdk.ActionSpec{Type: contract.ActionInstanceClosing, Description: "Note that an instance was asked to close: its end counts as a normal exit",
		Keys: instance, Scope: contract.ScopeApps, Timeout: sdk.Duration(10 * time.Second)},
		m.handleClosing)

	kit.OnResult(t, sdk.ActionSpec{Type: contract.ActionInstanceSwitch, Description: "Bring an instance's window to the front",
		Keys: instance, Scope: contract.ScopeApps,
		ArgScopes: map[string]string{"front": contract.ScopeFront},
		Timeout:   sdk.Duration(20 * time.Second),
		Route:     &sdk.Route{Method: "POST", Path: "/v1/instances/{id}/switch"},
		CLI: &sdk.CLI{Alias: []string{"switch", "focus"}, Done: []string{
			"not switched: someone is using the screen (they got a notice; --front switches anyway) if reason=in_use",
			"switched to {instance}"}}},
		m.handleSwitch)
	kit.OnResult(t, sdk.ActionSpec{Type: contract.ActionInstanceClose, Description: "Close an instance as its window's close button would; stop it if it stays open",
		Keys: instance, Scope: contract.ScopeApps, Timeout: sdk.Duration(closeTimeout),
		Route: &sdk.Route{Method: "POST", Path: "/v1/instances/{id}/close"},
		CLI:   &sdk.CLI{Alias: []string{"close"}, Done: []string{"stopped {instance} (it did not close) if stopped", "closed {instance}"}}},
		m.handleClose)
	kit.OnResult(t, sdk.ActionSpec{Type: contract.ActionInstanceRestart, Description: "Stop an instance and start its app again, as it was started",
		Keys: instance, Scope: contract.ScopeApps,
		Timeout: sdk.Duration(5 * time.Minute), // the start can pull an image
		Route:   &sdk.Route{Method: "POST", Path: "/v1/instances/{id}/restart"},
		CLI:     &sdk.CLI{Alias: []string{"restart"}, Done: []string{"restarted {instance} ({state})"}}},
		m.handleRestart)
	for _, typ := range []string{contract.ActionInstanceStop, contract.ActionInstanceClosing, contract.ActionInstanceSwitch,
		contract.ActionInstanceClose, contract.ActionInstanceRestart} {
		kit.Check(t, typ, m.validateStop)
	}

	kit.OnResult(t, sdk.ActionSpec{Type: ActionRun, Description: "Run a command now, on the screen or in the background (an instance with no app file)",
		Keys: []sdk.KeyTemplate{"app:run"}, Scope: contract.ScopeRun,
		ArgScopes: map[string]string{"front": contract.ScopeFront},
		Timeout:   sdk.Duration(time.Minute),
		Route:     &sdk.Route{Method: "POST", Path: "/v1/run"},
		CLI: &sdk.CLI{Alias: []string{"run"}, Rest: "command", Wait: true, Flags: map[string]sdk.Flag{"background": {Short: "b"}},
			Done: []string{"started {instance} ({state})"}}},
		m.handleRun)

	kit.On(t, sdk.ActionSpec{Type: "secret.set", Description: "Store a secret, for app environments as {secret:<name>} (the value is read from standard input)",
		Scope: sdk.ScopeAdmin, Secret: []string{"value"}, Keys: []sdk.KeyTemplate{"secret:{name}"}}, m.setSecret)
	kit.On(t, sdk.ActionSpec{Type: "secret.remove", Description: "Remove a secret",
		Scope: sdk.ScopeAdmin, Keys: []sdk.KeyTemplate{"secret:{name}"}}, m.removeSecret)
	return t
}
