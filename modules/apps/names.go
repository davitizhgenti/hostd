package apps

import "github.com/davitizhgenti/hostd/modules/apps/spec"

// The apps module's own names for what it shares with its runners
// (modules/apps/spec).
type (
	App            = spec.App
	Deploy         = spec.Deploy
	AppAction      = spec.AppAction
	Runner         = spec.Runner
	Window         = spec.Window
	InstancePolicy = spec.InstancePolicy
	Audio          = spec.Audio
	Match          = spec.Match
	Health         = spec.Health
	State          = spec.State
	Instance       = spec.Instance
	Backend        = spec.Backend
	Passer         = spec.Passer
	Ended          = spec.Ended
)

const (
	SurfaceWindow     = spec.SurfaceWindow
	SurfaceBackground = spec.SurfaceBackground

	RunnerExec    = spec.RunnerExec
	RunnerFlatpak = spec.RunnerFlatpak
	RunnerURL     = spec.RunnerURL
	RunnerDocker  = spec.RunnerDocker
	RunnerCompose = spec.RunnerCompose
	RunnerProcess = spec.RunnerProcess

	StateStarting = spec.StateStarting
	StateRunning  = spec.StateRunning
	StateStopping = spec.StateStopping
	StateExited   = spec.StateExited
	StateFailed   = spec.StateFailed
)
