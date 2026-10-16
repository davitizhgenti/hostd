// Command hostd-setup installs hostd on a Debian 13 machine, keeps it up to
// date and takes it off again (see deploy and docs/install.md).
//
//	sudo hostd-setup [install] [options]
//	sudo hostd-setup update [options] [ADDON...]
//	sudo hostd-setup auto-update          what hostd-update.timer runs
//	sudo hostd-setup uninstall [--purge] [--all]
//	sudo hostd-setup addons | add NAME... | remove NAME...
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/davitizhgenti/hostd/deploy"
)

const usage = `hostd-setup: install hostd on a Debian 13 machine, update it, remove it.

  sudo hostd-setup [install] [options]   set up the machine (safe to run again)
  sudo hostd-setup update [ADDON...]     the newest signed build; prints only what changed
  sudo hostd-setup update --all          ...and run every installed add-on again
  sudo hostd-setup auto-update           what hostd-update.timer runs (see /etc/hostd/update.conf)
  sudo hostd-setup uninstall [--purge] [--all]
  sudo hostd-setup addons                list the add-ons, and which are installed
  sudo hostd-setup add NAME...           install add-ons (with what they need), as an update
  sudo hostd-setup remove NAME...        remove add-ons

Options:
  --gpu auto|nvidia|other   GPU setup (default: auto, from the PCI devices)
  --user NAME               screen user name (default: screen)
  --from DIR                install hostd and hostctl from DIR (a build), not the release
  --source DIR              update, add: run DIR's hostd-setup instead of downloading the newest
  --ssh-passwords keep      leave SSH password login on (by default it is turned off once
                            the admin running sudo has an SSH key)
  --all                     update: run every installed add-on; uninstall: also undo the base
                            setup (autologin to Sway, managed configs, lingering)
  --purge                   uninstall: also delete hostd's data (tokens, audit trail, app files)
  -v, --verbose             print every step, changed or not
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

type args struct {
	cmd                 string
	gpu, user, from     string
	sshPasswords        string
	source              string
	purge, all, verbose bool
	names               []string
	pass                []string // options handed to the newer installer by update
}

func parse(argv []string) (args, error) {
	a := args{cmd: "install"}
	if len(argv) > 0 {
		switch argv[0] {
		case "install", "update", "auto-update", "uninstall", "addons", "add", "remove":
			a.cmd, argv = argv[0], argv[1:]
		}
	}
	value := func(i int) (string, error) {
		if i+1 >= len(argv) {
			return "", fmt.Errorf("%s needs a value", argv[i])
		}
		return argv[i+1], nil
	}
	for i := 0; i < len(argv); i++ {
		switch arg := argv[i]; arg {
		case "--gpu", "--user", "--from", "--source", "--ssh-passwords":
			v, err := value(i)
			if err != nil {
				return a, err
			}
			i++
			switch arg {
			case "--gpu":
				if v != "auto" && v != "nvidia" && v != "other" {
					return a, errors.New("--gpu must be auto, nvidia or other")
				}
				a.gpu = v
			case "--user":
				a.user = v
			case "--from":
				a.from = v
			case "--source":
				a.source = v
			case "--ssh-passwords":
				if v != "keep" && v != "off" {
					return a, errors.New("--ssh-passwords must be keep or off")
				}
				a.sshPasswords = v
			}
			if arg != "--source" {
				a.pass = append(a.pass, arg, v)
			}
		case "--purge":
			a.purge = true
		case "--all":
			a.all = true
		case "-v", "--verbose":
			a.verbose = true
		case "-h", "--help":
			a.cmd = "help"
		default:
			if len(arg) > 0 && arg[0] == '-' {
				return a, fmt.Errorf("unknown option: %s (see --help)", arg)
			}
			switch a.cmd {
			case "add", "remove", "update":
				a.names = append(a.names, arg)
			default:
				return a, fmt.Errorf("unexpected argument: %s (see --help)", arg)
			}
		}
	}
	if a.purge && a.cmd != "uninstall" {
		return a, errors.New("--purge goes with uninstall")
	}
	if a.all && a.cmd != "uninstall" && a.cmd != "update" {
		return a, errors.New("--all goes with update or uninstall")
	}
	return a, nil
}

func run(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	a, err := parse(argv)
	if err != nil {
		fmt.Fprintf(stderr, "\033[31merror:\033[0m %s\n", err)
		return 2
	}
	if a.cmd == "help" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if a.cmd != "addons" && geteuid() != 0 {
		fmt.Fprintf(stderr, "\033[31merror:\033[0m run as root: sudo hostd-setup %s\n", a.cmd)
		return 1
	}
	// Quiet for updates, unless asked; the installer run by an update is
	// told so by HOSTD_UPDATE_MODE. One run by the shell installer of
	// earlier versions prints everything, with the markers its filter reads.
	// auto-update says in a line what it did; the installer it runs is quiet.
	updating := a.cmd == "update" || os.Getenv("HOSTD_UPDATE_MODE") != ""
	out := deploy.NewOutput(stdout, a.verbose || !updating || os.Getenv("HOSTD_FILTERED") != "")
	out.Markers = os.Getenv("HOSTD_FILTERED") != ""
	s := &deploy.Setup{Root: "/", Run: deploy.ExecRunner{Out: out.Writer()}, Out: out}

	self, _ := os.Executable()
	conf := deploy.ReadUpdateConf("/etc/hostd/update.conf")
	release := deploy.Release{URL: conf.ReleaseURL()}
	if u := os.Getenv("HOSTD_RELEASE_URL"); u != "" {
		release.URL = u
	}
	var key []byte
	if k := os.Getenv("HOSTD_RELEASE_KEY"); k != "" {
		key = []byte(k)
	}
	opts := deploy.Options{ScreenUser: a.user, GPU: a.gpu, From: a.from, Release: release, ReleaseKey: key, SSHPasswords: a.sshPasswords,
		SudoUser: os.Getenv("SUDO_USER"), Self: self, Auto: os.Getenv("HOSTD_AUTO") == "1",
		Adding: fields(os.Getenv("HOSTD_ADDING"))}
	if mode := os.Getenv("HOSTD_UPDATE_MODE"); mode != "" {
		opts.Update = &deploy.UpdateOptions{All: mode == "all", Addons: fields(os.Getenv("HOSTD_FORCE_ADDONS"))}
	}
	up := deploy.UpdateArgs{Options: opts, Source: a.source, Verbose: a.verbose, Pass: a.pass}
	return exitCode(do(ctx, a, s, opts, up, execInstaller, stdout), stderr)
}

// do runs a parsed command against a machine (s): the real one, or a
// test's.
func do(ctx context.Context, a args, s *deploy.Setup, opts deploy.Options, up deploy.UpdateArgs, run deploy.Exec, stdout io.Writer) error {
	var err error
	switch a.cmd {
	case "install":
		err = s.Install(ctx, opts)
	case "update":
		up.Update = &deploy.UpdateOptions{All: a.all, Addons: a.names}
		err = s.Update(ctx, up, run)
	case "auto-update":
		err = s.AutoUpdate(ctx, up, run)
	case "add":
		if err = s.AddAddons(a.names); err == nil {
			up.Update, up.Adding = &deploy.UpdateOptions{}, a.names
			// add shows everything, like an install.
			up.Verbose = true
			err = s.Update(ctx, up, run)
		}
	case "remove":
		err = s.RemoveAddons(ctx, a.user, a.names)
	case "addons":
		err = s.ListAddons(stdout)
	case "uninstall":
		err = s.Uninstall(ctx, deploy.UninstallArgs{ScreenUser: a.user, Purge: a.purge, All: a.all})
	}
	// An update's summary is the newer installer's: this one adds none.
	if a.cmd != "update" && a.cmd != "auto-update" && a.cmd != "add" {
		s.Out.Close()
	}
	return err
}

// exitCode is how hostd-setup ends after err: with the newer installer's
// status when that is what failed (it said why already).
func exitCode(err error, stderr io.Writer) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	if err != nil {
		fmt.Fprintf(stderr, "\033[31merror:\033[0m %s\n", err)
		return 1
	}
	return 0
}

// geteuid is the user hostd-setup runs as (a test may say root, to reach
// what follows the check with a command that changes nothing).
var geteuid = os.Geteuid

// execInstaller runs the newer installer with its output on the terminal.
func execInstaller(ctx context.Context, path string, args, env []string) error {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func fields(s string) []string { return strings.Fields(s) }
