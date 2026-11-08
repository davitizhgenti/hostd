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

	"github.com/davitizhgenti/hostd/internal/paths"

	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/core/api"
	"github.com/davitizhgenti/hostd/core/resources"
	"github.com/davitizhgenti/hostd/core/store"
	"github.com/davitizhgenti/hostd/internal/clock"
	"github.com/davitizhgenti/hostd/internal/extmod"
	"github.com/davitizhgenti/hostd/internal/host"
	"github.com/davitizhgenti/hostd/internal/migrate"
	"github.com/davitizhgenti/hostd/internal/sdnotify"
	"github.com/davitizhgenti/hostd/internal/secrets"
	"github.com/davitizhgenti/hostd/internal/system"
	"github.com/davitizhgenti/hostd/internal/systemd"
	"github.com/davitizhgenti/hostd/internal/tlsca"
	"github.com/davitizhgenti/hostd/internal/update"
	"github.com/davitizhgenti/hostd/internal/version"
	"github.com/davitizhgenti/hostd/sdk"
)

// Run runs hostd with command-line args until ctx ends or a signal
// arrives, and returns the exit code.
func Run(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("hostd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		configPath = fs.String("config", DefaultConfigPath(), "config file (missing: all defaults)")
		stateDir   = fs.String("state", paths.State(), "directory for the state database")
		runtimeDir = fs.String("runtime", os.Getenv("XDG_RUNTIME_DIR"), "runtime directory for the socket and the first admin token")
		socket     = fs.String("socket", "", "unix socket path (default <runtime>/hostd.sock)")
		listen     = fs.String("listen", ":7300", "TCP address for the home network; empty to disable (overrides the config)")
		showVer    = fs.Bool("version", false, "print the version and exit")
		withDemo   = fs.Bool("demo", false, "load the demo module (a pretend lamp), for trying hostctl")
		docs       = fs.String("docs", defaultDocsDir(), "serve this folder (the docs site) at /docs/, without a token; empty to disable")
		install    = fs.String("install", "", "install this hostd binary next to the running one and restart into it")
		libDir     = fs.String("lib", "", "-install: the installation directory (default: the one this binary is in)")
		doMigrate  = fs.Bool("migrate", false, "move the config folder from the layout before 0.2 to the current one (the old files are kept), and exit")
		noRestart  = fs.Bool("no-restart", false, "-install: switch to the new version, but leave restarting hostd to the caller")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVer {
		fmt.Fprintln(stderr, "hostd", version.Version)
		return 0
	}
	if *install != "" {
		return installFromFile(*install, *libDir, *stateDir, *configPath, *noRestart, stderr)
	}
	configDir := filepath.Dir(*configPath)
	if *doMigrate {
		did, err := migrate.Run(configDir)
		for _, line := range did {
			fmt.Fprintln(stderr, line)
		}
		if err != nil {
			fmt.Fprintln(stderr, "hostd:", err)
			return 1
		}
		if len(did) == 0 {
			fmt.Fprintln(stderr, "nothing to migrate")
		}
		return 0
	}
	if err := migrate.Needed(configDir); err != nil {
		fmt.Fprintln(stderr, "hostd:", err)
		return 2
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
	confs, err := moduleConfigs(cfg)
	if err != nil {
		fmt.Fprintln(stderr, "hostd:", err)
		return 2
	}
	home, _ := os.UserHomeDir()
	h := &host.Host{RuntimeDir: *runtimeDir, StateDir: *stateDir, ConfigDir: filepath.Dir(*configPath), Socket: *socket,
		Home: home, Log: log, Clock: clock.Real(), Secrets: secrets.Store{Dir: paths.Config("secrets")},
		Systemd: &systemd.UserSystemd{RuntimeDir: *runtimeDir}}
	defer h.Systemd.Close()
	var mods []sdk.Module
	for _, name := range cfg.Modules {
		m, err := builtinNamed(name).New(h, confs[name])
		if err != nil {
			fmt.Fprintf(stderr, "hostd: %s: %v\n", name, err)
			return 2
		}
		mods = append(mods, m)
	}
	// External modules: one that fails to start or to describe itself is
	// left out, and hostd runs without it.
	var externals []system.ExternalState
	for _, x := range loadExternals(configDir) {
		if x.err != nil {
			log.Error("external module left out", "module", x.name, "err", x.err)
			externals = append(externals, x.state(nil))
			continue
		}
		em, err := extmod.Load(ctx, extmod.Options{Name: x.name, Command: x.file.Command, Env: x.file.Env, Transport: x.file.Transport,
			SocketDir: filepath.Join(*runtimeDir, "hostd-modules"), Logger: log})
		if err != nil {
			log.Error("external module left out", "module", x.name, "err", err)
			externals = append(externals, x.state(err))
			continue
		}
		externals = append(externals, x.state(nil))
		mods = append(mods, em)
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
				return h.Systemd.RestartUnit(ctx, "hostd.service")
			}}
		}
	}
	if err := serve(ctx, log, *stateDir, *runtimeDir, configDir, *socket, *listen, *docs, cfg.API, mods, externals, coreOpts, newUpdater, stderr); err != nil {
		log.Error("hostd stopped", "err", err)
		return 1
	}
	return 0
}

