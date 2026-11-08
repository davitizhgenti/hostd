package audio

import (
	"github.com/davitizhgenti/hostd/internal/host"
	"github.com/davitizhgenti/hostd/sdk"
)

// Builtin is how hostd builds the audio module: WirePlumber and MPRIS.
var Builtin = host.Builtin{
	Name:    "audio",
	Default: true,
	New: func(h *host.Host, _ any) (sdk.Module, error) {
		return New(Options{Backend: &WirePlumber{}, Logger: h.Log,
			Media: &DBusMedia{RuntimeDir: h.RuntimeDir}}), nil
	},
}
