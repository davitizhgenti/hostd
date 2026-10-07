package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
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
	"action": true, "apps": true, "help": true, "completion": true,
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
		a.eventsCommand(), a.actionCommand(), a.appsCommand())
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
	create.Flags().StringVar(&kind, "kind", "manual", "manual (a person's device), automation (scripts) or local (the on-screen switcher)")
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
				what := strings.TrimSpace(r.Type + " " + argSummary(r.Args))
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

func outcome(r client.AuditRecord) string {
	switch r.Status {
	case "skipped":
		if r.Until != nil {
			return fmt.Sprintf("skipped\theld by %s until %s", r.HeldBy, r.Until.Local().Format("15:04:05"))
		}
		return "skipped\t" + r.Reason
	case "failed":
		return fmt.Sprintf("failed\t%s: %s", r.Code, r.Reason)
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
