package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/davitizhgenti/hostd/internal/client"
	"github.com/davitizhgenti/hostd/internal/version"
	"github.com/davitizhgenti/hostd/sdk"
)

// builtins are the top-level commands hostctl always has; anything else is
// looked up in the server's manifests.
var builtins = map[string]bool{
	"login": true, "version": true, "token": true, "log": true, "events": true,
	"action": true, "apps": true, "start": true, "stop": true, "ps": true, "focus": true, "windows": true,
	"volume": true, "mute": true, "update": true,
	"help": true, "completion": true,
}

func (a *app) rootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "hostctl",
		Short: "Control a hostd machine",
		Long: `hostctl controls a hostd machine: apps, windows, audio, services.

Module commands come from the server: hostctl <namespace> <action> [args],
e.g. hostctl audio volume set 40. Run hostctl --help while connected to
see them all.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().BoolVar(&a.jsonOut, "json", false, "print JSON instead of text")
	root.PersistentFlags().StringVar(&a.url, "url", "", "hostd address, overriding the config (unix:///path or host:port)")
	root.AddCommand(a.loginCommand(), a.versionCommand(), a.tokenCommand(), a.logCommand(),
		a.eventsCommand(), a.actionCommand(), a.appsCommand(), a.startCommand(), a.stopCommand(), a.psCommand(),
		a.focusCommand(), a.windowsCommand(), a.volumeCommand(), a.muteCommand(), a.updateCommand())
	return root
}

// needsModules reports whether the command line names something that is
// not built in, so the module commands must be fetched first. Help without
// a command fetches them too, to list them, but tolerates failure.
func (a *app) needsModules(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--url":
			i++ // skip its value
		case arg == "--json" || strings.HasPrefix(arg, "--url="):
		case strings.HasPrefix(arg, "-"):
			return isHelp([]string{arg})
		default:
			return !builtins[arg]
		}
	}
	return false
}

// --- login ----------------------------------------------------------------

func (a *app) loginCommand() *cobra.Command {
	var tokenFile string
	cmd := &cobra.Command{
		Use:   "login <address>",
		Short: "Save the address and token of a hostd machine",
		Long: `Save the address and token of a hostd machine in ~/.config/hostctl.

The address is host:port (port 7300) or unix:///path/to/hostd.sock. The token
is read from --token-file, or from standard input: paste the admin token
that hostd printed on its first start, or one made with hostctl token create.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			addr := args[0]
			if !strings.Contains(addr, "://") && !strings.Contains(addr, ":") {
				addr += ":7300"
			}
			var token string
			if tokenFile != "" {
				b, err := os.ReadFile(tokenFile)
				if err != nil {
					return err
				}
				token = strings.TrimSpace(string(b))
			} else {
				if f, ok := a.stdin.(*os.File); ok && isTerminal(f) {
					fmt.Fprint(a.stderr, "Token: ")
				}
				line, err := bufio.NewReader(a.stdin).ReadString('\n')
				if err != nil && !errors.Is(err, io.EOF) {
					return err
				}
				token = strings.TrimSpace(line)
			}
			if token == "" {
				return usageError{errors.New("no token given")}
			}
			c, err := client.New(addr, token)
			if err != nil {
				return usageError{err}
			}
			v, err := c.Version(cmd.Context())
			if err != nil {
				return err
			}
			path, err := saveConfig(Config{URL: addr, Token: token})
			if err != nil {
				return err
			}
			fmt.Fprintf(a.stdout, "Logged in to hostd %s at %s (saved in %s)\n", v.Version, addr, path)
			return nil
		},
	}
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "read the token from this file")
	return cmd
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// --- version --------------------------------------------------------------

func (a *app) versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the versions of hostctl and the connected hostd",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintf(a.stdout, "hostctl %s\n", version.Version)
			c, err := a.client()
			if err != nil {
				return err
			}
			v, err := c.Version(cmd.Context())
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(v)
			}
			dev := ""
			if v.Dev {
				dev = " (dev build)"
			}
			fmt.Fprintf(a.stdout, "hostd   %s%s, %s/%s\n", v.Version, dev, v.OS, v.Arch)
			return nil
		},
	}
}

