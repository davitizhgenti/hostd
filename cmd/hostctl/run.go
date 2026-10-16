package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/davitizhgenti/hostd/internal/client"
	"github.com/davitizhgenti/hostd/sdk"
)

// --- run ----------------------------------------------------------------------

func (a *app) runCommand() *cobra.Command {
	var name, dir string
	var env []string
	var background, windowed, front, wait bool
	cmd := &cobra.Command{
		Use:   "run [flags] -- <command> [args...]",
		Short: "Run a command on the machine now: a window on the screen, or in the background",
		Long: `Run a command on the machine as the screen user, without an app file: a
full-screen window by default, or a background process. It is an instance
like any other (hostctl ps, hostctl stop, its logs). Needs the run scope:
a token with it can run anything as the screen user.

--wait waits until it ends, prints what it wrote, and exits with its exit
status. Ctrl-C stops waiting; the command keeps running.`,
		Example: `  hostctl run -- imv -f /home/screen/plot.png
  hostctl run --windowed -- foot
  hostctl run --background --name tests --wait -- pytest -q /home/screen/proj`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := map[string]any{"command": args}
			if name != "" {
				req["name"] = name
			}
			if background {
				req["background"] = true
			}
			if windowed {
				req["fullscreen"] = false
			}
			if front {
				req["front"] = true
			}
			if dir != "" {
				req["dir"] = dir
			}
			if len(env) > 0 {
				vars := map[string]string{}
				for _, kv := range env {
					k, v, ok := strings.Cut(kv, "=")
					if !ok || k == "" {
						return usageError{fmt.Errorf("--env %q: use NAME=value", kv)}
					}
					vars[k] = v
				}
				req["env"] = vars
			}
			if !wait {
				return a.act(cmd, "app.run", req, func(res sdk.Result) string {
					var d struct{ Instance, State string }
					_ = json.Unmarshal(res.Data, &d)
					return fmt.Sprintf("started %s (%s)", d.Instance, d.State)
				})
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			res, err := c.Submit(cmd.Context(), client.ActionRequest{Type: "app.run", Args: mustArgs(req)})
			if err != nil {
				return err
			}
			var started struct{ Instance string }
			_ = json.Unmarshal(res.Data, &started)
			path := "/v1/instances/" + url.PathEscape(started.Instance)
			var in instanceInfo
			for {
				if err := c.Get(cmd.Context(), path, &in); err != nil {
					return err
				}
				if in.Ended != nil {
					break
				}
				select {
				case <-cmd.Context().Done():
					return fmt.Errorf("stopped waiting; %s keeps running (hostctl stop %s)", started.Instance, started.Instance)
				case <-time.After(500 * time.Millisecond):
				}
			}
			var logs struct{ Lines []string }
			if err := c.Get(cmd.Context(), path+"/logs?plain=true&lines=5000", &logs); err != nil {
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
		},
	}
	// Flags after the command are the command's.
	cmd.Flags().SetInterspersed(false)
	cmd.Flags().StringVar(&name, "name", "", "name the instance run-<name> (default: run)")
	cmd.Flags().BoolVarP(&background, "background", "b", false, "no window: run in the background")
	cmd.Flags().BoolVar(&windowed, "windowed", false, "a window, not full screen")
	cmd.Flags().BoolVar(&front, "front", false, "open in front even while someone is using the screen")
	cmd.Flags().StringVar(&dir, "dir", "", "the directory to run in (an absolute path on the machine)")
	cmd.Flags().StringArrayVar(&env, "env", nil, "an environment variable, NAME=value (repeat for more)")
	cmd.Flags().BoolVarP(&wait, "wait", "w", false, "wait until it ends; print its output and exit with its status")
	return cmd
}
