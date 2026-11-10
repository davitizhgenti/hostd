package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
)

// This file is hostd-admin's part: what only root can do, once. hostd-setup
// (the user's own, no root) checks for it and says what is missing.

// Prepare sets up the machine for hostd users: packages, the NVIDIA driver,
// the screen user with its groups, subordinate IDs and lingering, SSH keys,
// and autologin. It installs no hostd: the user does that with hostd-setup.
func (s *Setup) Prepare(ctx context.Context, o Options) error {
	if o.ScreenUser == "" {
		o.ScreenUser = "screen"
	}
	if o.GPU == "" {
		o.GPU = "auto"
	}
	in := &installer{Setup: s, o: o}
	for _, step := range []func(context.Context) error{
		in.checkSystem, in.enableNonFree, in.packages, in.nvidiaSway, in.screenUser, in.sshKeysOnly, in.autologin,
	} {
		if err := step(ctx); err != nil {
			return err
		}
	}
	if o.Self != "" { // stays on the machine, for add-ons and users
		if self, err := os.ReadFile(o.Self); err == nil {
			if _, err := s.installFile(self, "/usr/local/sbin/hostd-admin", 0o755, "root"); err != nil {
				return err
			}
		}
	}
	s.Out.Done()
	if !s.Out.AnyChanged() {
		s.Out.Info("No changes needed; the machine was already prepared.")
	}
	if in.needReboot {
		s.Out.Info("Reboot to finish: sudo reboot")
	}
	s.Out.Info("Now, as %s (no root): ./deploy/install.sh   (or hostd-setup)", o.ScreenUser)
	return nil
}

// adminInstaller is an installer for root add-ons, with the screen user's
// details for the files an add-on gives that user.
func (s *Setup) adminInstaller(ctx context.Context, o Options) (*installer, error) {
	if o.ScreenUser == "" {
		o.ScreenUser = "screen"
	}
	home, err := s.homeOf(ctx, o.ScreenUser)
	if err != nil {
		return nil, err
	}
	out, _ := s.run(ctx, "id", "-u", o.ScreenUser)
	uid, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return &installer{Setup: s, o: o, user: screenUser{name: o.ScreenUser, home: home, uid: uid}}, nil
}

// AdminAddAddons installs add-ons that need root, and records them in the
// machine's list (/etc/hostd/addons), which users' hostd-setup reads.
func (s *Setup) AdminAddAddons(ctx context.Context, o Options, names []string) error {
	order, err := addonOrder(names)
	if err != nil {
		return err
	}
	for _, a := range order {
		if !a.Privileged() {
			return fmt.Errorf("add-on %s needs no root: the user installs it: hostd-setup add %s", a.Name, a.Name)
		}
	}
	in, err := s.adminInstaller(ctx, o)
	if err != nil {
		return err
	}
	if err := s.AddAddons(names); err != nil {
		return err
	}
	in.o.Adding = names
	if err := in.runAddons(ctx, order); err != nil {
		return err
	}
	s.Out.Done()
	if in.needReboot {
		s.Out.Info("Reboot to finish: sudo reboot")
	}
	return nil
}

// AdminRemoveAddons removes root add-ons.
func (s *Setup) AdminRemoveAddons(ctx context.Context, o Options, names []string) error {
	for _, n := range names {
		a, err := LoadAddon(n)
		if err != nil {
			return err
		}
		if !a.Privileged() {
			return errors.New("add-on " + n + " needs no root: the user removes it: hostd-setup remove " + n)
		}
	}
	return s.RemoveAddons(ctx, o.ScreenUser, names)
}

// ListAdminAddons prints the add-ons that need root.
func (s *Setup) ListAdminAddons(w io.Writer) error {
	installed := s.installedAddons()
	for _, n := range AddonNames() {
		a, err := LoadAddon(n)
		if err != nil {
			return err
		}
		if !a.Privileged() {
			continue
		}
		mark := " "
		if slices.Contains(installed, n) {
			mark = "*"
		}
		fmt.Fprintf(w, "%s %-12s %s\n", mark, n, a.Description)
	}
	fmt.Fprintln(w, "(* installed)  sudo hostd-admin addons add NAME | remove NAME")
	return nil
}