// --- tokens ---------------------------------------------------------------

func (a *app) tokenCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "token", Short: "Create, list and revoke API tokens", Args: cobra.ArbitraryArgs, RunE: groupRun}

	var scopes []string
	var kind, ttl string
	create := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a token; its secret is shown only once",
		Example: `  hostctl token create phone --scopes read,apps,audio
  hostctl token create backup.sh --kind automation --scopes read,services`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			t, err := c.CreateToken(cmd.Context(), client.TokenRequest{
				Name: args[0], Kind: sdk.SourceKind(kind), Scopes: scopes, TTL: ttl})
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(t)
			}
			fmt.Fprintf(a.stdout, "Created token %q (%s, scopes %s). Its secret, shown only now:\n\n    %s\n",
				t.Name, t.Kind, strings.Join(t.Scopes, ","), t.Secret)
			return nil
		},
	}
	create.Flags().StringSliceVar(&scopes, "scopes", nil, "scopes, comma-separated (required)")
	create.Flags().StringVar(&kind, "kind", "manual", "manual (a person's device), automation (scripts) or local (the on-screen menu)")
	create.Flags().StringVar(&ttl, "ttl", "", "lifetime such as 1h; default never expires")
	_ = create.MarkFlagRequired("scopes")

	list := &cobra.Command{
		Use:   "list",
		Short: "List tokens",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			toks, err := c.Tokens(cmd.Context())
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(toks)
			}
			tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tKIND\tSCOPES\tLAST USED\tSTATE\tID")
			for _, t := range toks {
				state := "active"
				switch {
				case t.Revoked != nil:
					state = "revoked"
				case t.Expires != nil && !time.Now().Before(*t.Expires):
					state = "expired"
				case t.Expires != nil:
					state = "expires " + t.Expires.Local().Format("2006-01-02 15:04")
				}
				used := "never"
				if t.LastUsed != nil {
					used = t.LastUsed.Local().Format("2006-01-02 15:04")
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", t.Name, t.Kind, strings.Join(t.Scopes, ","), used, state, t.ID)
			}
			return tw.Flush()
		},
	}

	revoke := &cobra.Command{
		Use:   "revoke <name or id>",
		Short: "Revoke a token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id := args[0]
			if !strings.HasPrefix(id, "tok_") {
				toks, err := c.Tokens(cmd.Context())
				if err != nil {
					return err
				}
				id = ""
				for _, t := range toks {
					if t.Name == args[0] && t.Revoked == nil {
						id = t.ID
					}
				}
				if id == "" {
					return &client.Error{Err: sdk.Errorf(sdk.CodeNotFound, "no active token named %q", args[0])}
				}
			}
			if err := c.RevokeToken(cmd.Context(), id); err != nil {
				return err
			}
			fmt.Fprintf(a.stdout, "Revoked %s\n", args[0])
			return nil
		},
	}
	cmd.AddCommand(create, list, revoke)
	return cmd
}

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
			// Oldest first, like a log.
			tw := tabwriter.NewWriter(a.stdout, 0, 0, 3, ' ', 0)
			for i := len(recs) - 1; i >= 0; i-- {
				r := recs[i]
				what := strings.TrimSpace(r.Type + " " + shorten(argSummary(r.Args), 40))
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Time.Local().Format("15:04:05"), what, r.Source, outcome(r))
			}
			return tw.Flush()
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

