package main

import (
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// rulesCommand lists the rules: when each fired, its last result, and
// whether it is paused.
func (a *app) rulesCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rules",
		Short: "List the rules, when they fired and whether they are paused",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var list struct {
				Rules []struct {
					Name string `json:"name"`
					On   string `json:"on"`
					Do   struct {
						Scene  string `json:"scene"`
						Script string `json:"script"`
					} `json:"do"`
					Fired       int        `json:"fired"`
					LastFired   *time.Time `json:"last_fired"`
					LastResult  string     `json:"last_result"`
					PausedUntil *time.Time `json:"paused_until"`
					PausedWhy   string     `json:"paused_why"`
				} `json:"rules"`
				Problems []struct{ File, Error string } `json:"problems"`
			}
			if err := c.Get(cmd.Context(), "/v1/rules", &list); err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(list)
			}
			tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "RULE\tON\tDO\tFIRED\tLAST\tSTATE")
			for _, r := range list.Rules {
				do := "scene " + r.Do.Scene
				if r.Do.Script != "" {
					do = "script " + r.Do.Script
				}
				last := "-"
				if r.LastFired != nil {
					last = r.LastFired.Local().Format("15:04:05") + " " + r.LastResult
				}
				state := "active"
				if r.PausedUntil != nil {
					state = "paused: " + r.PausedWhy
					if r.PausedUntil.Year() < 9999 {
						state += " (until " + r.PausedUntil.Local().Format("15:04") + ")"
					}
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n", r.Name, r.On, do, r.Fired, last, state)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			for _, p := range list.Problems {
				fmt.Fprintf(a.stderr, "%s: %s (the rules before it stay in use)\n", p.File, p.Error)
			}
			return nil
		},
	}
}

// scenesCommand lists the scenes.
func (a *app) scenesCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "scenes",
		Short: "List the scenes (hostctl scene apply <id> reaches one)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var list struct {
				Scenes []struct {
					ID      string          `json:"id"`
					Name    string          `json:"name"`
					Audio   *map[string]any `json:"audio"`
					Apps    *map[string]any `json:"apps"`
					Display *map[string]any `json:"display"`
				} `json:"scenes"`
				Problems []struct{ File, Error string } `json:"problems"`
			}
			if err := c.Get(cmd.Context(), "/v1/scenes", &list); err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(list)
			}
			tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "SCENE\tNAME\tSETS")
			for _, s := range list.Scenes {
				var sets []string
				for part, v := range map[string]*map[string]any{"apps": s.Apps, "audio": s.Audio, "display": s.Display} {
					if v != nil {
						sets = append(sets, part)
					}
				}
				sort.Strings(sets)
				fmt.Fprintf(tw, "%s\t%s\t%s\n", s.ID, s.Name, strings.Join(sets, ", "))
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			for _, p := range list.Problems {
				fmt.Fprintf(a.stderr, "%s: %s\n", p.File, p.Error)
			}
			return nil
		},
	}
}
