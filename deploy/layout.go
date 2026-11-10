package deploy

import (
	"os"
	"os/user"
	"strconv"
)

// A layout is where hostd's files go: the system's (hostd-admin, root) or
// the user's own home (hostd-setup, no root).
type layout struct {
	hostctl, self, docs, menu     string
	updateConf, applied, notified string
	addonsState, addonsStamp      string
	updateUnits                   string // where hostd-update.{service,timer} go
	updateUnitsDir                string // the unit files in the embedded files
	updateCmd                     string // how a person runs an update
}

// UserMode makes s work for the user running it, in their home, without
// root: nothing outside the home is read for writing, no command is run as
// another user.
func (s *Setup) UserMode(u *user.User) error {
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return err
	}
	s.User = &screenUser{name: u.Username, home: u.HomeDir, uid: uid}
	return nil
}

// CurrentUserMode is UserMode for the user running this process.
func (s *Setup) CurrentUserMode() error {
	u, err := user.Current()
	if err != nil {
		return err
	}
	if h := os.Getenv("HOME"); h != "" {
		u.HomeDir = h
	}
	return s.UserMode(u)
}

func (s *Setup) lay() layout {
	if s.User == nil {
		return layout{
			hostctl: "/usr/local/bin/hostctl", self: "/usr/local/sbin/hostd-setup",
			docs: "/usr/local/share/hostd/docs", menu: "/usr/local/lib/hostd/hostd-menu",
			updateConf: "/etc/hostd/update.conf", applied: "/etc/hostd/applied.sha256", notified: "/etc/hostd/notified.sha256",
			addonsState: "/etc/hostd/addons", addonsStamp: "/etc/hostd/addons.applied",
			updateUnits: "/etc/systemd/system", updateUnitsDir: "systemd",
			updateCmd: "sudo hostd-setup update",
		}
	}
	h := s.User.home
	return layout{
		hostctl: h + "/.local/bin/hostctl", self: h + "/.local/bin/hostd-setup",
		docs: h + "/.local/share/hostd/docs", menu: h + "/.local/lib/hostd/hostd-menu",
		updateConf: h + "/.config/hostd/update.conf", applied: h + "/.local/state/hostd/applied.sha256", notified: h + "/.local/state/hostd/notified.sha256",
		addonsState: h + "/.local/state/hostd/addons", addonsStamp: h + "/.local/state/hostd/addons.applied",
		updateUnits: h + "/.config/systemd/user", updateUnitsDir: "systemd/user",
		updateCmd: "hostd-setup update",
	}
}

// rootAddonsState lists the add-ons an admin installed (hostd-admin), which
// a user's add-ons may need.
const rootAddonsState = "/etc/hostd/addons"

// UpdateConfPath is where this setup reads which channel it follows.
func (s *Setup) UpdateConfPath() string { return s.lay().updateConf }
