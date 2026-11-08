package podman_test

import (
	"testing"

	"github.com/davitizhgenti/hostd/modules/apps/runner/podman"
	"github.com/davitizhgenti/hostd/modules/apps/runner/podman/podmantest"
	"github.com/davitizhgenti/hostd/modules/apps/spec"
	"github.com/davitizhgenti/hostd/modules/apps/spec/conformance"
)

// The docker runner behaves as every runner must.
func TestDockerRunnerConforms(t *testing.T) {
	conformance.Run(t, func(t *testing.T) conformance.Harness {
		eng := podmantest.NewFakeEngine()
		newRunner := func() spec.Backend { return &podman.DockerRunner{Docker: eng} }
		return conformance.Harness{
			Backend: newRunner(),
			App:     func(id string) *spec.App { return dockerApp(id, "img", "on-failure") },
			Exit:    func(inst spec.Instance, code int) { eng.Exit(inst.Container, code, false) },
			Gone: func(inst spec.Instance, code int) {
				eng.Mu.Lock()
				c := eng.Find(inst.Container)
				c.Info.Running, c.Info.ExitCode = false, code
				eng.Mu.Unlock()
			},
			Watching: eng.Watching,
			Restart:  newRunner,
		}
	})
}
