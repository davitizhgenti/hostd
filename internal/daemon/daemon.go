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
	"github.com/davitizhgenti/hostd/internal/version"
	"github.com/davitizhgenti/hostd/modules/apps"
	"github.com/davitizhgenti/hostd/modules/audio"
	"github.com/davitizhgenti/hostd/modules/demo"
	"github.com/davitizhgenti/hostd/modules/display"
	"github.com/davitizhgenti/hostd/sdk"
)

// Run runs hostd with command-line args until ctx ends or a signal
// arrives, and returns the exit code.
func Run(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("hostd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		stateDir   = fs.String("state", defaultStateDir(), "directory for the state database")
		runtimeDir = fs.String("runtime", os.Getenv("XDG_RUNTIME_DIR"), "runtime directory for the socket and the first admin token")
		socket     = fs.String("socket", "", "unix socket path (default <runtime>/hostd.sock)")
		listen     = fs.String("listen", ":7300", "TCP address for the home network; empty to disable")
		showVer    = fs.Bool("version", false, "print the version and exit")
		withDemo   = fs.Bool("demo", false, "load the demo module (a pretend lamp), for trying hostctl")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVer {
		fmt.Fprintln(stderr, "hostd", version.Version)
		return 0
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
	mods := []sdk.Module{
		apps.New(apps.Options{
			DesktopDirs: apps.DefaultDesktopDirs(), AppsDir: apps.DefaultAppsDir(), Logger: log,
			Backends: map[string]apps.Backend{
				apps.RunnerExec:   &apps.ExecRunner{Systemd: systemd, RuntimeDir: *runtimeDir, HomeDir: home},
				apps.RunnerDocker: &apps.DockerRunner{Docker: &apps.EngineAPI{Socket: apps.DefaultEngineSocket(*runtimeDir)}},
			},
		}),
		display.New(display.Options{Connect: display.SwayConnector(*runtimeDir), Logger: log}),
		audio.New(audio.Options{Backend: &audio.WirePlumber{}, Logger: log}),
	}
	if *withDemo {
		mods = append(mods, demo.New())
	}
	if err := serve(ctx, log, *stateDir, *runtimeDir, *socket, *listen, mods, stderr); err != nil {
		log.Error("hostd stopped", "err", err)
		return 1
	}
	return 0
}

func serve(ctx context.Context, log *slog.Logger, stateDir, runtimeDir, socket, listen string, mods []sdk.Module, stderr io.Writer) error {
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

	// Built-in modules are added here as they are written (M1 steps 3.7 on).
	reg := core.NewRegistry()
	for _, m := range mods {
		if err := reg.Add(m); err != nil {
			return err
		}
	}
	eng := core.New(reg, core.Options{Audit: st, Logger: log})
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
	srv, err := api.New(eng, st, api.Options{Logger: log, AdminTokenFile: adminFile, OnListening: onListening})
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
