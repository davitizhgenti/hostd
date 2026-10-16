package main

import (
	"encoding/json"
	"strings"

	"github.com/spf13/cobra"
)

// builtins are the top-level commands hostctl always has; anything else is
// looked up in the server's manifests.
var builtins = map[string]bool{
	"login": true, "version": true, "token": true, "log": true, "events": true,
	"action": true, "update": true, "git-shell": true, "wait-quiet": true, "tls": true,
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
		a.eventsCommand(), a.actionCommand(), a.updateCommand(), a.gitShellCommand(), a.waitQuietCommand(), a.tlsCommand())
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

func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