// shorten cuts s to n runes, marking the cut with "…".
func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func outcome(r client.AuditRecord) string {
	switch r.Status {
	case "skipped":
		if r.Until != nil {
			return fmt.Sprintf("skipped\theld by %s until %s", r.HeldBy, r.Until.Local().Format("15:04:05"))
		}
		return "skipped\t" + r.Reason
	case "failed":
		return fmt.Sprintf("failed\t%s: %s", r.Code, shorten(r.Reason, 80))
	default:
		if r.Version > 0 {
			return fmt.Sprintf("%s\tv%d", r.Status, r.Version)
		}
		return r.Status + "\t"
	}
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
	line := ev.Time.Local().Format("15:04:05") + " " + ev.Type
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
		until := ""
		if res.Until != nil {
			until = " until " + res.Until.Local().Format("15:04:05")
		}
		fmt.Fprintf(a.stdout, "skipped: held by %s%s\n", res.HeldBy, until)
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

func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// --- apps -------------------------------------------------------------------

type appInfo struct {
	ID      string   `json:"id"`
	Aliases []string `json:"aliases"`
	Name    string   `json:"name"`
	Source  string   `json:"source"`
	Runner  struct {
		Type string `json:"type"`
	} `json:"runner"`
	Surface string `json:"surface"`
	Hidden  bool   `json:"hidden"`
}

func (a *app) appsCommand() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "apps [id]",
		Short: "List the app catalog, or show one app",
		Example: `  hostctl apps
  hostctl apps --all      # include hidden apps (settings, terminals...)
  hostctl apps firefox`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				var one json.RawMessage
				if err := c.Get(cmd.Context(), "/v1/apps/"+url.PathEscape(args[0]), &one); err != nil {
					return err
				}
				var pretty bytes.Buffer
				_ = json.Indent(&pretty, one, "", "  ")
				_, err := fmt.Fprintln(a.stdout, pretty.String())
				return err
			}
			path := "/v1/apps"
			if all {
				path += "?all=true"
			}
			var list struct {
				Apps     []appInfo `json:"apps"`
				Problems []struct {
					File  string `json:"file"`
					Error string `json:"error"`
				} `json:"problems"`
			}
			if err := c.Get(cmd.Context(), path, &list); err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(list)
			}
			tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tRUNNER\tSURFACE\tFROM")
			for _, ap := range list.Apps {
				from := ap.Source
				if ap.Hidden {
					from += " (hidden)"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", ap.ID, ap.Name, ap.Runner.Type, ap.Surface, from)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			if n := len(list.Problems); n > 0 {
				fmt.Fprintf(a.stderr, "\n%d app file(s) could not be used:\n", n)
				for _, p := range list.Problems {
					fmt.Fprintf(a.stderr, "  %s: %s\n", p.File, p.Error)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include hidden apps")
	return cmd
}

// --- start, stop, ps ----------------------------------------------------------

func (a *app) startCommand() *cobra.Command {
	var front bool
	var action string
	cmd := &cobra.Command{
		Use:   "start <app>",
		Short: "Start an app (if it already runs: focus it, start another copy, or restart it, per its settings)",
		Long: `Start an app. While someone is using the screen, an app started from
elsewhere opens in the background and a notice says it is ready; --front
brings it to the front anyway (needs the display.front scope). --action
starts one of the app's actions (see hostctl apps <app>) as a new instance.`,
		Example: `  hostctl start foot
  hostctl start jellyfin --front
  hostctl start chromium --action new-private-window`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := map[string]any{"id": args[0]}
			if front {
				req["front"] = true
			}
			if action != "" {
				req["action"] = action
			}
			res, err := a.submitQuiet(cmd, client.ActionRequest{Type: "app.start", Args: mustArgs(req)})
			if err != nil || a.jsonOut {
				return err
			}
			var d struct {
				Instance       string `json:"instance"`
				State          string `json:"state"`
				AlreadyRunning bool   `json:"already_running"`
			}
			_ = json.Unmarshal(res.Data, &d)
			switch {
			case res.Status == sdk.StatusSkipped:
				fmt.Fprintf(a.stdout, "skipped: held by %s\n", res.HeldBy)
			case d.AlreadyRunning:
				fmt.Fprintf(a.stdout, "%s is already running\n", d.Instance)
			default:
				fmt.Fprintf(a.stdout, "started %s (%s)\n", d.Instance, d.State)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&front, "front", false, "open in front even while someone is using the screen")
	cmd.Flags().StringVar(&action, "action", "", "start one of the app's actions, as a new instance")
	return cmd
}

func (a *app) stopCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "stop <instance>",
		Short:   "Stop a running instance (see hostctl ps)",
		Example: "  hostctl stop foot\n  hostctl stop firefox#2",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := a.submitQuiet(cmd, client.ActionRequest{Type: "instance.stop", Args: mustArgs(map[string]string{"id": args[0]})})
			if err != nil || a.jsonOut {
				return err
			}
			if res.Status == sdk.StatusSkipped {
				fmt.Fprintf(a.stdout, "skipped: held by %s\n", res.HeldBy)
				return nil
			}
			fmt.Fprintf(a.stdout, "stopped %s\n", args[0])
			return nil
		},
	}
}

