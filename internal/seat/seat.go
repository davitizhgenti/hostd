// Package seat knows which user holds the seat (the screen, keyboard and
// controllers): logind's active session. hostd reads input only while its
// user's session is the active one, so a hostd that does not hold the seat
// takes nothing from the one that does.
package seat

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/davitizhgenti/hostd/internal/evdev"
)

// Active reports whether user's primary session is the active one of its
// seat. When logind cannot say (no session, no logind, a remote session
// that has no seat) the answer is yes: nothing then takes the seat away.
func Active(ctx context.Context, user string) bool {
	display := loginctl(ctx, "show-user", user, "-p", "Display", "--value")
	if display == "" {
		return true
	}
	switch loginctl(ctx, "show-session", display, "-p", "Active", "--value") {
	case "no":
		return false
	default:
		return true
	}
}

func loginctl(ctx context.Context, args ...string) string {
	out, err := exec.CommandContext(ctx, "loginctl", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Holder is the user whose session is active on seat0, "" if none is.
func Holder(ctx context.Context) string {
	id := loginctl(ctx, "show-seat", "seat0", "-p", "ActiveSession", "--value")
	if id == "" {
		return ""
	}
	return loginctl(ctx, "show-session", id, "-p", "Name", "--value")
}

// A Gate lets an Input read only while the user holds the seat. Devices and
// Grab pass through to Inner (a grab is kept until the devices are read again).
type Gate struct {
	Inner evdev.Input
	// User holds the seat when their session is the active one (Active).
	User string
	// Active replaces that check (tests).
	Active func(context.Context) bool
	// Every is how often it asks (default 2s).
	Every time.Duration
}

// Watch runs Inner's Watch while the seat is held. Losing the seat closes
// the devices (which also gives back any grab); getting it back opens them.
func (g *Gate) Watch(ctx context.Context, fn func(evdev.InputEvent)) error {
	every := g.Every
	if every == 0 {
		every = 2 * time.Second
	}
	active := g.Active
	if active == nil {
		active = func(ctx context.Context) bool { return Active(ctx, g.User) }
	}
	var stop func() // ends the reading, and waits for it
	defer func() {
		if stop != nil {
			stop()
		}
	}()
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		if active(ctx) {
			if stop == nil {
				stop = g.start(ctx, fn)
			}
		} else if stop != nil {
			stop()
			stop = nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// Grab passes through, when Inner can.
func (g *Gate) Grab(device string, on bool) error {
	if gr, ok := g.Inner.(evdev.Grabber); ok {
		return gr.Grab(device, on)
	}
	return nil
}

// Devices passes through, when Inner can.
func (g *Gate) Devices() map[string]int {
	if d, ok := g.Inner.(interface{ Devices() map[string]int }); ok {
		return d.Devices()
	}
	return nil
}

// start runs Inner's Watch in the background; the function it returns ends it.
func (g *Gate) start(ctx context.Context, fn func(evdev.InputEvent)) (stop func()) {
	c, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = g.Inner.Watch(c, fn) }()
	return func() { cancel(); <-done }
}
