// Package daemon is hostd: it opens the state database, starts the core
// and the built-in modules, and serves the API. cmd/hostd is a thin main
// around Run, so tests can run the daemon in-process.
package daemon

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/core/api"
	"github.com/davitizhgenti/hostd/core/store"
	"github.com/davitizhgenti/hostd/internal/sdnotify"
	"github.com/davitizhgenti/hostd/internal/update"
	"github.com/davitizhgenti/hostd/internal/version"
	"github.com/davitizhgenti/hostd/modules/apps"
	"github.com/davitizhgenti/hostd/modules/audio"
	"github.com/davitizhgenti/hostd/modules/demo"
	"github.com/davitizhgenti/hostd/modules/deploy"
	"github.com/davitizhgenti/hostd/modules/display"
	"github.com/davitizhgenti/hostd/sdk"
)

// Run runs hostd with command-line args until ctx ends or a signal
// arrives, and returns the exit code.
func Run(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("hostd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		configPath = fs.String("config", DefaultConfigPath(), "config file (missing: all defaults)")
		stateDir   = fs.String("state", defaultStateDir(), "directory for the state database")
		runtimeDir = fs.String("runtime", os.Getenv("XDG_RUNTIME_DIR"), "runtime directory for the socket and the first admin token")
		socket     = fs.String("socket", "", "unix socket path (default <runtime>/hostd.sock)")
		listen     = fs.String("listen", ":7300", "TCP address for the home network; empty to disable (overrides the config)")
		showVer    = fs.Bool("version", false, "print the version and exit")
		withDemo   = fs.Bool("demo", false, "load the demo module (a pretend lamp), for trying hostctl")
		install    = fs.String("install", "", "install this hostd binary next to the running one and restart into it")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVer {
		fmt.Fprintln(stderr, "hostd", version.Version)
		return 0
	}
	if *install != "" {
		return installFromFile(*install, stderr)
	}
	cfg, err := LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, "hostd:", err)
		return 2
	}
	flagSet := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { flagSet[f.Name] = true })
	if !flagSet["listen"] && cfg.Listen != nil {
		*listen = *cfg.Listen
	}
	if *withDemo && !contains(cfg.Modules, "demo") {
		cfg.Modules = append(cfg.Modules, "demo")
	}
	if *runtimeDir == "" {
		fmt.Fprintln(stderr, "hostd: XDG_RUNTIME_DIR is not set; pass -runtime")
		return 2
	}
	if *socket == "" {
		*socket = filepath.Join(*runtimeDir, "hostd.sock")
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	home, _ := os.UserHomeDir()
	systemd := &apps.UserSystemd{RuntimeDir: *runtimeDir}
	defer systemd.Close()
	var mods []sdk.Module
	for _, name := range cfg.Modules {
		switch name {
		case "apps":
			mods = append(mods, apps.New(apps.Options{
				DesktopDirs: apps.DefaultDesktopDirs(), AppsDir: apps.DefaultAppsDir(), SecretsDir: apps.DefaultSecretsDir(), Logger: log,
				Backends: map[string]apps.Backend{
					apps.RunnerExec: &apps.ExecRunner{Systemd: systemd, RuntimeDir: *runtimeDir, HomeDir: home,
						StateDir: *stateDir, Browser: cfg.Browser, Gamescope: cfg.Gamescope},
					apps.RunnerDocker: &apps.DockerRunner{Docker: &apps.EngineAPI{Socket: apps.DefaultEngineSocket(*runtimeDir)}},
				},
			}))
		case "display":
			profiles, err := display.LoadProfiles(filepath.Join(filepath.Dir(*configPath), "controllers"))
			if err != nil {
				fmt.Fprintf(stderr, "hostd: %v\n", err)
				return 2
			}
			mods = append(mods, display.New(display.Options{Connect: display.SwayConnector(*runtimeDir), Logger: log,
				Input: &display.Evdev{Profiles: profiles}, Profiles: profiles, IdleAfter: time.Duration(cfg.IdleAfter),
				Notifier: &display.DesktopNotifier{RuntimeDir: *runtimeDir}, Keys: cfg.keys, Buttons: cfg.buttons,
				HoldFor: time.Duration(cfg.Input.Hold), GPU: display.SystemGPU{}}))
		case "audio":
			mods = append(mods, audio.New(audio.Options{Backend: &audio.WirePlumber{}, Logger: log,
				Media: &audio.DBusMedia{RuntimeDir: *runtimeDir}}))
		case "deploy":
			mods = append(mods, deploy.New(deploy.Options{Logger: log}))
		case "demo":
			mods = append(mods, demo.New())
		}
	}
	coreOpts := core.Options{Logger: log, HoldWindow: time.Duration(cfg.HoldWindow), HoldWindows: cfg.holdWindows()}
	// Updates work when hostd runs from the installer's layout under
	// systemd; a development build just has none.
	var newUpdater func(*store.Store) api.Updater
	if lib, err := update.FromExecutable(); err == nil && os.Getenv("INVOCATION_ID") != "" {
		newUpdater = func(st *store.Store) api.Updater {
			return &updater{lib: lib, store: st, configPath: *configPath, restart: func() error {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				return systemd.RestartUnit(ctx, "hostd.service")
			}}
		}
	}
	if err := serve(ctx, log, *stateDir, *runtimeDir, *socket, *listen, mods, coreOpts, newUpdater, stderr); err != nil {
		log.Error("hostd stopped", "err", err)
		return 1
	}
	return 0
}

