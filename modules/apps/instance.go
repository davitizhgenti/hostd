package apps

import (
	"fmt"
	"strconv"

	"github.com/davitizhgenti/hostd/modules/apps/spec"
)

// change is something that happens to an instance.
type change int

const (
	changeStarted    change = iota // the runner confirmed it runs
	changeStartError               // the runner could not start it
	changeStop                     // hostd asked it to stop
	changeEnded                    // its process ended (exit code known or not)
)

// next applies a change to a state. An instance that ends with a non-zero
// exit code has failed, unless hostd asked it to stop.
func next(cur State, c change, exitCode int) (State, error) {
	switch {
	case cur.Ended():
		return cur, fmt.Errorf("instance already %s", cur)
	case c == changeStarted && cur == StateStarting:
		return StateRunning, nil
	case c == changeStarted:
		return cur, nil // late confirmation; nothing to do
	case c == changeStartError && cur == StateStarting:
		return StateFailed, nil
	case c == changeStartError:
		return cur, fmt.Errorf("start error while %s", cur)
	case c == changeStop:
		return StateStopping, nil
	case c == changeEnded && cur == StateStopping:
		return StateExited, nil
	case c == changeEnded && exitCode == 0:
		return StateExited, nil
	case c == changeEnded:
		return StateFailed, nil
	}
	return cur, fmt.Errorf("unknown change %d", c)
}

// nextID picks an instance ID for app: the app ID itself if free, then
// app#2, app#3... the lowest number not in use by a live instance.
func nextID(app string, inUse func(string) bool) string {
	if !inUse(app) {
		return app
	}
	for n := 2; ; n++ {
		if id := app + "#" + strconv.Itoa(n); !inUse(id) {
			return id
		}
	}
}

// appOf returns the app part of an instance ID.
func appOf(instanceID string) string { return spec.AppOf(instanceID) }