func (a *app) psCommand() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "ps",
		Short: "List running instances",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			path := "/v1/instances"
			if all {
				path += "?all=true"
			}
			var list []struct {
				ID       string     `json:"id"`
				App      string     `json:"app"`
				Runner   string     `json:"runner"`
				State    string     `json:"state"`
				PID      int        `json:"pid"`
				Started  time.Time  `json:"started"`
				Ended    *time.Time `json:"ended"`
				ExitCode *int       `json:"exit_code"`
				Error    string     `json:"error"`
			}
			if err := c.Get(cmd.Context(), path, &list); err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(list)
			}
			tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "INSTANCE\tAPP\tRUNNER\tSTATE\tPID\tSINCE")
			for _, in := range list {
				since := in.Started
				state := in.State
				if in.Ended != nil {
					since = *in.Ended
					if in.ExitCode != nil {
						state += fmt.Sprintf(" (%d)", *in.ExitCode)
					}
				}
				pid := "-"
				if in.PID > 0 && in.Ended == nil {
					pid = strconv.Itoa(in.PID)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", in.ID, in.App, in.Runner, state, pid, since.Local().Format("15:04:05"))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "also show instances that ended recently")
	return cmd
}

// submitQuiet sends an action and returns its result without printing it.
func (a *app) submitQuiet(cmd *cobra.Command, req client.ActionRequest) (sdk.Result, error) {
	c, err := a.client()
	if err != nil {
		return sdk.Result{}, err
	}
	res, err := c.Submit(cmd.Context(), req)
	if err == nil && a.jsonOut {
		err = a.printJSON(res)
	}
	return res, err
}

func mustArgs(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// --- focus, windows -----------------------------------------------------------

func (a *app) focusCommand() *cobra.Command {
	var front bool
	cmd := &cobra.Command{
		Use:   "focus <instance>",
		Short: "Bring an instance's window to the front",
		Long: `Bring an instance's window to the front. While someone is using the
screen it stays put and they get a notice instead; --front switches anyway
(needs the display.front scope).`,
		Example: "  hostctl focus firefox\n  hostctl focus firefox#2 --front",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := map[string]any{"instance": args[0]}
			if front {
				req["front"] = true
			}
			res, err := a.submitQuiet(cmd, client.ActionRequest{Type: "window.focus", Args: mustArgs(req)})
			if err != nil || a.jsonOut {
				return err
			}
			switch {
			case res.Status == sdk.StatusSkipped && res.Reason == "in_use":
				fmt.Fprintf(a.stdout, "not switched: someone is using the screen (they got a notice; --front switches anyway)\n")
			case res.Status == sdk.StatusSkipped:
				fmt.Fprintf(a.stdout, "skipped: held by %s\n", res.HeldBy)
			default:
				fmt.Fprintf(a.stdout, "focused %s\n", args[0])
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&front, "front", false, "switch even while someone is using the screen")
	return cmd
}

func (a *app) windowsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "windows",
		Short: "List all windows on the screen, including ones hostd did not start",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var list []struct {
				ID         int64  `json:"id"`
				Instance   string `json:"instance"`
				AppID      string `json:"app_id"`
				Class      string `json:"class"`
				Title      string `json:"title"`
				Workspace  string `json:"workspace"`
				Focused    bool   `json:"focused"`
				Fullscreen bool   `json:"fullscreen"`
			}
			if err := c.Get(cmd.Context(), "/v1/windows", &list); err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(list)
			}
			tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "INSTANCE\tAPP ID\tWORKSPACE\tFOCUSED\tFULLSCREEN\tTITLE")
			for _, w := range list {
				inst := w.Instance
				if inst == "" {
					inst = "-"
				}
				app := w.AppID
				if app == "" {
					app = w.Class
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%v\t%v\t%s\n", inst, app, w.Workspace, w.Focused, w.Fullscreen, shorten(w.Title, 40))
			}
			return tw.Flush()
		},
	}
}

