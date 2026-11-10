// Command hostd-admin is the one place hostd uses root: what a machine
// needs once, so that users run hostd without it (see docs/install.md).
//
//	sudo hostd-admin prepare [--gpu G] [--user NAME]   packages, the screen user, autologin
//	sudo hostd-admin addons | add NAME... | remove NAME...   the add-ons that need root
//	sudo hostd-admin user add NAME [--port N] | remove NAME [--purge] | list
//	sudo hostd-admin uninstall [--purge] [--all]
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/davitizhgenti/hostd/deploy"
)

const usage = `hostd-admin: what hostd needs root for, done once.

  sudo hostd-admin prepare            packages, the screen user (groups, lingering, subordinate
                                      IDs for Podman), SSH keys only, autologin to Sway
  sudo hostd-admin addons             list the add-ons that need root, and which are installed
  sudo hostd-admin add NAME...        install root add-ons (nvidia, controllers, flatpak, ...)
  sudo hostd-admin remove NAME...     remove them
  sudo hostd-admin user add NAME [--port N]   a user with a hostd of their own
  sudo hostd-admin user remove NAME [--purge]
  sudo hostd-admin user list
  sudo hostd-admin uninstall [--purge] [--all]   take hostd off the machine

Options:
  --gpu auto|nvidia|other   prepare: GPU setup (default: from the PCI devices)
  --user NAME               the screen user (default: screen)
  --ssh-passwords keep      prepare: leave SSH password login on
  --all, --purge, --port N  as in hostd-setup (see its --help)

Everything else (installing and updating hostd, add-ons that need no root) is
hostd-setup, run by the user.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

var geteuid = os.Geteuid

func run(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	err := do(ctx, argv, stdout)
	if err != nil {
		if errors.Is(err, errUsage) {
			fmt.Fprint(stderr, usage)
			return 2
		}
		fmt.Fprintf(stderr, "\033[31merror:\033[0m %s\n", err)
		return 1
	}
	return 0
}

var errUsage = errors.New("usage")

func do(ctx context.Context, argv []string, stdout io.Writer) error {
	if len(argv) == 0 || argv[0] == "-h" || argv[0] == "--help" || argv[0] == "help" {
		fmt.Fprint(stdout, usage)
		return nil
	}
	cmd, rest := argv[0], argv[1:]
	switch cmd {
	case "prepare", "addons", "add", "remove", "user", "uninstall":
	default:
		return errUsage
	}
	var names []string
	var gpu, user, ssh string
	var port int
	var purge, all bool
	for i := 0; i < len(rest); i++ {
		switch a := rest[i]; a {
		case "--gpu", "--user", "--ssh-passwords", "--port":
			if i+1 >= len(rest) {
				return fmt.Errorf("%s needs a value", a)
			}
			v := rest[i+1]
			i++
			switch a {
			case "--gpu":
				gpu = v
			case "--user":
				user = v
			case "--ssh-passwords":
				ssh = v
			case "--port":
				n, err := strconv.Atoi(v)
				if err != nil {
					return errors.New("--port needs a number")
				}
				port = n
			}
		case "--purge":
			purge = true
		case "--all":
			all = true
		default:
			if len(a) > 0 && a[0] == '-' {
				return fmt.Errorf("unknown option: %s", a)
			}
			names = append(names, a)
		}
	}
	readOnly := cmd == "addons" || (cmd == "user" && len(names) == 1 && names[0] == "list")
	if !readOnly && geteuid() != 0 {
		return fmt.Errorf("run as root: sudo hostd-admin %s", cmd)
	}
	out := deploy.NewOutput(stdout, true)
	s := &deploy.Setup{Root: "/", Run: deploy.ExecRunner{Out: out.Writer()}, Out: out}
	self, _ := os.Executable()
	conf := deploy.ReadUpdateConf("/etc/hostd/update.conf")
	release := deploy.Release{URL: conf.ReleaseURL()}
	if u := os.Getenv("HOSTD_RELEASE_URL"); u != "" {
		release.URL = u
	}
	o := deploy.Options{ScreenUser: user, GPU: gpu, Release: release, SSHPasswords: ssh, SudoUser: os.Getenv("SUDO_USER"), Self: self}
	var err error
	switch cmd {
	case "prepare":
		err = s.Prepare(ctx, o)
	case "addons":
		err = s.ListAdminAddons(stdout)
	case "add":
		if len(names) == 0 {
			return errUsage
		}
		err = s.AdminAddAddons(ctx, o, names)
	case "remove":
		if len(names) == 0 {
			return errUsage
		}
		err = s.AdminRemoveAddons(ctx, o, names)
	case "user":
		switch {
		case len(names) == 2 && names[0] == "add":
			err = s.AddUser(ctx, o, names[1], port)
		case len(names) == 2 && names[0] == "remove":
			err = s.RemoveUser(ctx, names[1], purge)
		case len(names) == 1 && names[0] == "list":
			err = s.ListUsers(stdout)
		default:
			return errUsage
		}
	case "uninstall":
		err = s.Uninstall(ctx, deploy.UninstallArgs{ScreenUser: user, Purge: purge, All: all})
	default:
		return errUsage
	}
	out.Close()
	return err
}
