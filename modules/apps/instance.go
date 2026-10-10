package apps

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// State of an instance.
type State string

const (
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateExited   State = "exited" // ended normally, or stopped on request
	StateFailed   State = "failed" // could not start, or ended with an error
)

// Ended reports whether the instance is over.
func (s State) Ended() bool { return s == StateExited || s == StateFailed }

// Instance is one running copy of an app.
type Instance struct {
	ID      string `json:"id"` // "firefox", or "firefox#2" for further copies
	App     string `json:"app"`
	Name    string `json:"name"`
	Runner  string `json:"runner"`
	Surface string `json:"surface"`
	State   State  `json:"state"`
	// Fullscreen is the app's window preference, for the display module.
	Fullscreen bool `json:"fullscreen"`
	// Front: the start asked to be shown even while someone is using the
	// screen (app.start front=true).
	Front bool `json:"front,omitempty"`
	// Action is the app action it was started with ("" for the app itself).
	Action string `json:"action,omitempty"`
	// closing: asked to close (instance.closing); its end is an exit.
	closing bool
	// Match rules for its windows, for the display module.
	Match *Match `json:"match,omitempty"`
	// ShownBy: the app whose window shows it (see Window.ShownBy).
	ShownBy string `json:"shown_by,omitempty"`
	// Volume: the app's own volume (audio.volume), for the audio module.
	Volume *int `json:"volume,omitempty"`
	// Dir and Env: a release's directory and environment (deploy).
	Dir string            `json:"dir,omitempty"`
	Env map[string]string `json:"env,omitempty"`

	Unit      string `json:"unit,omitempty"`      // systemd unit (exec runner)
	Container string `json:"container,omitempty"` // container name (docker runner)
	PID       int    `json:"pid,omitempty"`

	Started  time.Time  `json:"started"`
	Ended    *time.Time `json:"ended,omitempty"`
	ExitCode *int       `json:"exit_code,omitempty"`
	Error    string     `json:"error,omitempty"` // why it failed
}

// change is something that happens to an instance.
type change int

const (
	changeStarted    change = iota // the runner confirmed it runs
	changeStartError               // the runner could not start it
	changeStop                     // hostd asked it to stop
	changeEnded                    // its process ended (exit code known or not)
)

// next applies a change to a state. An instance that ends with a non-zero
// exit code has failed, unless hostd asked it to stop.
func next(cur State, c change, exitCode int) (State, error) {
	switch {
	case cur.Ended():
		return cur, fmt.Errorf("instance already %s", cur)
	case c == changeStarted && cur == StateStarting:
		return StateRunning, nil
	case c == changeStarted:
		return cur, nil // late confirmation; nothing to do
	case c == changeStartError && cur == StateStarting:
		return StateFailed, nil
	case c == changeStartError:
		return cur, fmt.Errorf("start error while %s", cur)
	case c == changeStop:
		return StateStopping, nil
	case c == changeEnded && cur == StateStopping:
		return StateExited, nil
	case c == changeEnded && exitCode == 0:
		return StateExited, nil
	case c == changeEnded:
		return StateFailed, nil
	}
	return cur, fmt.Errorf("unknown change %d", c)
}

// nextID picks an instance ID for app: the app ID itself if free, then
// app#2, app#3... the lowest number not in use by a live instance.
func nextID(app string, inUse func(string) bool) string {
	if !inUse(app) {
		return app
	}
	for n := 2; ; n++ {
		if id := app + "#" + strconv.Itoa(n); !inUse(id) {
			return id
		}
	}
}

// appOf returns the app part of an instance ID.
func appOf(instanceID string) string {
	app, _, _ := strings.Cut(instanceID, "#")
	return app
}

// Backend starts and stops instances for one runner type (exec, docker...).
// Instances never run as children of hostd, so they survive a hostd
// restart, and Adopt finds them again.
type Backend interface {
	// Start runs inst and returns it with backend fields (Unit, PID...)
	// filled in, once it is running.
	Start(ctx context.Context, inst Instance, app *App) (Instance, error)
	// Stop ends inst and returns once it has stopped.
	Stop(ctx context.Context, inst Instance) error
	// Adopt returns the instances this backend finds already running when
	// hostd starts. Ones that ended while hostd was away come back Ended,
	// with their exit code when known, and are cleaned up.
	Adopt(ctx context.Context) ([]Instance, error)
	// Watch calls fn whenever an instance ends on its own, until ctx ends.
	Watch(ctx context.Context, fn func(Ended)) error
}

// A Passer can run an app's command outside an instance, to hand a
// request to the app's program, which already runs as inst: an action of
// a handoff app (Steam's Big Picture while Steam runs).
type Passer interface {
	Pass(ctx context.Context, inst Instance, app *App) error
}

// Ended reports an instance whose process ended.
type Ended struct {
	Instance string
	ExitCode int
	Reason   string // e.g. "exit status 1", "killed by signal 9"
}
