// Package wm holds what the display module and its compositor backend
// share: windows, screens, their events, and the Backend a compositor
// implements (Sway's is package sway). It depends on nothing of the
// module, so a backend can live in a package of its own.
package wm

import (
	"context"
	"fmt"
)

// Window is an application window.
type Window struct {
	ID         int64  `json:"id"`
	PID        int    `json:"pid"`
	AppID      string `json:"app_id,omitempty"` // Wayland app_id
	Class      string `json:"class,omitempty"`  // X11 class (Xwayland apps, Steam games)
	Title      string `json:"title"`
	Workspace  string `json:"workspace"`
	Output     string `json:"output,omitempty"`
	Focused    bool   `json:"focused"`
	Fullscreen bool   `json:"fullscreen"`
	// Dialog: a window that belongs to another (floating, or an X11
	// transient or dialog); it stays with that window.
	Dialog bool `json:"dialog,omitempty"`
}

// Output is a screen.
type Output struct {
	Name    string  `json:"name"`
	Make    string  `json:"make,omitempty"`
	Model   string  `json:"model,omitempty"`
	Width   int     `json:"width"`
	Height  int     `json:"height"`
	Refresh float64 `json:"refresh"` // Hz
	Active  bool    `json:"active"`
	Focused bool    `json:"focused"`
	Power   bool    `json:"power"`           // false: the screen is off (power saving)
	Modes   []Mode  `json:"modes,omitempty"` // what the screen supports
}

// Mode is a resolution and refresh rate.
type Mode struct {
	Width   int     `json:"width"`
	Height  int     `json:"height"`
	Refresh float64 `json:"refresh"` // Hz
}

func (m Mode) String() string { return fmt.Sprintf("%dx%d@%.3fHz", m.Width, m.Height, m.Refresh) }

// Event is something that happened in the session:
//   - a window change (Change is new, close, focus, title, fullscreen_mode,
//     move...; Window is set),
//   - "binding": one of hostd's key bindings fired (Binding is its input
//     name, e.g. "Super+Tab"),
//   - "reload": the compositor reloaded its config, which drops runtime
//     bindings.
type Event struct {
	Change  string
	Window  Window
	Binding string
}

// Backend is the compositor. The display module's logic only talks to this
// interface; Sway is the first implementation.
type Backend interface {
	Windows(ctx context.Context) ([]Window, error)
	Outputs(ctx context.Context) ([]Output, error)
	// Show switches the screen to a workspace.
	Show(ctx context.Context, workspace string) error
	// Move puts a window on a workspace.
	Move(ctx context.Context, window int64, workspace string) error
	Focus(ctx context.Context, window int64) error
	Fullscreen(ctx context.Context, window int64, on bool) error
	// CloseWindow asks a window to close, as its close button would.
	CloseWindow(ctx context.Context, window int64) error
	// Bind replaces hostd's key bindings. A fired binding is reported by
	// Watch as an Event with Change "binding".
	Bind(ctx context.Context, keys []string) error
	// Place puts a window next to another, both windowed, side by side.
	Place(ctx context.Context, window, beside int64) error
	// SetOutput changes a screen: output is a name or "*", setting one of
	// "power on|off", "enable", "disable", "mode WxH@RHz".
	SetOutput(ctx context.Context, output, setting string) error
	// Watch calls fn with window events until ctx ends or the compositor
	// goes away (then it returns an error).
	Watch(ctx context.Context, fn func(Event)) error
	// Disconnect drops the connection to the compositor.
	Disconnect() error
}
