// Package contract holds what modules rely on from each other: the names
// of another module's actions, events and reads, and the shape of the data
// they carry. The owning module defines its side from here, and a module
// that uses it imports this package instead of repeating strings, so a
// change shows up at compile time (and in the contract tests) rather than
// as a silent mismatch. It holds names, types and conventions only, no
// behaviour. External modules (M6) get the same contract as JSON.
package contract

import "strings"

// The apps module: the app catalog and running instances.
const (
	AppsModule = "apps"

	// Scopes the apps module declares, used by other modules' actions too.
	ScopeApps  = "apps"
	ScopeFront = "display.front"

	ActionAppStart     = "app.start"     // args: AppStart
	ActionInstanceStop = "instance.stop" // args: {"id": instance}
	// ActionInstanceClosing tells the apps module that an instance was
	// asked to close (its windows), so its end counts as a normal exit
	// whatever its exit status. Args: {"id": instance}.
	ActionInstanceClosing = "instance.closing"

	EventInstances        = "instance.*" // every instance event; data: Instance
	EventInstanceStarting = "instance.starting"
	EventInstanceStarted  = "instance.started"
	EventInstanceExited   = "instance.exited"
	EventInstanceFailed   = "instance.failed"

	ReadInstances = "instances" // running instances: []Instance
)

// AppStart is the arguments of app.start.
type AppStart struct {
	ID     string `json:"id"`
	Front  bool   `json:"front,omitempty"`
	Action string `json:"action,omitempty"`
}

// Instance is what other modules see of an instance, in instance events
// and the instances read. The apps module's own type carries more; the
// fields here must keep their names and meaning.
type Instance struct {
	ID      string `json:"id"` // "firefox", or "firefox#2" for further copies
	App     string `json:"app"`
	Name    string `json:"name"`
	Surface string `json:"surface"` // window or background
	State   string `json:"state"`   // starting, running, stopping, exited, failed
	// Fullscreen is the app's window preference.
	Fullscreen *bool `json:"fullscreen,omitempty"`
	// Front: the start asked to be shown even while someone uses the screen.
	Front bool `json:"front,omitempty"`
	// Action is the app action it was started with, if any.
	Action string `json:"action,omitempty"`
	// Match is how to find its windows when they are not in its unit.
	Match *Match `json:"match,omitempty"`
	// ShownBy is the app whose window shows it, when it has none of its
	// own (a game inside Steam's gamescope session): focusing it brings
	// that app's window.
	ShownBy string `json:"shown_by,omitempty"`
	// Volume is the app's own volume (0-150), set on its sound when it
	// first plays.
	Volume *int `json:"volume,omitempty"`
}

// Ended reports whether the instance has ended.
func (in Instance) Ended() bool { return in.State == "exited" || in.State == "failed" }

// Match rules find an app's windows when cgroup matching cannot: apps
// whose windows are not in the instance's unit, such as Steam games
// (Steam's processes) and Flatpak apps (their own scope). A window
// matches if any rule that is set does. Class, app_id and title are glob
// patterns ("steam_app_*"); env is a KEY=value the window's process has.
type Match struct {
	Class string `json:"class,omitempty" toml:"class"`
	AppID string `json:"app_id,omitempty" toml:"app_id"`
	Title string `json:"title,omitempty" toml:"title"`
	Env   string `json:"env,omitempty" toml:"env"`
}

// IsZero reports whether no rule is set.
func (m Match) IsZero() bool { return m == Match{} }

// UnitName is the systemd user unit an instance runs in:
// hostd-<instance>.service. '#' is not allowed in unit names, so it is
// escaped the systemd way: firefox#2 -> hostd-firefox\x232.service. Every
// process of the instance, and so every window, is in this unit's cgroup.
func UnitName(instance string) string {
	return "hostd-" + strings.ReplaceAll(instance, "#", `\x23`) + ".service"
}

// InstanceFromUnit reads the instance from a unit name made by UnitName.
// hostd.service itself is not an instance.
func InstanceFromUnit(unit string) (string, bool) {
	name, ok := strings.CutSuffix(unit, ".service")
	if !ok {
		return "", false
	}
	id, ok := strings.CutPrefix(name, "hostd-")
	if !ok || id == "" {
		return "", false
	}
	return strings.ReplaceAll(id, `\x23`, "#"), true
}
