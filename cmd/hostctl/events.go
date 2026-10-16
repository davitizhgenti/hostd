package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/davitizhgenti/hostd/internal/client"
	"github.com/davitizhgenti/hostd/sdk"
)

// --- events ---------------------------------------------------------------

func (a *app) eventsCommand() *cobra.Command {
	var typ string
	var recent bool
	var count int
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Stream live events (Ctrl-C to stop)",
		Example: `  hostctl events
  hostctl events --type 'instance.*'
  hostctl events --recent --json | jq .
  hostctl events --type instance.started --count 1   # wait for the next start`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			since := ""
			if recent {
				since = "start"
			}
			seen := 0
			return c.Events(cmd.Context(), typ, since, func(ev sdk.Event) error {
				if err := a.printEvent(ev); err != nil {
					return err
				}
				if ev.Type != "bus.gap" && ev.Type != "bus.lagged" {
					seen++
				}
				if count > 0 && seen >= count {
					return client.ErrStop
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&typ, "type", "*", "only these event types, e.g. 'instance.*'")
	cmd.Flags().BoolVar(&recent, "recent", false, "first show the recent events the server still has (up to 1,000)")
	cmd.Flags().IntVar(&count, "count", 0, "exit after this many events")
	return cmd
}

func (a *app) printEvent(ev sdk.Event) error {
	if a.jsonOut {
		b, _ := json.Marshal(ev)
		_, err := fmt.Fprintln(a.stdout, string(b))
		return err
	}
	line := ev.Time.In(displayZone).Format("15:04:05") + " " + ev.Type
	if len(ev.Data) > 0 && string(ev.Data) != "null" {
		line += " " + string(ev.Data)
	}
	if ev.Source != nil {
		line += "  " + ev.Source.String()
	}
	if ev.Version > 0 {
		line += fmt.Sprintf(" v%d", ev.Version)
	}
	_, err := fmt.Fprintln(a.stdout, line)
	return err
}

// --- raw action -----------------------------------------------------------

func (a *app) actionCommand() *cobra.Command {
	var expect int64
	cmd := &cobra.Command{
		Use:     "action <type> [json-args]",
		Short:   "Send any action by type, with arguments as JSON",
		Example: `  hostctl action audio.volume.set '{"percent": 40}'`,
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := client.ActionRequest{Type: args[0]}
			if len(args) == 2 {
				if !json.Valid([]byte(args[1])) {
					return usageError{fmt.Errorf("arguments must be JSON, e.g. '{\"percent\": 40}'")}
				}
				req.Args = json.RawMessage(args[1])
			}
			if expect >= 0 {
				v := uint64(expect)
				req.ExpectVersion = &v
			}
			return a.submit(cmd, req)
		},
	}
	cmd.Flags().Int64Var(&expect, "expect-version", -1, "refuse if the resource is not at this version")
	return cmd
}

// submit sends an action and prints its result.
func (a *app) submit(cmd *cobra.Command, req client.ActionRequest) error {
	return a.submitDone(cmd, req, nil)
}
