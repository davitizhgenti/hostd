package apps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/davitizhgenti/hostd/internal/clock"

	"github.com/davitizhgenti/hostd/sdk"
)

// UnitSpec is what the exec runner asks systemd to run.
type UnitSpec struct {
	Description string
	Argv        []string
	Env         []string // KEY=value
	Dir         string
}

// UnitInfo is what the exec runner reads back about a unit.
type UnitInfo struct {
	Name        string
	Description string
	ActiveState string // active, inactive, failed...
	SubState    string // running, exited, failed, dead...
	MainPID     int
	ExitStatus  int    // ExecMainStatus
	ExitCode    int    // ExecMainCode: 1 exited, 2 killed, 3 dumped core
	Result      string // success, exit-code, signal, core-dump...
}

// Systemd is the part of the systemd user manager the exec runner uses.
// The real one talks D-Bus (systemd.go); tests use a fake.
type Systemd interface {
	// StartTransient starts a transient service and returns once its job
	// is done (for Type=exec: the binary was executed).
	StartTransient(ctx context.Context, name string, spec UnitSpec) error
	// Stop stops a unit and waits; a unit that is not loaded is fine.
	Stop(ctx context.Context, name string) error
	// ResetFailed forgets a failed unit, so its name can be reused.
	ResetFailed(ctx context.Context, name string) error
	// Unit reads a unit; ok is false if it is not loaded.
	Unit(ctx context.Context, name string) (info UnitInfo, ok bool, err error)
	// List returns the loaded units whose names match a glob.
	List(ctx context.Context, pattern string) ([]UnitInfo, error)
	// Watch calls fn with every unit's sub-state change until ctx ends.
	Watch(ctx context.Context, fn func(name, subState string)) error
}

// ExecRunner runs apps as transient systemd user services,
// hostd-<instance>.service. systemd starts and owns the process, so the
// app survives a hostd restart and its exit status is never lost: units
// have RemainAfterExit=yes, which keeps them loaded after the app ends
// until the runner has read the status and cleaned them up.
//
// It also runs Flatpak apps (flatpak run <id>) and web pages (a kiosk
// browser window with its own profile, so it is a process of its own and
// not a tab of a browser that already runs).
type ExecRunner struct {
	Systemd    Systemd
	RuntimeDir string // $XDG_RUNTIME_DIR, to find the Sway session
	HomeDir    string // working directory of apps
	StateDir   string // browser profiles go in <StateDir>/browser/<app>

	// Browser runs url apps; {url} and {profile} are replaced. Default:
	// Chromium if installed, else Firefox.
	Browser []string
	// Gamescope is the wrap for window.wrap = "gamescope"; the app's
	// command follows it after "--". Default: gamescope -f.
	Gamescope []string
	LookPath  func(string) (string, error)

	// For handoff apps (handoff.go).
	ProcRoot   string        // default /proc
	Clock      clock.Clock   // default real time
	PollEvery  time.Duration // how often to look for the app's processes (default 2s)
	StartGrace time.Duration // how long they may take to appear (default 3m)
	StopWait   time.Duration // between SIGTERM and SIGKILL (default 10s)
	Kill       func(pid int, sig syscall.Signal) error
	// RunCommand runs a handoff command outside a unit, when the unit is
	// still busy hosting the program it handed off to before (Steam
	// started by the first launch). Tests replace it.
	RunCommand func(ctx context.Context, argv, env []string, dir string) error

	mu       sync.Mutex
	handoffs map[string]*handoff // by instance ID
}

func (r *ExecRunner) clock() clock.Clock {
	if r.Clock == nil {
		return clock.Real()
	}
	return r.Clock
}

// browsers, in order of preference. Chromium's --user-data-dir and
// Firefox's --new-instance --profile each make a separate browser process.
var browsers = [][]string{
	{"chromium", "--kiosk", "--no-first-run", "--user-data-dir={profile}", "{url}"},
	{"chromium-browser", "--kiosk", "--no-first-run", "--user-data-dir={profile}", "{url}"},
	{"firefox-esr", "--kiosk", "--new-instance", "--profile", "{profile}", "{url}"},
	{"firefox", "--kiosk", "--new-instance", "--profile", "{profile}", "{url}"},
}

