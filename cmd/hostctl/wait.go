package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/davitizhgenti/hostd/internal/client"
	"github.com/davitizhgenti/hostd/sdk"
)

// submitDone sends an action and prints its result: as the action's Done
// lines say (its manifest's CLI hint), or its status and data.
func (a *app) submitDone(cmd *cobra.Command, req client.ActionRequest, done []string) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	res, err := c.Submit(cmd.Context(), req)
	if err != nil {
		return err
	}
	if a.jsonOut {
		return a.printJSON(res)
	}
	switch res.Status {
	case sdk.StatusSkipped:
		fmt.Fprintln(a.stdout, heldBy(res))
	case sdk.StatusAccepted:
		fmt.Fprintf(a.stdout, "accepted: %s (follow it with hostctl events)\n", res.Action)
	default:
		if line, ok := doneLine(done, res.Data); ok {
			fmt.Fprintln(a.stdout, line)
			return nil
		}
		line := string(res.Status)
		if res.Version > 0 {
			line += fmt.Sprintf(" (v%d)", res.Version)
		}
		if len(res.Data) > 0 {
			line += " " + string(res.Data)
		}
		fmt.Fprintln(a.stdout, line)
	}
	return nil
}

var rePlaceholder = regexp.MustCompile(`\{([a-z0-9_.]+)\}`)

// doneLine is the first of the Done lines whose condition holds for the
// result's data, with its {fields} filled in.
func doneLine(done []string, data json.RawMessage) (string, bool) {
	var d map[string]any
	if len(done) == 0 || json.Unmarshal(data, &d) != nil {
		return "", false
	}
	field := func(path string) any {
		var v any = d
		for _, k := range strings.Split(path, ".") {
			m, _ := v.(map[string]any)
			v = m[k]
		}
		return v
	}
	for _, line := range done {
		text, cond, _ := strings.Cut(line, " if ")
		if cond != "" {
			if v := field(cond); v == nil || v == false || v == "" {
				continue
			}
		}
		return rePlaceholder.ReplaceAllStringFunc(text, func(ph string) string {
			return render(field(ph[1:len(ph)-1]), "")
		}), true
	}
	return "", false
}

// submitAndWait sends an action whose result names an instance, waits
// until that instance ends, prints what it wrote and ends with its exit
// status (hostctl run --wait).
func (a *app) submitAndWait(cmd *cobra.Command, req client.ActionRequest) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	res, err := c.Submit(ctx, req)
	if err != nil {
		return err
	}
	if res.Status != sdk.StatusApplied {
		return fmt.Errorf("%s: %s", res.Status, heldBy(res))
	}
	var started struct{ Instance string }
	_ = json.Unmarshal(res.Data, &started)
	if started.Instance == "" {
		return errors.New("the result names no instance to wait for")
	}
	in, err := waitEnded(ctx, c, started.Instance, res.Action)
	if err != nil {
		return err
	}
	path := "/v1/instances/" + url.PathEscape(started.Instance)
	var logs struct{ Lines []string }
	if err := c.Get(ctx, path+"/logs?plain=true&lines=5000", &logs); err != nil {
		return err
	}
	if a.jsonOut {
		if err := a.printJSON(map[string]any{"instance": in, "lines": logs.Lines}); err != nil {
			return err
		}
	} else {
		for _, l := range logs.Lines {
			fmt.Fprintln(a.stdout, l)
		}
	}
	switch {
	case in.ExitCode != nil && *in.ExitCode != 0:
		return commandExit(*in.ExitCode)
	case in.Error != "":
		return fmt.Errorf("%s: %s", started.Instance, in.Error)
	}
	return nil
}

// waitEnded follows the event stream until the instance an action started
// has ended. The stream replays what the server kept, from before the
// action, so an instance that ended at once is not missed; an earlier
// instance of the same name is, as its events come before the action's.
// Without the stream (a token without read, an old server), it asks for
// the instance twice a second instead.
func waitEnded(ctx context.Context, c *client.Client, instance, action string) (instanceInfo, error) {
	var ended *instanceInfo
	ours := false
	err := c.Events(ctx, "instance.*", "start", func(ev sdk.Event) error {
		var in instanceInfo
		_ = json.Unmarshal(ev.Data, &in) // an event of another shape is not this instance's
		if in.ID != instance {
			return nil
		}
		if ev.Action == action {
			ours = true
		}
		if ours && (ev.Type == "instance.exited" || ev.Type == "instance.failed") {
			ended = &in
			return client.ErrStop
		}
		return nil
	})
	switch {
	case ended != nil:
		return *ended, nil
	case ctx.Err() != nil:
		return instanceInfo{}, fmt.Errorf("stopped waiting; %s keeps running (hostctl stop %s)", instance, instance)
	case err != nil:
		return pollEnded(ctx, c, instance)
	}
	return instanceInfo{}, errors.New("the event stream ended")
}

func pollEnded(ctx context.Context, c *client.Client, instance string) (instanceInfo, error) {
	path := "/v1/instances/" + url.PathEscape(instance)
	for {
		var in instanceInfo
		if err := c.Get(ctx, path, &in); err != nil {
			return in, err
		}
		if in.Ended != nil {
			return in, nil
		}
		select {
		case <-ctx.Done():
			return in, fmt.Errorf("stopped waiting; %s keeps running (hostctl stop %s)", instance, instance)
		case <-time.After(500 * time.Millisecond):
		}
	}
}
