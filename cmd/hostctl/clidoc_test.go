package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var updateDocs = flag.Bool("update", false, "rewrite docs/cli.md")

// TestCLIDoc keeps docs/cli.md, the reference of hostctl's built-in
// commands, in step with the code. After a change:
// go test ./cmd/hostctl -run CLIDoc -update
func TestCLIDoc(t *testing.T) {
	a := &app{}
	root := a.rootCommand()
	var b strings.Builder
	b.WriteString(`# The CLI

<!-- Generated from hostctl's commands by go test ./cmd/hostctl -run CLIDoc -update. -->

hostctl talks to a hostd machine: over its unix socket on the machine itself, or over the network with a token (` + "`hostctl login <machine>:7300`" + `). ` + "`HOSTD_URL`" + ` and ` + "`HOSTD_TOKEN`" + ` override the saved login.

**Module commands** come from the machine: every action becomes ` + "`hostctl <namespace> <action> [args]`" + `, such as ` + "`hostctl audio volume set 40`" + `, ` + "`hostctl deploy run blog`" + ` or ` + "`hostctl scene apply movie`" + `. An action's required arguments are positional, in order, and every argument also has a ` + "`--flag`" + `. Arguments marked secret (` + "`hostctl secret set <name>`" + `) are read from standard input. ` + "`hostctl --help`" + ` on a connected machine lists them all, including those of external modules.

**Reads** (what a module reports) come from the machine too: ` + "`hostctl get <module> <read>`" + `, such as ` + "`hostctl get display windows`" + `. A read that returns a list prints as a table when its module says how, and as JSON otherwise, or with ` + "`--json`" + `. A read with ` + "`{parameters}`" + ` in its path takes them as arguments (` + "`hostctl get apps app firefox`" + `), and query parameters are given as ` + "`--param key=value`" + `. A read named after its module is also the module's own command: ` + "`hostctl get audio`" + ` is ` + "`hostctl get audio audio`" + `. A module can also ask for a short top-level name for one of its commands: ` + "`hostctl windows`" + ` is ` + "`hostctl get display windows`" + `. A name that a built-in command or another module has is not taken.

Global flags:

`)
	writeFlags(&b, root.PersistentFlags())
	b.WriteString("\nExit codes, for scripts:\n\n| Code | Meaning |\n| --- | --- |\n")
	for _, c := range []struct {
		code int
		what string
	}{
		{exitOK, "done (a skipped action included: skipping is not an error)"},
		{exitError, "anything else: cannot connect, an internal error"},
		{exitUsage, "hostctl was called wrong"},
		{exitAuth, "`unauthorized` or `forbidden`"},
		{exitNotFound, "`not_found`"},
		{exitNotRunning, "`instance_not_running`"},
		{exitPrecondition, "`precondition_failed`"},
		{exitTimeout, "`timeout`"},
		{exitUnavailable, "`module_unavailable`"},
		{exitLoop, "`loop_detected`"},
		{exitInvalidArgs, "`invalid_args`"},
	} {
		fmt.Fprintf(&b, "| %d | %s |\n", c.code, c.what)
	}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			fmt.Fprintf(&b, "\n## %s\n\n%s\n\n```\n%s\n```\n", sub.CommandPath(), sub.Short, sub.UseLine())
			if sub.Long != "" && sub.Long != sub.Short {
				fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(sub.Long))
			}
			if sub.LocalNonPersistentFlags().HasAvailableFlags() {
				b.WriteString("\n")
				writeFlags(&b, sub.LocalNonPersistentFlags())
			}
			walk(sub)
		}
	}
	walk(root)
	got := []byte(b.String())
	path := filepath.Join("..", "..", "docs", "cli.md")
	if *updateDocs {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("docs/cli.md is out of date: go test ./cmd/hostctl -run CLIDoc -update")
	}
}

func writeFlags(b *strings.Builder, fs *pflag.FlagSet) {
	b.WriteString("| Flag | Default | Meaning |\n| --- | --- | --- |\n")
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Name == "help" {
			return
		}
		def := f.DefValue
		if def == "" || def == "false" || def == "[]" {
			def = ""
		} else {
			def = "`" + def + "`"
		}
		fmt.Fprintf(b, "| `--%s` | %s | %s |\n", f.Name, def, strings.ReplaceAll(f.Usage, "|", "\\|"))
	})
}
