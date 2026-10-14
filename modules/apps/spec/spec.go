// Package spec holds what the apps module and its runners share: an app
// as the catalog describes it, a running instance, and the Backend a runner
// implements. It depends on nothing of the apps module, so runners live in
// packages of their own (runner/units, runner/podman) and new ones can be
// added without touching the module.
package spec

import (
	"context"
	"strings"
	"time"

	"github.com/davitizhgenti/hostd/contract"
)

// Surfaces: a window app shows on the screen; a background app is a
// service with no window.
const (
	SurfaceWindow     = "window"
	SurfaceBackground = "background"
)

// Runner types.
const (
	RunnerExec    = "exec"
	RunnerFlatpak = "flatpak"
	RunnerURL     = "url"
	RunnerDocker  = "docker"
	RunnerCompose = "compose"
	RunnerProcess = "process"
)

// App is one entry of the catalog: a runner plus a surface, and options.
type App struct {
	ID      string   `json:"id"`
	Aliases []string `json:"aliases,omitempty"` // other IDs that find this app, e.g. the discovered one it extends
	Name    string   `json:"name"`
	Icon    string   `json:"icon,omitempty"`

	// Source is where the app came from: desktop (a .desktop file), or
	// file (a hand-written TOML file). Files lists every file involved.
	Source string   `json:"source"`
	Files  []string `json:"files"`

	Runner   Runner         `json:"runner"`
	Surface  string         `json:"surface"`
	Window   Window         `json:"window"`
	Instance InstancePolicy `json:"instance"`
	Audio    Audio          `json:"audio,omitzero"`
	Requires []string       `json:"requires,omitempty"`
	Hidden   bool           `json:"hidden"`
	// Under is the app this one is listed under rather than on its own: a
	// Steam game under Steam. Menus show it with that app's actions.
	Under   string            `json:"under,omitempty"`
	Match   Match             `json:"match,omitzero"`
	Restart string            `json:"restart,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Health  Health            `json:"health,omitzero"`
	// Actions are extra ways to start the app ("New private window"):
	// from its desktop entry, and from app files.
	Actions []AppAction `json:"actions,omitempty"`
	// Deploy is how a service's new versions arrive ([source] in its
	// file); the deploy module builds and switches to them.
	Deploy *Deploy `json:"deploy,omitempty"`
}

// Deploy is a service's [source]: where new versions come from, how they
// are built, and the public port hostd serves them on.
type Deploy struct {
	Type   string   `json:"type" toml:"type"`               // push: git push to the box; remote: hostd polls url
	Build  []string `json:"build,omitempty" toml:"build"`   // run in the release's directory
	Port   int      `json:"port,omitempty" toml:"port"`     // public port; each release gets PORT
	Branch string   `json:"branch,omitempty" toml:"branch"` // default main
	Keep   int      `json:"keep,omitempty" toml:"keep"`     // releases kept (default 5)
	URL    string   `json:"url,omitempty" toml:"url"`       // remote: the repository
	Poll   string   `json:"poll,omitempty" toml:"poll"`     // remote: how often to look (default 60s)
	Secret string   `json:"secret,omitempty" toml:"secret"` // webhook: the secret's name (hostctl secret set)
	// image: the image (ghcr.io/me/blog), its tag (default latest), the
	// port the container listens on, and a secret with registry
	// credentials (user:token) for a private image.
	Image         string `json:"image,omitempty" toml:"image"`
	Tag           string `json:"tag,omitempty" toml:"tag"`
	ContainerPort int    `json:"container_port,omitempty" toml:"container_port"`
	Auth          string `json:"auth,omitempty" toml:"auth"`
}

// AppAction is an extra way to start an app: app.start with action=<id>
// runs Command as a new instance.
type AppAction struct {
	ID      string   `json:"id" toml:"id"`
	Name    string   `json:"name" toml:"name"`
	Command []string `json:"command" toml:"command"`
}

// Action finds one of the app's actions.
func (a *App) Action(id string) (AppAction, bool) {
	for _, x := range a.Actions {
		if x.ID == id {
			return x, true
		}
	}
	return AppAction{}, false
}

// Runner says how to run the app. Which fields apply depends on Type.
type Runner struct {
	Type    string   `json:"type" toml:"type"`
	Command []string `json:"command,omitempty" toml:"command"` // exec, process: the argv
	URL     string   `json:"url,omitempty" toml:"url"`         // url
	Image   string   `json:"image,omitempty" toml:"image"`     // docker
	Build   string   `json:"build,omitempty" toml:"build"`     // docker: build context
	Ports   []string `json:"ports,omitempty" toml:"ports"`     // docker
	File    string   `json:"file,omitempty" toml:"file"`       // compose
	AppID   string   `json:"app_id,omitempty" toml:"app_id"`   // flatpak ID

	// Handoff is for commands that pass the app to another program and
	// exit, such as "steam steam://rungameid/620": the instance runs while
	// processes with this KEY=value in their environment exist.
	Handoff string `json:"handoff,omitempty" toml:"handoff"`
}

// Window options for window apps.
type Window struct {
	Fullscreen bool   `json:"fullscreen"`
	Wrap       string `json:"wrap,omitempty"` // "gamescope"
	// ShownBy is the app whose window shows this one, when it has none of
	// its own: a game drawn inside Steam's gamescope session.
	ShownBy string `json:"shown_by,omitempty"`
}

// InstancePolicy decides what starting a running app does.
type InstancePolicy struct {
	Policy    string `json:"policy"`     // single | multiple
	IfRunning string `json:"if_running"` // focus | new | restart
}

// Audio options.
type Audio struct {
	Volume *int `json:"volume,omitempty"` // per-app volume applied on start
}

// Match rules find an app's windows when cgroup matching cannot (see
// contract.Match). An app with a handoff and no rules matches by its
// handoff variable.
type Match = contract.Match

// Health check for background apps: an app with one counts as running
// once it passes, and as failed if it does not within Start.
type Health struct {
	HTTP  string `json:"http,omitempty" toml:"http"`   // a URL that answers 2xx
	TCP   string `json:"tcp,omitempty" toml:"tcp"`     // host:port that accepts connections
	Start string `json:"start,omitempty" toml:"start"` // how long the first pass may take (default 60s)
	Every string `json:"every,omitempty" toml:"every"` // between checks afterwards (default 10s)
}

// Set reports whether the app has a health check.
func (h Health) Set() bool { return h.HTTP != "" || h.TCP != "" }

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
	// Closing: asked to close (instance.closing), so its end counts as an
	// exit whatever its status. The apps module keeps it; runners ignore it.
	Closing bool `json:"-"`
	// Match rules for its windows, for the display module.
	Match *Match `json:"match,omitempty"`
	// ShownBy: the app whose window shows it (see Window.ShownBy).
	ShownBy string `json:"shown_by,omitempty"`
	// Volume: the app's own volume (audio.volume), for the audio module.
	Volume *int `json:"volume,omitempty"`
	// Dir and Env: a release's directory and environment (deploy).
	Dir string            `json:"dir,omitempty"`
	Env map[string]string `json:"env,omitempty"`
	// Image and Ports: a container release's image and ports (deploy).
	Image string   `json:"image,omitempty"`
	Ports []string `json:"ports,omitempty"`

	Unit      string `json:"unit,omitempty"`      // systemd unit (exec runner)
	Container string `json:"container,omitempty"` // container name (docker runner)
	PID       int    `json:"pid,omitempty"`

	Started  time.Time  `json:"started"`
	Ended    *time.Time `json:"ended,omitempty"`
	ExitCode *int       `json:"exit_code,omitempty"`
	Error    string     `json:"error,omitempty"` // why it failed
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

// AppOf returns the app part of an instance ID ("firefox#2" is firefox).
func AppOf(instanceID string) string {
	app, _, _ := strings.Cut(instanceID, "#")
	return app
}
