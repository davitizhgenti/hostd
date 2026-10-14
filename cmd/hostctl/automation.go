package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type ruleInfo struct {
	Name string `json:"name"`
	On   string `json:"on"`
	Do   struct {
		Scene  string `json:"scene"`
		Script string `json:"script"`
		Action string `json:"action"`
	} `json:"do"`
	Fired       int        `json:"fired"`
	LastFired   *time.Time `json:"last_fired"`
	LastResult  string     `json:"last_result"`
	PausedUntil *time.Time `json:"paused_until"`
	PausedWhy   string     `json:"paused_why"`
}

var ruleColumns = []column[ruleInfo]{
	{"RULE", func(r ruleInfo) string { return r.Name }},
	{"ON", func(r ruleInfo) string { return r.On }},
	{"DO", func(r ruleInfo) string {
		switch {
		case r.Do.Script != "":
			return "script " + r.Do.Script
		case r.Do.Action != "":
			return "action " + r.Do.Action
		}
		return "scene " + r.Do.Scene
	}},
	{"FIRED", func(r ruleInfo) string { return strconv.Itoa(r.Fired) }},
	{"LAST", func(r ruleInfo) string {
		if r.LastFired == nil {
			return "-"
		}
		return r.LastFired.In(displayZone).Format("15:04:05") + " " + r.LastResult
	}},
	{"STATE", func(r ruleInfo) string {
		if r.PausedUntil == nil {
			return "active"
		}
		state := "paused: " + r.PausedWhy
		if r.PausedUntil.Year() < 9999 { // paused by hand has no end
			state += " (until " + r.PausedUntil.In(displayZone).Format("15:04") + ")"
		}
		return state
	}},
}

// rulesCommand lists the rules: when each fired, its last result, and
// whether it is paused.
func (a *app) rulesCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rules",
		Short: "List the rules, when they fired and whether they are paused",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list struct {
				Rules    []ruleInfo `json:"rules"`
				Problems []problem  `json:"problems"`
			}
			if done, err := a.fetch(cmd, "/v1/rules", &list); done || err != nil {
				return err
			}
			if err := writeTable(a.stdout, tableStyle{}, ruleColumns, list.Rules); err != nil {
				return err
			}
			for _, p := range list.Problems {
				fmt.Fprintf(a.stderr, "%s: %s (the rules before it stay in use)\n", p.File, p.Error)
			}
			return nil
		},
	}
}

type sceneInfo struct {
	ID      string           `json:"id"`
	Name    string           `json:"name"`
	Audio   *map[string]any  `json:"audio"`
	Apps    *map[string]any  `json:"apps"`
	Display *map[string]any  `json:"display"`
	Do      []map[string]any `json:"do"` // its [[do]] actions; here only whether it has any
}

var sceneColumns = []column[sceneInfo]{
	{"SCENE", func(s sceneInfo) string { return s.ID }},
	{"NAME", func(s sceneInfo) string { return s.Name }},
	{"SETS", func(s sceneInfo) string { // the parts it has, in a fixed order
		var sets []string
		for _, p := range []struct {
			name string
			has  bool
		}{{"apps", s.Apps != nil}, {"audio", s.Audio != nil}, {"display", s.Display != nil}, {"do", len(s.Do) > 0}} {
			if p.has {
				sets = append(sets, p.name)
			}
		}
		return strings.Join(sets, ", ")
	}},
}

// scenesCommand lists the scenes and what each one sets.
func (a *app) scenesCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "scenes",
		Short: "List the scenes (hostctl scene apply <id> reaches one)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list struct {
				Scenes   []sceneInfo `json:"scenes"`
				Problems []problem   `json:"problems"`
			}
			if done, err := a.fetch(cmd, "/v1/scenes", &list); done || err != nil {
				return err
			}
			if err := writeTable(a.stdout, tableStyle{}, sceneColumns, list.Scenes); err != nil {
				return err
			}
			for _, p := range list.Problems {
				fmt.Fprintf(a.stderr, "%s: %s\n", p.File, p.Error)
			}
			return nil
		},
	}
}