// command returns the argv that runs the app.
func (r *ExecRunner) command(app *App) ([]string, error) {
	lookPath := r.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	var argv []string
	switch app.Runner.Type {
	case RunnerFlatpak:
		argv = []string{"flatpak", "run", app.Runner.AppID}
	case RunnerURL:
		tmpl := r.Browser
		if len(tmpl) == 0 {
			for _, b := range browsers {
				if _, err := lookPath(b[0]); err == nil {
					tmpl = b
					break
				}
			}
		}
		if len(tmpl) == 0 {
			return nil, sdk.Errorf(sdk.CodeModuleUnavailable,
				"no browser for web apps: install chromium or firefox-esr, or set browser in hostd.toml")
		}
		profile := filepath.Join(r.StateDir, "browser", app.ID)
		if err := os.MkdirAll(profile, 0o700); err != nil {
			return nil, err
		}
		for _, a := range tmpl {
			argv = append(argv, strings.NewReplacer("{url}", app.Runner.URL, "{profile}", profile).Replace(a))
		}
	default:
		argv = append([]string(nil), app.Runner.Command...)
	}
	if len(argv) == 0 {
		return nil, sdk.Errorf(sdk.CodeInvalidArgs, "app %q has no command", app.ID)
	}
	if app.Window.Wrap == "gamescope" {
		wrap := r.Gamescope
		if len(wrap) == 0 {
			wrap = []string{"gamescope", "-f"}
		}
		if _, err := lookPath(wrap[0]); err != nil {
			return nil, sdk.Errorf(sdk.CodeModuleUnavailable, "app %q runs in gamescope, which is not installed", app.ID)
		}
		argv = append(append(append([]string(nil), wrap...), "--"), argv...)
	}
	return argv, nil
}

// unitName is hostd-<instance>.service. '#' is not allowed in unit names,
// so it is escaped the systemd way: firefox#2 -> hostd-firefox\x232.service.
func unitName(instance string) string {
	return "hostd-" + strings.ReplaceAll(instance, "#", `\x23`) + ".service"
}

var reDescription = regexp.MustCompile(`^hostd instance (\S+) of app (\S+)(?: handoff (\S+))?$`)

// description names the instance, app and handoff variable in the unit, so
// a restarted hostd can tell what a unit is.
func description(inst Instance, handoff string) string {
	d := fmt.Sprintf("hostd instance %s of app %s", inst.ID, inst.App)
	if handoff != "" {
		d += " handoff " + handoff
	}
	return d
}

// SessionEnv finds the graphical session in the runtime directory and
// returns the variables an app needs to open a window there. ok is false
// when no Wayland compositor is running.
func SessionEnv(runtimeDir string) (env []string, ok bool) {
	sockets, _ := filepath.Glob(filepath.Join(runtimeDir, "wayland-[0-9]*"))
	sort.Strings(sockets)
	var wayland string
	for _, s := range sockets {
		if !strings.HasSuffix(s, ".lock") {
			if fi, err := os.Stat(s); err == nil && fi.Mode()&os.ModeSocket != 0 {
				wayland = filepath.Base(s)
				break
			}
		}
	}
	if wayland == "" {
		return nil, false
	}
	env = []string{
		"WAYLAND_DISPLAY=" + wayland,
		"XDG_RUNTIME_DIR=" + runtimeDir,
		"XDG_SESSION_TYPE=wayland",
		"XDG_CURRENT_DESKTOP=sway",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=" + filepath.Join(runtimeDir, "bus"),
	}
	if socks, _ := filepath.Glob(filepath.Join(runtimeDir, "sway-ipc.*.sock")); len(socks) > 0 {
		sort.Strings(socks)
		env = append(env, "SWAYSOCK="+socks[len(socks)-1])
	}
	return env, true
}

// Start runs the app's command in its own unit.
func (r *ExecRunner) Start(ctx context.Context, inst Instance, app *App) (Instance, error) {
	argv, err := r.command(app)
	if err != nil {
		return inst, err
	}
	env := []string{"HOSTD_INSTANCE=" + inst.ID}
	if inst.Surface == SurfaceWindow {
		var ok bool
		var session []string
		session, ok = SessionEnv(r.RuntimeDir)
		env = append(env, session...)
		if !ok {
			return inst, sdk.Errorf(sdk.CodeModuleUnavailable, "no graphical session: Sway is not running")
		}
	}
	keys := make([]string, 0, len(app.Env))
	for k := range app.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+app.Env[k])
	}

	name := unitName(inst.ID)
	kv := app.Runner.Handoff
	// A unit left over from an earlier run with this ID (ended while
	// hostd was not watching) would block the name.
	if info, ok, err := r.Systemd.Unit(ctx, name); err == nil && ok {
		if info.ActiveState == "active" && info.SubState == "running" {
			if kv == "" {
				return inst, sdk.Errorf(sdk.CodeInternal, "unit %s is already running", name)
			}
			// The last launch's command became the program it hands off
			// to (it started Steam, which stays): hand off to it again.
			if err := r.runCommand(ctx, argv, env); err != nil {
				return inst, fmt.Errorf("handing off with %s: %w", strings.Join(argv, " "), err)
			}
			r.trackHandoff(inst.ID, kv, true)
			return inst, nil
		}
		_ = r.Systemd.Stop(ctx, name)
		_ = r.Systemd.ResetFailed(ctx, name)
	}
	err = r.Systemd.StartTransient(ctx, name, UnitSpec{
		Description: description(inst, kv), Argv: argv, Env: env, Dir: r.HomeDir,
	})
	if err != nil {
		_ = r.Systemd.ResetFailed(ctx, name)
		return inst, fmt.Errorf("starting %s: %w", strings.Join(argv, " "), err)
	}
	if kv != "" {
		r.trackHandoff(inst.ID, kv, false)
	}
	inst.Unit = name
	if info, ok, err := r.Systemd.Unit(ctx, name); err == nil && ok {
		inst.PID = info.MainPID
	}
	return inst, nil
}

