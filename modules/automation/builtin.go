package automation

import (
	"github.com/davitizhgenti/hostd/internal/host"
	"github.com/davitizhgenti/hostd/sdk"
)

// Builtin is how hostd builds the automation module: its scripts call
// hostd back on its socket.
var Builtin = host.Builtin{
	Name:    "automation",
	Default: true,
	New: func(h *host.Host, _ any) (sdk.Module, error) {
		return New(Options{Logger: h.Log, URL: "unix://" + h.Socket}), nil
	},
}
