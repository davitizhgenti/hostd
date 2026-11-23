package units

import (
	"context"
	"fmt"
	"syscall"
	"time"

	"github.com/davitizhgenti/hostd/internal/execx"
	"github.com/davitizhgenti/hostd/internal/proc"
	"github.com/davitizhgenti/hostd/internal/systemd"
)

// A handoff app's command passes the app to another program and exits
// (runner.handoff, e.g. "steam steam://rungameid/620" with
// handoff = "SteamAppId=620"). Its instance runs while processes with the
// handoff variable exist. The unit is kept (exited) until then, so a
// restarted hostd finds the instance again from it.

type handoff struct {
	kv       string    // KEY=value
	since    time.Time // when the command was started
	seen     bool      // the app's processes were seen
	unitDone bool      // the handing-off command has exited
}

func (r *ExecRunner) procRoot() string { return proc.Root(r.ProcRoot) }

// trackHandoff starts following a handed-off instance.
func (r *ExecRunner) trackHandoff(id, kv string, unitDone bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handoffDefaults()
	r.handoffs[id] = &handoff{kv: kv, since: r.clock().Now(), unitDone: unitDone}
}

// handedOff marks an instance's command as done handing off, and reports
// whether the instance is followed by its variable.
func (r *ExecRunner) handedOff(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.handoffs[id]
	if ok {
		h.unitDone = true
	}
	return ok
}

// handoffOf returns an instance's handoff variable, from what hostd
// follows or from its unit's description.
func (r *ExecRunner) handoffOf(id string, u systemd.UnitInfo) string {
	r.mu.Lock()
	h, ok := r.handoffs[id]
	r.mu.Unlock()
	if ok {
		return h.kv
	}
	if m := reDescription.FindStringSubmatch(u.Description); m != nil {
		return m[3]
	}
	return ""
}

func (r *ExecRunner) runCommand(ctx context.Context, argv, env []string) error {
	run := r.Exec
	if run == nil {
		run = execx.OS{}
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	_, err := run.Run(ctx, execx.Cmd{Argv: argv, Env: env, Dir: r.HomeDir})
	return err
}

func (r *ExecRunner) handoffDefaults() {
	if r.PollEvery == 0 {
		r.PollEvery = 2 * time.Second
	}
	if r.StartGrace == 0 {
		r.StartGrace = 3 * time.Minute
	}
	if r.StopWait == 0 {
		r.StopWait = 10 * time.Second
	}
	if r.Kill == nil {
		r.Kill = syscall.Kill
	}
	if r.handoffs == nil {
		r.handoffs = map[string]*handoff{}
	}
}

// pollHandoffs reports handed-off instances whose processes are gone, or
// never appeared within StartGrace.
func (r *ExecRunner) pollHandoffs(ctx context.Context, fn func(Ended)) {
	r.mu.Lock()
	every := r.PollEvery
	r.mu.Unlock()
	t := r.clock().NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C():
		}
		type done struct {
			Ended
			unit string
		}
		var ended []done
		r.mu.Lock()
		for id, h := range r.handoffs {
			alive := len(proc.WithEnv(r.procRoot(), h.kv)) > 0
			switch {
			case alive:
				h.seen = true
			case h.seen:
				ended = append(ended, done{Ended{Instance: id, Reason: "the app's processes ended"}, unitName(id)})
				delete(r.handoffs, id)
			case r.clock().Since(h.since) > r.StartGrace:
				ended = append(ended, done{Ended{Instance: id, ExitCode: 1,
					Reason: fmt.Sprintf("no process with %s appeared within %s", h.kv, r.StartGrace)}, unitName(id)})
				delete(r.handoffs, id)
			}
		}
		r.mu.Unlock()
		for _, e := range ended {
			// A unit that still runs hosts what it handed off to (Steam):
			// it stays. An exited one is cleaned up.
			if info, ok, err := r.Systemd.Unit(ctx, e.unit); err == nil && ok && info.SubState != "running" {
				r.cleanup(ctx, info)
			}
			fn(e.Ended)
		}
	}
}

// stopHandoff ends the app's processes, politely first.
func (r *ExecRunner) stopHandoff(ctx context.Context, kv string) error {
	r.mu.Lock()
	r.handoffDefaults()
	kill, wait := r.Kill, r.StopWait
	r.mu.Unlock()
	for _, p := range proc.WithEnv(r.procRoot(), kv) {
		_ = kill(p, syscall.SIGTERM)
	}
	deadline := r.clock().NewTimer(wait)
	defer deadline.Stop()
	tick := r.clock().NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for len(proc.WithEnv(r.procRoot(), kv)) > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C():
			for _, p := range proc.WithEnv(r.procRoot(), kv) {
				_ = kill(p, syscall.SIGKILL)
			}
			return nil
		case <-tick.C():
		}
	}
	return nil
}

// HandoffsRunning reports which of these handoff variables processes
// have: apps that run though hostd did not start them (a game started
// from Steam's own menu). One pass over the processes for all of them.
func (r *ExecRunner) HandoffsRunning(kvs []string) map[string]bool {
	return proc.EnvsRunning(r.procRoot(), kvs)
}

// FollowHandoff follows an instance hostd did not start by its handoff
// variable: it ends when no process has it any more.
func (r *ExecRunner) FollowHandoff(id, kv string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handoffDefaults()
	r.handoffs[id] = &handoff{kv: kv, since: r.clock().Now(), seen: true, unitDone: true}
}
