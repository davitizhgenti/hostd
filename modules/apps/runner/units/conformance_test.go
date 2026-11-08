package units

import (
	"testing"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/modules/apps/runner/units/unitstest"
	"github.com/davitizhgenti/hostd/modules/apps/spec"
	"github.com/davitizhgenti/hostd/modules/apps/spec/conformance"
)

// The exec runner behaves as every runner must.
func TestExecRunnerConforms(t *testing.T) {
	conformance.Run(t, func(t *testing.T) conformance.Harness {
		sd := unitstest.NewFakeSystemd()
		session := unitstest.FakeSession(t)
		newRunner := func() spec.Backend { return &ExecRunner{Systemd: sd, RuntimeDir: session, HomeDir: "/home/screen"} }
		return conformance.Harness{
			Backend:  newRunner(),
			App:      func(id string) *spec.App { return execApp(id, id) },
			Exit:     func(inst spec.Instance, code int) { sd.Exit(contract.UnitName(inst.ID), code, 1) },
			Gone:     func(inst spec.Instance, code int) { sd.ExitQuietly(contract.UnitName(inst.ID), code) },
			Watching: sd.Watching,
			Restart:  newRunner,
		}
	})
}
