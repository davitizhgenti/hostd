package main

import (
	"encoding/json"

	"github.com/spf13/cobra"
)

// builtins are the top-level commands hostctl always has; anything else is
// looked up in the server's manifests.
var builtins = map[string]bool{
	"login": true, "version": true, "events": true,
	"action": true, "update": true, "git-shell": true,
	"help": true, "completion": true, "context": true, "systems": true, "new": true,
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
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
	root.PersistentFlags().BoolVar(&a.jsonOut, "json", false, "print JSON instead of text")
	root.PersistentFlags().StringVar(&a.context, "context", "", "which saved login to use (hostctl context lists them; HOSTD_CONTEXT)")
	root.PersistentFlags().StringVar(&a.url, "url", "", "hostd address, overriding the config (unix:///path or host:port)")
	root.AddCommand(a.loginCommand(), a.versionCommand(),
		a.eventsCommand(), a.actionCommand(), a.updateCommand(), a.gitShellCommand(), a.contextCommand(), a.systemsCommand(), a.newCommand())
	return root
}

// needsModules reports whether the command line (its words, the flags
// taken out) names something that is not built in, so the module commands
// must be fetched first. Help without a command fetches them too, to list
// them, but tolerates failure.
func (a *app) needsModules(args, words []string) bool {
	if len(words) == 0 {
		return isHelp(args)
	}
	return !builtins[words[0]]
}

func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
