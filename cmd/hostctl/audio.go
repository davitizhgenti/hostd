package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/davitizhgenti/hostd/internal/client"
)

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
			if line := heldBy(res); line != "" {
				_, err := fmt.Fprintln(a.stdout, line)
				return err
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
			if line := heldBy(res); line != "" {
				_, err := fmt.Fprintln(a.stdout, line)
				return err
			}
			return a.printAudio(res.Data)
		},
	}
}
