package deploy

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The shell add-on is hostd's on-screen UI: the menu, the notices, the OSD
// and the background (hostd-shell, Qt Quick). A normal install has it; an
// install with --no-menu, or after `hostd-setup remove shell`, is a screen
// machine with Sway and apps and no UI of its own. hostd, hostctl and the API
// are the same either way.
const shellAddon = "shell"

// menuWanted says whether this run installs the menu: it is on the add-on
// list already, or this is a fresh install (not --no-menu), or the machine
// has the menu from before the add-on existed (an update then records it).
func (in *installer) menuWanted() bool {
	if in.o.NoMenu {
		return false
	}
	if slices.Contains(in.installedAddons(), shellAddon) {
		return true
	}
	if in.User != nil && slices.Contains(in.adminInstalled(), shellAddon) {
		return true // a user's hostd on a machine whose admin installed the menu
	}
	if in.o.Update == nil {
		return true // a fresh install, or setting the machine up: the menu is on by default
	}
	return in.hasMenuFiles()
}

// hasMenuFiles reports whether the machine has the shell's unit or files
// (what installs before the add-on made).
func (in *installer) hasMenuFiles() bool {
	return in.exists(in.user.home+"/.config/systemd/user/hostd-shell.service") || in.exists(in.lay().shell+"/hostd-shell")
}

// installShell is the shell add-on's work: the packages (as root), the UI's
// files and its menu app, the unit, the menu's token, and the restart of
// what runs, as the base installer did before the menu was an add-on.
func (in *installer) installShell(ctx context.Context) error {
	if in.User == nil {
		if err := in.ensurePackages(ctx, shellPackages...); err != nil {
			return err
		}
	} else if missing := in.missingPackages(ctx, shellPackages); len(missing) > 0 {
		in.Out.Warn("warning: not installed: %s (an admin: sudo hostd-admin prepare)", strings.Join(missing, " "))
	}
	if err := in.installUI(); err != nil {
		return err
	}
	unit := bytes.ReplaceAll(file("systemd/hostd-shell.service"), []byte("@SHELL@"), []byte(in.lay().shell))
	var err error
	in.shellChanged, err = in.installFile(unit, in.user.home+"/.config/systemd/user/hostd-shell.service", 0o644, in.user.name)
	if err != nil {
		return err
	}
	if !in.shellChanged {
		in.Out.Info("shell service up to date")
	} else {
		_, _ = in.asScreen(ctx, "systemctl", "--user", "daemon-reload")
	}
	in.restartShell(ctx)
	// The menu's token: kind "local", so what is chosen on the screen
	// counts as someone at the screen (it comes to the front, and outranks
	// phones and scripts).
	localToken := in.user.home + "/.config/hostd/local.token"
	if in.exists(in.user.home + "/.config/hostctl/config.toml") {
		if err := in.menuToken(ctx, localToken); err != nil {
			return err
		}
	}
	return in.restartMenu(ctx, localToken)
}

// restartShell: the shell runs the UI's code, so a new version of it, or of
// its unit, takes effect when it restarts. Where 0.2's notices ran, a
// session is up: the shell takes their place now rather than at the next
// login. Not running and no session: the session starts it.
func (in *installer) restartShell(ctx context.Context) {
	if in.oldNotices {
		_, _ = in.asScreen(ctx, "systemctl", "--user", "stop", "hostd-notices.service")
	}
	swaySocks, _ := filepath.Glob(in.path(in.user.runtime() + "/sway-ipc.*.sock"))
	changedShell := in.shellChanged || in.shellFilesChanged || in.oldNotices
	if !changedShell && len(swaySocks) == 0 {
		return
	}
	active := in.screenOK(ctx, "systemctl", "--user", "is-active", "--quiet", "hostd-shell.service")
	if (changedShell && (active || in.oldNotices)) || (!active && len(swaySocks) > 0) {
		if _, err := in.asScreen(ctx, "systemctl", "--user", "restart", "hostd-shell.service"); err == nil {
			if active {
				in.Out.Changed("restarted hostd-shell")
			} else {
				in.Out.Changed("started hostd-shell")
			}
		} else {
			in.Out.Warn("warning: hostd-shell did not start; its packages come with: sudo hostd-admin prepare")
		}
	}
}

// removeShell takes the UI off: it stops, its unit, files and menu app go.
// The person's own settings (ui.toml, menu.toml) and the menu's token stay.
func (in *installer) removeShell(ctx context.Context) error {
	_, _ = in.asScreen(ctx, "systemctl", "--user", "stop", "hostd-shell.service")
	for _, p := range []string{
		in.user.home + "/.config/systemd/user/hostd-shell.service",
		in.user.home + "/.config/hostd/apps/hostd-menu.toml",
	} {
		if in.exists(p) {
			_ = os.Remove(in.path(p))
			in.Out.Changed("removed %s", p)
		}
	}
	if dir := in.lay().shell; in.exists(dir) {
		_ = os.RemoveAll(in.path(dir))
		in.Out.Changed("removed %s", dir)
	}
	_, _ = in.asScreen(ctx, "systemctl", "--user", "daemon-reload")
	return nil
}
