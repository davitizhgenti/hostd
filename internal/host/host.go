// Package host is what hostd gives the built-in modules it builds, and how
// a module package says how to build it. A module package exports one
// Builtin; the daemon keeps a plain list of them, so adding a module never
// touches the daemon's wiring.
package host

import (
	"log/slog"

	"github.com/davitizhgenti/hostd/internal/clock"
	"github.com/davitizhgenti/hostd/internal/secrets"
	"github.com/davitizhgenti/hostd/internal/systemd"
	"github.com/davitizhgenti/hostd/sdk"
)

// Host is what a module is built from besides its own settings. The daemon
// owns it: a module must not close what it shares.
type Host struct {
	// Directories: the runtime dir ($XDG_RUNTIME_DIR), the state dir, the
	// config dir (hostd.toml's folder), and the user's home.
	RuntimeDir, StateDir, ConfigDir, Home string
	// Socket is hostd's own API socket, for what a module starts that
	// calls back (scripts, git hooks).
	Socket string

	Log   *slog.Logger
	Clock clock.Clock
	// Secrets is the secrets store, shared by the modules that use secrets.
	Secrets secrets.Store
	// Systemd is the user's systemd, shared; the daemon closes it.
	Systemd *systemd.UserSystemd
}

// Builtin describes a module compiled into hostd.
type Builtin struct {
	Name string
	// Default: loaded when hostd.toml names no modules.
	Default bool
	// Settings returns a pointer to the module's settings with their
	// defaults (hostd.toml's [name] section is decoded into it, unknown
	// keys refused, then its Check method, if any, is called); nil when the
	// module has no settings.
	Settings func() any
	// New builds the module from what hostd provides and its settings (the
	// value Settings returned, filled in; nil without Settings).
	New func(h *Host, settings any) (sdk.Module, error)
}