// Stop stops the unit; systemd ends the app's whole process tree. A
// handed-off app's processes are ended by their variable; a unit that
// hosts the program it handed off to (Steam) is left running.
func (r *ExecRunner) Stop(ctx context.Context, inst Instance) error {
	name := unitName(inst.ID)
	info, loaded, _ := r.Systemd.Unit(ctx, name)
	kv := r.handoffOf(inst.ID, info)
	if kv != "" {
		r.mu.Lock()
		r.handoffDefaults()
		delete(r.handoffs, inst.ID)
		r.mu.Unlock()
		if err := r.stopHandoff(ctx, kv); err != nil {
			return err
		}
		if loaded && info.SubState == "running" {
			return nil
		}
	}
	if err := r.Systemd.Stop(ctx, name); err != nil {
		return err
	}
	_ = r.Systemd.ResetFailed(ctx, name)
	return nil
}

// Adopt finds hostd's units: running ones come back as running instances,
// ended ones are reported with their exit status and cleaned up.
func (r *ExecRunner) Adopt(ctx context.Context) ([]Instance, error) {
	units, err := r.Systemd.List(ctx, "hostd-*.service")
	if err != nil {
		return nil, err
	}
	var out []Instance
	for _, u := range units {
		m := reDescription.FindStringSubmatch(u.Description)
		if m == nil || unitName(m[1]) != u.Name {
			continue // not ours (e.g. started by hand with systemd-run)
		}
		inst := Instance{ID: m[1], App: m[2], Runner: RunnerExec, Unit: u.Name, PID: u.MainPID, State: StateRunning}
		ended, code, _ := endedState(u)
		if kv := m[3]; kv != "" {
			alive := len(pidsWithEnv(r.procRoot(), kv)) > 0
			switch {
			case alive:
				r.trackHandoff(inst.ID, kv, ended)
				r.mu.Lock()
				r.handoffs[inst.ID].seen = true
				r.mu.Unlock()
				inst.PID = 0
				out = append(out, inst)
				continue
			case !ended:
				// The command still runs but the app does not: it hosts
				// what it handed off to (Steam, after the game ended). Not
				// an instance; the unit stays.
				continue
			}
		}
		if ended {
			inst.State = StateExited
			if code != 0 {
				inst.State = StateFailed
			}
			inst.ExitCode = &code
			r.cleanup(ctx, u)
		}
		out = append(out, inst)
	}
	return out, nil
}

// Watch reports units that end on their own. With RemainAfterExit an app
// that ends leaves its unit "exited" or "failed"; "dead" only follows a
// stop (hostd's, or a unit being replaced by a new start under the same
// name), so reacting to it would clean up, and cancel, the new start.
func (r *ExecRunner) Watch(ctx context.Context, fn func(Ended)) error {
	r.mu.Lock()
	r.handoffDefaults()
	r.mu.Unlock()
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); r.pollHandoffs(pctx, fn) }()
	defer wg.Wait()
	return r.Systemd.Watch(ctx, func(name, sub string) {
		if !strings.HasPrefix(name, "hostd-") || (sub != "exited" && sub != "failed") {
			return
		}
		info, ok, err := r.Systemd.Unit(ctx, name)
		if err != nil || !ok {
			return // stopped by hostd and already gone
		}
		m := reDescription.FindStringSubmatch(info.Description)
		if m == nil {
			return
		}
		ended, code, reason := endedState(info)
		if !ended {
			return
		}
		if m[3] != "" && code == 0 && r.handedOff(m[1]) {
			return // the app goes on in other processes; pollHandoffs follows it
		}
		r.cleanup(ctx, info)
		fn(Ended{Instance: m[1], ExitCode: code, Reason: reason})
	})
}

// endedState reads whether a unit's app has ended, its exit code (128+n
// for signal n, as shells do) and a readable reason.
func endedState(u UnitInfo) (ended bool, code int, reason string) {
	switch {
	case u.SubState == "exited" || u.SubState == "dead" || u.ActiveState == "failed" || u.SubState == "failed":
	default:
		return false, 0, ""
	}
	switch u.ExitCode {
	case 2, 3: // killed, dumped
		return true, 128 + u.ExitStatus, fmt.Sprintf("killed by signal %d", u.ExitStatus)
	default:
		if u.ExitStatus == 0 && u.Result != "" && u.Result != "success" {
			return true, 1, u.Result
		}
		return true, u.ExitStatus, fmt.Sprintf("exit status %d", u.ExitStatus)
	}
}

func (r *ExecRunner) cleanup(ctx context.Context, u UnitInfo) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if u.ActiveState == "failed" {
		_ = r.Systemd.ResetFailed(ctx, u.Name)
		return
	}
	_ = r.Systemd.Stop(ctx, u.Name)
}

var errNoSystemd = errors.New("not connected to the systemd user manager")