func serve(ctx context.Context, log *slog.Logger, stateDir, runtimeDir, socket, listen string, mods []sdk.Module,
	coreOpts core.Options, newUpdater func(*store.Store) api.Updater, stderr io.Writer) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	st, err := store.Open(ctx, filepath.Join(stateDir, "state.db"), store.Options{Logger: log})
	if err != nil {
		return err
	}
	defer st.Close()

	adminFile := filepath.Join(runtimeDir, "hostd-admin-token")
	secret, created, err := st.EnsureAdminToken(ctx)
	if err != nil {
		return err
	}

	reg := core.NewRegistry()
	for _, m := range mods {
		if err := reg.Add(m); err != nil {
			return err
		}
	}
	coreOpts.Audit = st
	eng := core.New(reg, coreOpts)
	if err := eng.Start(ctx); err != nil {
		return err
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := eng.Stop(sctx); err != nil {
			log.Error("stopping modules", "err", err)
		}
	}()

	// Once the API is listening: hand over the first admin token (so
	// whoever waits for that file can use it at once), then tell systemd
	// hostd is ready.
	onListening := func() {
		if created {
			if err := os.WriteFile(adminFile, []byte(secret+"\n"), 0o600); err != nil {
				log.Error("writing the first admin token", "file", adminFile, "err", err)
			}
			fmt.Fprintf(stderr, "\nFirst start: created the admin token. It is shown only now, and saved in\n%s\nuntil its first use. On your laptop: hostctl login <address>\n\n    %s\n\n", adminFile, secret)
		}
		if err := sdnotify.Ready(); err != nil {
			log.Warn("telling systemd hostd is ready", "err", err)
		}
	}
	apiOpts := api.Options{Logger: log, AdminTokenFile: adminFile, OnListening: onListening}
	if newUpdater != nil {
		apiOpts.Updater = newUpdater(st)
	}
	srv, err := api.New(eng, st, apiOpts)
	if err != nil {
		return err
	}
	log.Info("hostd started", "version", version.Version, "socket", socket, "listen", listen, "modules", reg.Order())
	err = srv.Serve(ctx, socket, listen)
	if errors.Is(err, context.Canceled) {
		err = nil
	}
	log.Info("hostd stopping")
	_ = sdnotify.Stopping()
	return err
}

func defaultStateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "hostd")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "hostd-state"
	}
	return filepath.Join(home, ".local", "state", "hostd")
}
