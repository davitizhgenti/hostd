package podman

import "github.com/davitizhgenti/hostd/modules/apps/spec"

type (
	App      = spec.App
	Instance = spec.Instance
	Ended    = spec.Ended
	State    = spec.State
)

const (
	StateRunning = spec.StateRunning
	StateExited  = spec.StateExited
	StateFailed  = spec.StateFailed
)
