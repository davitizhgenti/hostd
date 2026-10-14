package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/davitizhgenti/hostd/internal/client"
	"github.com/davitizhgenti/hostd/sdk"
)

// --- log ------------------------------------------------------------------

func (a *app) logCommand() *cobra.Command {
	var limit int
	var typ, action string
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Show recent actions and their sources (the audit trail)",
		Example: `  hostctl log
  hostctl log --type 'audio.*' --limit 20`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			q := url.Values{"limit": {strconv.Itoa(limit)}}
			if typ != "" {
				q.Set("type", typ)
			}
			if action != "" {
				q.Set("action", action)
			}
			recs, err := c.Audit(cmd.Context(), q)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(recs)
			}
			slices.Reverse(recs) // oldest first, like a log
			return writeTable(a.stdout, tableStyle{gap: 3, noHeader: true}, logColumns, recs)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "how many entries")
	cmd.Flags().StringVar(&typ, "type", "", "only these types, e.g. 'audio.*'")
	cmd.Flags().StringVar(&action, "action", "", "only this action ID")
	return cmd
}

// argSummary shows argument values compactly, in the order they were
// sent: {"lamp":"desk","brightness":40} becomes "desk 40".
func argSummary(raw json.RawMessage) string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return ""
	}
	var parts []string
	for dec.More() {
		if _, err := dec.Token(); err != nil { // key
			return strings.Join(parts, " ")
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return strings.Join(parts, " ")
		}
		var str string
		if json.Unmarshal(v, &str) == nil {
			parts = append(parts, str)
		} else {
			parts = append(parts, string(v))
		}
	}
	return strings.Join(parts, " ")
}

// logColumns are the columns of hostctl log: when, what (with its
// arguments, short), from whom, and how it went.
var logColumns = []column[client.AuditRecord]{
	{"TIME", func(r client.AuditRecord) string { return r.Time.In(displayZone).Format("15:04:05") }},
	{"ACTION", func(r client.AuditRecord) string {
		return strings.TrimSpace(r.Type + " " + shorten(argSummary(r.Args), 40))
	}},
	{"SOURCE", func(r client.AuditRecord) string { return r.Source.String() }},
	{"STATUS", func(r client.AuditRecord) string { return r.Status }},
	{"DETAIL", func(r client.AuditRecord) string {
		switch {
		case r.Status == "skipped" && r.Until != nil:
			return fmt.Sprintf("held by %s until %s", r.HeldBy, r.Until.In(displayZone).Format("15:04:05"))
		case r.Status == "skipped":
			return r.Reason
		case r.Status == "failed":
			return fmt.Sprintf("%s: %s", r.Code, shorten(r.Reason, 80))
		case r.Version > 0:
			return fmt.Sprintf("v%d", r.Version)
		}
		return ""
	}},
}

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
