package deploy

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// UpdateArgs are what hostd-setup update was asked.
type UpdateArgs struct {
	Options
	// Source runs the hostd-setup in this directory (a build:
	// hostd-setup-linux-<arch>, or hostd-setup) instead of downloading the
	// newest one.
	Source  string
	Verbose bool
	// Pass are the options handed on to the newer installer (--gpu, --user,
	// --from), as given.
	Pass []string
}

// Exec runs a program in place of the current step (the newer installer):
// its output goes straight to the terminal. Tests replace it.
type Exec func(ctx context.Context, path string, args, env []string) error

// Update runs the newest installer (signed, of the configured channel), or
// --source's, as an install with update's add-on rules: it updates hostd,
// hostctl and whatever else changed, and changes nothing that is current.
func (s *Setup) Update(ctx context.Context, a UpdateArgs, run Exec) error {
	if a.Update == nil {
		a.Update = &UpdateOptions{}
	}
	installed := s.installedAddons()
	for _, n := range a.Update.Addons {
		if !contains(installed, n) {
			return fmt.Errorf("add-on %q is not installed (see: hostd-setup addons)", n)
		}
	}
	arch, err := s.run(ctx, "dpkg", "--print-architecture")
	if err != nil {
		return err
	}
	name := "hostd-setup-linux-" + strings.TrimSpace(string(arch))
	var next string
	env := []string{}
	if a.Source != "" {
		next = filepath.Join(a.Source, name)
		if _, err := os.Stat(next); err != nil {
			next = filepath.Join(a.Source, "hostd-setup")
		}
		if _, err := os.Stat(next); err != nil {
			return fmt.Errorf("%s has no %s or hostd-setup", a.Source, name)
		}
	} else {
		dir, err := os.MkdirTemp("", "hostd-setup-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		s.Out.Step("Fetching the latest build's installer")
		if err := a.Release.Fetch(ctx, dir, "SHA256SUMS", "SHA256SUMS.sig", name); err != nil {
			return err
		}
		if err := Verify(dir, a.ReleaseKey, name); err != nil {
			return err
		}
		s.Out.Info("signature and checksums match")
		next = filepath.Join(dir, name)
		if err := os.Chmod(next, 0o755); err != nil {
			return err
		}
		env = append(env, "HOSTD_RELEASE_URL="+a.Release.URL) // the binaries come from the same channel
	}
	mode := "quick"
	if a.Update.All {
		mode = "all"
	}
	env = append(env, "HOSTD_UPDATE_MODE="+mode, "HOSTD_FORCE_ADDONS="+strings.Join(a.Update.Addons, " "),
		"HOSTD_ADDING="+strings.Join(a.Adding, " "))
	if a.Auto {
		env = append(env, "HOSTD_AUTO=1") // add-ons that change data do not upgrade it unattended
	}
	args := append([]string{"install"}, a.Pass...)
	if a.Verbose {
		args = append(args, "--verbose")
	}
	return run(ctx, next, args, env)
}

var reDebVersion = regexp.MustCompile(`(?m)  hostd_(.*)_amd64\.deb$`)

// AutoUpdate is what hostd-update.timer runs: a new signed build of the
// configured channel is applied, announced or left, as update.conf says.
func (s *Setup) AutoUpdate(ctx context.Context, a UpdateArgs, run Exec) error {
	conf := ReadUpdateConf(s.path("/etc/hostd/update.conf"))
	if conf.Apply == "manual" {
		s.Out.Info("updates are manual (/etc/hostd/update.conf): sudo hostd-setup update")
		return nil
	}
	dir, err := os.MkdirTemp("", "hostd-setup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := a.Release.Fetch(ctx, dir, "SHA256SUMS", "SHA256SUMS.sig"); err != nil {
		return err
	}
	if err := Verify(dir, a.ReleaseKey); err != nil {
		return err
	}
	sums, _ := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if applied, _ := os.ReadFile(s.path("/etc/hostd/applied.sha256")); bytes.Equal(sums, applied) {
		s.Out.Info("up to date (%s)", conf.Channel)
		return nil
	}
	ver := "the newest build"
	if m := reDebVersion.FindSubmatch(sums); m != nil {
		ver = string(m[1])
	}
	user := a.ScreenUser
	if user == "" {
		user = "screen"
	}
	screen := s.screenRunner(ctx, user)
	if conf.Apply == "notify" {
		if notified, _ := os.ReadFile(s.path("/etc/hostd/notified.sha256")); !bytes.Equal(sums, notified) {
			_, _ = screen(ctx, "hostctl", "display", "notice", "hostd "+ver+" is available: sudo hostd-setup update")
			if err := os.WriteFile(s.path("/etc/hostd/notified.sha256"), sums, 0o644); err != nil {
				return err
			}
			s.Out.Info("a new build (%s) is available; announced on the screen", ver)
		}
		return nil
	}
	// Not in the middle of a deploy or a scene, nor while someone uses the
	// screen (hostd and the menu restart); else the next run tries again.
	if _, err := screen(ctx, "hostctl", "wait-quiet", "--idle", "--max", "5m"); err != nil {
		s.Out.Info("busy or in use; trying again at the next check")
		return nil
	}
	s.Out.Step(fmt.Sprintf("Updating to %s (%s)", ver, conf.Channel))
	a.Update = &UpdateOptions{All: true}
	a.Auto = true
	return s.Update(ctx, a, run)
}

// screenRunner runs commands as user, in its systemd session, for steps
// that have no installer of their own (auto-update, uninstall).
func (s *Setup) screenRunner(ctx context.Context, user string) func(context.Context, ...string) ([]byte, error) {
	home, _ := s.homeOf(ctx, user)
	out, _ := s.run(ctx, "id", "-u", user)
	uid, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	in := &installer{Setup: s, user: screenUser{name: user, home: home, uid: uid}}
	return in.asScreen
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
