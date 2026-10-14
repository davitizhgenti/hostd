package apps

import (
	"github.com/davitizhgenti/hostd/modules/apps/runner/podman"
	"github.com/davitizhgenti/hostd/modules/apps/runner/units"
)

// The module's tests run the real runners against fake systemd and
// Podman, under the names they had when they lived here.
type (
	ExecRunner    = units.ExecRunner
	DockerRunner  = podman.DockerRunner
	ComposeRunner = podman.ComposeRunner
	EngineAPI     = podman.EngineAPI
	ContainerInfo = podman.ContainerInfo
	ContainerSpec = podman.ContainerSpec
)