// --- volume, mute -------------------------------------------------------------

func (a *app) printAudio(data json.RawMessage) error {
	var st struct {
		Percent     int    `json:"percent"`
		Muted       bool   `json:"muted"`
		Description string `json:"description"`
		Sink        string `json:"sink"`
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return err
	}
	out := st.Description
	if out == "" {
		out = st.Sink
	}
	muted := ""
	if st.Muted {
		muted = " (muted)"
	}
	_, err := fmt.Fprintf(a.stdout, "volume %d%%%s on %s\n", st.Percent, muted, out)
	return err
}

func (a *app) volumeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "volume [percent | +n | -n]",
		Short: "Show the volume, or set it (0-150) or change it (+5, -5)",
		Example: `  hostctl volume
  hostctl volume 40
  hostctl volume -5`,
		// "-5" must reach us as a value, not as an unknown flag.
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 || (len(args) == 1 && (args[0] == "-h" || args[0] == "--help")) {
				return cmd.Help()
			}
			if len(args) == 0 {
				c, err := a.client()
				if err != nil {
					return err
				}
				var st json.RawMessage
				if err := c.Get(cmd.Context(), "/v1/audio", &st); err != nil {
					return err
				}
				return a.printAudio(st)
			}
			v := args[0]
			arg, _ := json.Marshal(v)
			if n, err := strconv.Atoi(v); err == nil && !strings.HasPrefix(v, "+") && !strings.HasPrefix(v, "-") {
				arg, _ = json.Marshal(n)
			}
			res, err := a.submitQuiet(cmd, client.ActionRequest{Type: "audio.volume.set",
				Args: json.RawMessage(`{"percent":` + string(arg) + `}`)})
			if err != nil {
				return err
			}
			if res.Status == sdk.StatusSkipped {
				fmt.Fprintf(a.stdout, "skipped: held by %s\n", res.HeldBy)
				return nil
			}
			return a.printAudio(res.Data)
		},
	}
}

func (a *app) muteCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "mute [on | off | toggle]",
		Short:   "Mute or unmute (toggle by default)",
		Example: "  hostctl mute\n  hostctl mute off",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			val := `"toggle"`
			if len(args) == 1 {
				switch args[0] {
				case "on":
					val = "true"
				case "off":
					val = "false"
				case "toggle":
				default:
					return usageError{fmt.Errorf("use on, off or toggle, not %q", args[0])}
				}
			}
			res, err := a.submitQuiet(cmd, client.ActionRequest{Type: "audio.mute.set", Args: json.RawMessage(`{"muted":` + val + `}`)})
			if err != nil || a.jsonOut {
				return err
			}
			if res.Status == sdk.StatusSkipped {
				fmt.Fprintf(a.stdout, "skipped: held by %s\n", res.HeldBy)
				return nil
			}
			return a.printAudio(res.Data)
		},
	}
}

// --- update -------------------------------------------------------------------

