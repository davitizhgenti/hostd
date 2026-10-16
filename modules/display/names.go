package display

import "github.com/davitizhgenti/hostd/modules/display/wm"

// The compositor's types, from package wm (shared with the backends).
type (
	Window  = wm.Window
	Output  = wm.Output
	Mode    = wm.Mode
	Event   = wm.Event
	Backend = wm.Backend
)