func serve(ctx context.Context, log *slog.Logger, stateDir, runtimeDir, configDir, socket, listen, docs string, apiCfg APIConfig, mods []sdk.Module, externals []system.ExternalState,
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

	// The machine's CA, for HTTPS on the home network.
	var ca *tlsca.Authority
	if listen != "" && apiCfg.TLSOn() {
		if ca, err = openCA(ctx, log, filepath.Join(stateDir, "tls"), apiCfg.Names); err != nil {
			return fmt.Errorf("the machine's certificate authority: %w", err)
		}
	} else if listen != "" {
		log.Warn("the API on " + listen + " is plain HTTP ([api] tls = false): tokens cross the network unencrypted")
	}

	reg := core.NewRegistry()
	sys := system.New(system.Options{Store: st, TLS: ca, Externals: externals, Reserved: builtinNames(), Logger: log}) // hostd's own: tokens, the audit trail...
	if err := reg.Add(sys); err != nil {
		return err
	}
	for _, m := range mods {
		if err := reg.Add(m); err != nil {
			if em, ok := m.(*extmod.Module); ok { // e.g. a namespace another module owns
				log.Error("external module left out", "module", em.Manifest().Name, "err", err)
				for i := range externals { // shown on its resource
					if externals[i].Name == em.Manifest().Name {
						externals[i].Err = err.Error()
					}
				}
				_ = em.Stop(ctx)
				continue
			}
			return err
		}
	}
	coreOpts.Audit = st
	coreOpts.Tokens = scriptTokens{st}
	coreOpts.Privileged = []string{system.Name}
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

	// Resources: the files of every kind modules declare, read now that the
	// modules run, and followed.
	rs := resources.New(eng, resources.Options{Root: configDir, Logger: log})
	if err := rs.Start(ctx); err != nil {
		return fmt.Errorf("the resource folders: %w", err)
	}

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
	apiOpts := api.Options{Resources: rs, Logger: log, AdminTokenFile: adminFile, OnListening: onListening, DocsDir: docs, Allow: apiCfg.AllowPrefixes()}
	if ca != nil {
		apiOpts.TLS, apiOpts.CA = ca.TLSConfig(), ca.CAPEM()
	}
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

// defaultDocsDir is where the installer puts the docs site, if it did.
func defaultDocsDir() string {
	const dir = "/usr/local/share/hostd/docs"
	if fi, err := os.Stat(filepath.Join(dir, "index.html")); err == nil && fi.Mode().IsRegular() {
		return dir
	}
	return ""
}

// scriptTokens mints the tokens of rule scripts: automation, expiring
// with the run, revoked when it ends.
type scriptTokens struct{ st *store.Store }

func (t scriptTokens) Mint(ctx context.Context, name string, scopes []string, ttl time.Duration) (string, func(), error) {
	tok, secret, err := t.st.CreateToken(ctx, name, sdk.SourceAutomation, scopes, ttl)
	if err != nil {
		return "", nil, err
	}
	return secret, func() { _ = t.st.RevokeToken(context.Background(), tok.ID) }, nil
}
