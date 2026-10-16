package deploy

import (
	"context"
	"os"
	"strconv"
	"strings"
)

// UninstallArgs are what hostd-setup uninstall was asked.
type UninstallArgs struct {
	ScreenUser string
	Purge      bool // also delete hostd's data (tokens, audit trail, app files, hostd.toml)
	All        bool // also undo the base setup (autologin, managed configs, lingering)
}

// Uninstall takes hostd off the machine. It never removes system packages,
// the NVIDIA driver, or the screen user and its home: they may hold things
// that are not hostd's.
func (s *Setup) Uninstall(ctx context.Context, a UninstallArgs) error {
	if a.ScreenUser == "" {
		a.ScreenUser = "screen"
	}
	rm := func(paths ...string) {
		for _, p := range paths {
			_ = os.RemoveAll(s.path(p))
		}
	}
	s.Out.Step("Removing hostd")
	home := ""
	if s.ok(ctx, "id", a.ScreenUser) {
		var err error
		if home, err = s.homeOf(ctx, a.ScreenUser); err != nil {
			return err
		}
		out, _ := s.run(ctx, "id", "-u", a.ScreenUser)
		uid, _ := strconv.Atoi(strings.TrimSpace(string(out)))
		in := &installer{Setup: s, user: screenUser{name: a.ScreenUser, home: home, uid: uid}}
		units := home + "/.config/systemd/user"
		busUp := in.exists(in.user.runtime() + "/bus")
		if busUp {
			_, _ = in.asScreen(ctx, "systemctl", "--user", "disable", "--now", "hostd.service")
			// Apps hostd started run in their own units and containers.
			if out, err := in.asScreen(ctx, "systemctl", "--user", "list-units", "--plain", "--no-legend", "hostd-*.service"); err == nil {
				for _, line := range strings.Split(string(out), "\n") {
					if f := strings.Fields(line); len(f) > 0 {
						_, _ = in.asScreen(ctx, "systemctl", "--user", "stop", f[0])
					}
				}
			}
			if out, err := in.asScreen(ctx, "podman", "ps", "-aq", "--filter", "label=hostd.instance"); err == nil {
				for _, id := range strings.Fields(string(out)) {
					_, _ = in.asScreen(ctx, "podman", "rm", "-f", id)
				}
			}
			s.Out.Info("stopped hostd and the apps it started")
		}
		rm(units+"/hostd.service", units+"/hostd-rollback.service", units+"/default.target.wants/hostd.service", home+"/.local/lib/hostd")
		if busUp {
			_, _ = in.asScreen(ctx, "systemctl", "--user", "daemon-reload")
		}
		rm(in.user.runtime()+"/hostd.sock", in.user.runtime()+"/hostd-admin-token")
		s.Out.Info("removed the hostd service and its versions")
		if a.Purge {
			rm(home+"/.local/state/hostd", home+"/.config/hostd", home+"/.config/hostctl")
			s.Out.Info("deleted hostd's data: tokens, audit trail, app files, hostd.toml, hostctl login")
		} else {
			// Kept together: the tokens, and the login that uses one, so a
			// reinstall continues where this left off.
			s.Out.Info("kept hostd's data (tokens, audit trail, app files, hostctl login); --purge deletes it")
		}
		if a.All {
			s.Out.Step("Undoing the base setup")
			if in.exists("/etc/greetd/config.toml.orig") {
				if err := os.Rename(s.path("/etc/greetd/config.toml.orig"), s.path("/etc/greetd/config.toml")); err != nil {
					return err
				}
				s.Out.Info("restored the original greetd config (no more autologin to Sway)")
			}
			for _, f := range []string{"sway/config", "mako/config"} {
				p := home + "/.config/" + f
				if in.exists(p + ".orig") {
					_ = os.Rename(s.path(p+".orig"), s.path(p))
				} else {
					rm(p)
				}
			}
			rm(units+"/wayvnc.service", units+"/sockets.target.wants/podman.socket")
			_, _ = s.run(ctx, "loginctl", "disable-linger", a.ScreenUser)
			s.Out.Info("removed the managed Sway, mako and VNC configs, the Podman socket, and lingering")
		}
	}
	_, _ = s.run(ctx, "systemctl", "disable", "--now", "hostd-update.timer")
	rm("/etc/systemd/system/hostd-update.service", "/etc/systemd/system/hostd-update.timer",
		"/etc/hostd/applied.sha256", "/etc/hostd/notified.sha256", "/etc/hostd/addons.applied")
	_, _ = s.run(ctx, "systemctl", "daemon-reload")
	if a.All {
		rm("/etc/hostd/update.conf")
		if _, err := os.Stat(s.path(sshdDropIn)); err == nil {
			rm(sshdDropIn)
			_ = (&installer{Setup: s}).restartSSH(ctx)
			s.Out.Info("SSH password logins are back to the system's setting")
		}
	}
	rm("/usr/local/bin/hostctl", "/usr/local/sbin/hostd-setup", "/usr/local/bin/hostd-switch",
		"/usr/local/share/hostd-setup", "/usr/local/lib/hostd", "/usr/local/share/hostd")
	if home != "" {
		rm(home+"/.config/hostd/apps/hostd-menu.toml", home+"/.config/hostd/apps/hostd-overlay.toml")
	}
	s.Out.Info("removed hostctl, the menu and hostd-setup")
	s.Out.Done()
	s.Out.Info("Not removed: system packages, the NVIDIA driver, and the %s user.", a.ScreenUser)
	s.Out.Info("To remove the user and everything in its home as well: sudo userdel -r %s", a.ScreenUser)
	if a.All {
		s.Out.Info("Reboot to leave the Sway session: sudo reboot")
	}
	return nil
}
