package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/davitizhgenti/hostd/sdk"
)

// waitQuietCommand waits until no action of the given kinds is being
// handled: what an update does before it restarts hostd, so it never cuts
// a deploy or a scene in half.
func (a *app) waitQuietCommand() *cobra.Command {
	var types string
	var idle bool
	var maxWait, every time.Duration
	cmd := &cobra.Command{
		Use:   "wait-quiet",
		Short: "Wait until no deploy or scene is in progress (exit 1 if one still is after --max)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			patterns := strings.Split(types, ",")
			deadline := time.Now().Add(maxWait)
			for {
				var running []struct {
					ID      string    `json:"id"`
					Type    string    `json:"type"`
					Started time.Time `json:"started"`
				}
				if err := c.Get(cmd.Context(), "/v1/actions/running", &running); err != nil {
					return err
				}
				var busy []string
				for _, r := range running {
					for _, p := range patterns {
						if sdk.MatchType(strings.TrimSpace(p), r.Type) {
							busy = append(busy, fmt.Sprintf("%s (%s, since %s)", r.Type, r.ID, r.Started.In(displayZone).Format("15:04:05")))
							break
						}
					}
				}
				if idle {
					var d struct {
						Present bool `json:"present"`
					}
					// No display module (a headless machine): nobody is at a screen.
					if err := c.Get(cmd.Context(), "/v1/display", &d); err == nil && d.Present {
						busy = append(busy, "someone is using the screen")
					} else if err != nil && sdk.CodeOf(err) != sdk.CodeNotFound {
						return err
					}
				}
				if len(busy) == 0 {
					return nil
				}
				if time.Now().After(deadline) {
					return errors.New("still in progress after " + maxWait.String() + ": " + strings.Join(busy, ", "))
				}
				fmt.Fprintln(a.stderr, "waiting for:", strings.Join(busy, ", "))
				select {
				case <-cmd.Context().Done():
					return cmd.Context().Err()
				case <-time.After(every):
				}
			}
		},
	}
	cmd.Flags().StringVar(&types, "types", "deploy.*,scene.*", "action types to wait for (comma-separated; a namespace with .*)")
	cmd.Flags().BoolVar(&idle, "idle", false, "also wait until nobody is using the screen")
	cmd.Flags().DurationVar(&maxWait, "max", 5*time.Minute, "give up after this long")
	cmd.Flags().DurationVar(&every, "every", 2*time.Second, "how often to look")
	return cmd
}
