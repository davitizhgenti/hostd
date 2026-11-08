package deploy

import (
	"path/filepath"

	"github.com/davitizhgenti/hostd/internal/host"
	"github.com/davitizhgenti/hostd/sdk"
)

// Builtin is how hostd builds the deploy module.
var Builtin = host.Builtin{
	Name:    "deploy",
	Default: true,
	New: func(h *host.Host, _ any) (sdk.Module, error) {
		return New(Options{Logger: h.Log, SecretsDir: h.Secrets.Dir, AppsDir: filepath.Join(h.ConfigDir, "apps")}), nil
	},
}
