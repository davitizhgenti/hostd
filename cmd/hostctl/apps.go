package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/davitizhgenti/hostd/internal/client"
	"github.com/davitizhgenti/hostd/sdk"
)

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

var appColumns = []column[appInfo]{
	{"ID", func(a appInfo) string { return a.ID }},
	{"NAME", func(a appInfo) string { return a.Name }},
	{"RUNNER", func(a appInfo) string { return a.Runner.Type }},
	{"SURFACE", func(a appInfo) string { return a.Surface }},
	{"FROM", func(a appInfo) string {
		if a.Hidden {
			return a.Source + " (hidden)"
		}
		return a.Source
	}},
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
			if len(args) == 1 {
				var one json.RawMessage
				if done, err := a.fetch(cmd, "/v1/apps/"+url.PathEscape(args[0]), &one); done || err != nil {
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
				Problems []problem `json:"problems"`
			}
			if done, err := a.fetch(cmd, path, &list); done || err != nil {
				return err
			}
			if err := writeTable(a.stdout, tableStyle{}, appColumns, list.Apps); err != nil {
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
			return a.act(cmd, "app.start", req, func(res sdk.Result) string {
				var d struct {
					Instance       string `json:"instance"`
					State          string `json:"state"`
					AlreadyRunning bool   `json:"already_running"`
				}
				_ = json.Unmarshal(res.Data, &d)
				if d.AlreadyRunning {
					return d.Instance + " is already running"
				}
				return fmt.Sprintf("started %s (%s)", d.Instance, d.State)
			})
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
			return a.act(cmd, "instance.stop", map[string]string{"id": args[0]},
				func(sdk.Result) string { return "stopped " + args[0] })
		},
	}
}

type instanceInfo struct {
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

var instanceColumns = []column[instanceInfo]{
	{"INSTANCE", func(in instanceInfo) string { return in.ID }},
	{"APP", func(in instanceInfo) string { return in.App }},
	{"RUNNER", func(in instanceInfo) string { return in.Runner }},
	{"STATE", func(in instanceInfo) string {
		if in.Ended != nil && in.ExitCode != nil {
			return fmt.Sprintf("%s (%d)", in.State, *in.ExitCode)
		}
		return in.State
	}},
	{"PID", func(in instanceInfo) string {
		if in.PID > 0 && in.Ended == nil {
			return strconv.Itoa(in.PID)
		}
		return "-"
	}},
	{"SINCE", func(in instanceInfo) string { // when it started, or ended
		t := in.Started
		if in.Ended != nil {
			t = *in.Ended
		}
		return t.In(displayZone).Format("15:04:05")
	}},
}

func (a *app) psCommand() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "ps",
		Short: "List running instances",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := "/v1/instances"
			if all {
				path += "?all=true"
			}
			var list []instanceInfo
			if done, err := a.fetch(cmd, path, &list); done || err != nil {
				return err
			}
			return writeTable(a.stdout, tableStyle{}, instanceColumns, list)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "also show instances that ended recently")
	return cmd
}

// act sends an action and says how it went: done(result) when it was
// carried out, who holds the resource when it was skipped, the result as
// JSON with --json.
func (a *app) act(cmd *cobra.Command, typ string, args any, done func(sdk.Result) string) error {
	res, err := a.submitQuiet(cmd, client.ActionRequest{Type: typ, Args: mustArgs(args)})
	if err != nil || a.jsonOut {
		return err
	}
	line := heldBy(res)
	if line == "" {
		line = done(res)
	}
	_, err = fmt.Fprintln(a.stdout, line)
	return err
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

// --- focus, controllers -------------------------------------------------------

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
			line := heldBy(res)
			switch {
			case res.Status == sdk.StatusSkipped && res.Reason == "in_use":
				line = "not switched: someone is using the screen (they got a notice; --front switches anyway)"
			case line == "":
				line = "focused " + args[0]
			}
			_, err = fmt.Fprintln(a.stdout, line)
			return err
		},
	}
	cmd.Flags().BoolVar(&front, "front", false, "switch even while someone is using the screen")
	return cmd
}

type controllerInfo struct {
	Device  string   `json:"device"`
	Name    string   `json:"name"`
	Vendor  string   `json:"vendor"`
	Product string   `json:"product"`
	Profile string   `json:"profile"`
	Kind    string   `json:"kind"`
	Buttons []string `json:"buttons"`
	Raw     bool     `json:"raw"`
	App     string   `json:"app"`
}

var controllerColumns = []column[controllerInfo]{
	{"DEVICE", func(x controllerInfo) string { return x.Device }},
	{"KIND", func(x controllerInfo) string { return x.Kind }},
	{"PROFILE", func(x controllerInfo) string { return x.Profile }},
	{"ID", func(x controllerInfo) string { return x.Vendor + ":" + x.Product }},
	{"BUTTONS", func(x controllerInfo) string {
		if x.Raw { // its app reads it; hostd binds nothing on it
			return "(read by " + x.App + ")"
		}
		return strings.Join(x.Buttons, ",")
	}},
	{"NAME", func(x controllerInfo) string { return x.Name }},
}

func (a *app) controllersCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "controllers",
		Short: "List connected game controllers and the buttons hostd knows on them",
		Long: `List connected game controllers: their profile (which family hostd takes
them for) and the buttons that can be bound in hostd.toml ([display.buttons]).
Profiles are built in, and ~/.config/hostd/controllers/*.toml adds more.
A raw device (a DolphinBar) is read only by its app.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list []controllerInfo
			if done, err := a.fetch(cmd, "/v1/controllers", &list); done || err != nil {
				return err
			}
			if len(list) == 0 {
				_, err := fmt.Fprintln(a.stdout, "no controllers connected")
				return err
			}
			return writeTable(a.stdout, tableStyle{}, controllerColumns, list)
		},
	}
}