func (a *app) updateCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "update", Short: "Update hostd on the machine", Args: cobra.ArbitraryArgs, RunE: groupRun}
	var sshTarget string
	push := &cobra.Command{
		Use:   "push [binary]",
		Short: "Install a hostd build on the machine and restart into it (rolled back if it does not start)",
		Long: `Install a hostd build on the machine and restart into it.

Without a binary, builds hostd from the source checkout you are in, for the
machine's architecture (needs Go). The machine keeps the previous version:
if the new one does not start, systemd switches back to it on its own.

--ssh user@machine copies the binary over SSH and installs it there, for
when the running hostd is too broken to take an upload.`,
		Example: `  hostctl update push
  hostctl update push ./dist/hostd-linux-amd64
  hostctl update push --ssh screen@192.168.1.20`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil && sshTarget == "" {
				return err
			}
			path := ""
			if len(args) == 1 {
				path = args[0]
			} else {
				arch := "amd64"
				if c != nil {
					if v, err := c.Version(cmd.Context()); err == nil {
						arch = v.Arch
					}
				}
				if path, err = buildHostd(cmd.Context(), arch, a.stderr); err != nil {
					return err
				}
				defer os.Remove(path)
			}
			if sshTarget != "" {
				return a.pushOverSSH(cmd.Context(), sshTarget, path)
			}
			return a.pushOverAPI(cmd.Context(), c, path)
		},
	}
	push.Flags().StringVar(&sshTarget, "ssh", "", "install over SSH instead of the API, e.g. screen@192.168.1.20")
	cmd.AddCommand(push)
	return cmd
}

// buildHostd builds hostd for linux/<arch> from the checkout in the
// current directory.
func buildHostd(ctx context.Context, arch string, stderr io.Writer) (string, error) {
	if _, err := os.Stat("cmd/hostd"); err != nil {
		return "", usageError{errors.New("no binary given and not in a hostd source checkout (cmd/hostd not found)")}
	}
	ver := "dev"
	if out, err := exec.CommandContext(ctx, "git", "describe", "--tags", "--always", "--dirty").Output(); err == nil {
		ver = strings.TrimSpace(string(out)) + "-dev"
	}
	out, err := os.CreateTemp("", "hostd-push-*")
	if err != nil {
		return "", err
	}
	out.Close()
	fmt.Fprintf(stderr, "building hostd %s for linux/%s...\n", ver, arch)
	build := exec.CommandContext(ctx, "go", "build", "-trimpath",
		"-ldflags", "-X github.com/davitizhgenti/hostd/internal/version.Version="+ver,
		"-o", out.Name(), "./cmd/hostd")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
	build.Stdout, build.Stderr = stderr, stderr
	if err := build.Run(); err != nil {
		os.Remove(out.Name())
		return "", fmt.Errorf("go build: %w", err)
	}
	return out.Name(), nil
}

func (a *app) pushOverAPI(ctx context.Context, c *client.Client, path string) error {
	before, err := c.Version(ctx)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintf(a.stderr, "uploading %s...\n", path)
	res, err := c.Update(ctx, f)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "installed hostd %s (was %s); restarting\n", res.Version, before.Version)

	// Wait for the new version to answer, or for systemd to roll back.
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		v, err := c.Version(ctx)
		if err != nil {
			continue // restarting
		}
		switch {
		case v.Version == res.Version:
			fmt.Fprintf(a.stdout, "hostd %s is running\n", v.Version)
			return nil
		case v.RolledBackFrom == res.Version:
			return &client.Error{Err: sdk.Errorf(sdk.CodeInternal,
				"hostd %s did not start; systemd rolled back to %s", res.Version, v.Version)}
		}
	}
	return &client.Error{Err: sdk.Errorf(sdk.CodeTimeout, "hostd did not come back within 3 minutes")}
}

func (a *app) pushOverSSH(ctx context.Context, target, path string) error {
	remote := "/tmp/hostd-push"
	fmt.Fprintf(a.stderr, "copying %s to %s...\n", path, target)
	scp := exec.CommandContext(ctx, "scp", "-q", path, target+":"+remote)
	scp.Stdout, scp.Stderr = a.stderr, a.stderr
	if err := scp.Run(); err != nil {
		return fmt.Errorf("scp: %w", err)
	}
	run := exec.CommandContext(ctx, "ssh", target,
		"chmod +x "+remote+" && ~/.local/lib/hostd/current/hostd -install "+remote+"; status=$?; rm -f "+remote+"; exit $status")
	run.Stdout, run.Stderr = a.stdout, a.stderr
	return run.Run()
}
