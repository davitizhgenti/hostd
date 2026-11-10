package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/davitizhgenti/hostd/internal/fsx"
	"github.com/davitizhgenti/hostd/sdk"
)

// hostctl new module NAME: the files of a module to start from.
func (a *app) newCommand() *cobra.Command {
	var goOut, pyOut bool
	module := &cobra.Command{
		Use:   "module NAME",
		Short: "Write the files of a new module to start from (--python, or --go)",
		Long: `Write a working module to start from, into ./NAME:

  --python   a program in Python's standard library that speaks the module
             protocol: one action, NAME.hello
  --go       a package built with sdk/kit, for hostd's source tree, with a test

It says how to run it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !sdk.ValidModuleName(name) {
				return usageError{fmt.Errorf("%q cannot name a module: lowercase letters, digits and - (up to 32)", name)}
			}
			if goOut == pyOut {
				return usageError{errors.New("choose --python or --go")}
			}
			files := pythonModule(name)
			if goOut {
				files = goModule(name)
			}
			if _, err := os.Stat(name); err == nil {
				return usageError{fmt.Errorf("%s exists already", name)}
			}
			for path, body := range files {
				mode := os.FileMode(0o644)
				if strings.HasSuffix(path, ".py") {
					mode = 0o755
				}
				if err := fsx.WriteFile(filepath.Join(name, path), []byte(body), mode); err != nil {
					return err
				}
				fmt.Fprintln(a.stdout, filepath.Join(name, path))
			}
			if pyOut {
				fmt.Fprintf(a.stdout, "\nTry it: copy %s/%s.toml to ~/.config/hostd/modules/ (set the path in it) and restart hostd.\nThen: hostctl %s hello\n", name, name, name)
			} else {
				fmt.Fprintf(a.stdout, "\nMove %s into hostd's modules/ folder, add its Builtin to internal/daemon/modules.go, and go test.\n", name)
			}
			return nil
		},
	}
	module.Flags().BoolVar(&pyOut, "python", false, "a Python module (standard library only)")
	module.Flags().BoolVar(&goOut, "go", false, "a Go module built with sdk/kit")
	root := &cobra.Command{Use: "new", Short: "Start something new: hostctl new module NAME", Args: cobra.ArbitraryArgs, RunE: groupRun}
	root.AddCommand(module)
	return root
}

// fill puts the module's name into a template.
func fill(tmpl, name string) string {
	return strings.NewReplacer("@NAME@", name, "@PKG@", strings.ReplaceAll(name, "-", "")).Replace(tmpl)
}

func goModule(name string) map[string]string {
	return map[string]string{
		name + ".go":      fill(goSource, name),
		name + "_test.go": fill(goTest, name),
	}
}

func pythonModule(name string) map[string]string {
	return map[string]string{
		name + ".py":   fill(pySource, name),
		name + ".toml": fill(pyConfig, name),
	}
}

const goSource = "// Package @PKG@ is a hostd module: @NAME@.hello says hello. Built with sdk/kit.\n" + `package @PKG@

import (
	"context"

	"github.com/davitizhgenti/hostd/internal/host"
	"github.com/davitizhgenti/hostd/sdk"
	"github.com/davitizhgenti/hostd/sdk/kit"
)

type helloArgs struct {
	Who string ` + "`json:\"who,omitempty\" desc:\"whom to greet\"`" + `
}

type greeting struct {
	Text string ` + "`json:\"text\"`" + `
}

// New returns the module.
func New() *kit.Module {
	m := kit.New("@NAME@", "0.1.0").Owns("@NAME@.*").Scope("@NAME@", "Use @NAME@")
	greeted := kit.Event[greeting](m, "@NAME@.greeted", "Someone was greeted")
	kit.Action(m, kit.Spec{Type: "@NAME@.hello", Description: "Say hello", Scope: "@NAME@"},
		func(_ context.Context, c kit.Ctx, a helloArgs) (greeting, error) {
			who := a.Who
			if who == "" {
				who = "world"
			}
			g := greeting{Text: "hello, " + who}
			greeted.Emit(c, g, "")
			return g, nil
		})
	return m
}

// Builtin is how hostd builds the module.
var Builtin = host.Builtin{
	Name: "@NAME@",
	New:  func(*host.Host, any) (sdk.Module, error) { return New(), nil },
}
`

const goTest = `package @PKG@

import (
	"context"
	"testing"

	"github.com/davitizhgenti/hostd/sdk"
	"github.com/davitizhgenti/hostd/sdk/moduletest"
)

func TestHello(t *testing.T) {
	m := New()
	man := m.Manifest()
	if err := man.Validate(); err != nil {
		t.Fatal(err)
	}
	core := moduletest.NewCore()
	if err := m.Start(context.Background(), core); err != nil {
		t.Fatal(err)
	}
	res, err := m.Handle(context.Background(), sdk.Action{ID: "act_1", Type: "@NAME@.hello", Args: []byte(` + "`{\"who\":\"you\"}`" + `)})
	if err != nil || string(res.Data) != ` + "`{\"text\":\"hello, you\"}`" + ` {
		t.Fatalf("%s %v", res.Data, err)
	}
	if got := core.Emitted("@NAME@.greeted"); len(got) != 1 {
		t.Fatalf("emitted %v", got)
	}
}
`

const pyConfig = `# Copy to ~/.config/hostd/modules/@NAME@.toml and set the path.
command   = ["python3", "/path/to/@NAME@/@NAME@.py"]
transport = "stdio"
`

const pySource = `#!/usr/bin/env python3
"""A hostd module in Python, standard library only: @NAME@.hello says hello.

hostd starts it and talks JSON-RPC 2.0 over its standard input and output,
one message per line (docs: "The module protocol"). Configure it with
~/.config/hostd/modules/@NAME@.toml.
"""

import json
import sys

NAME = "@NAME@"

MANIFEST = {
    "name": NAME,
    "version": "0.1.0",
    "owns": [NAME + ".*"],
    "scopes": [{"name": NAME, "description": "Use @NAME@"}],
    "actions": [{
        "type": NAME + ".hello",
        "description": "Say hello",
        "schema": {"type": "object", "properties": {"who": {"type": "string"}}, "additionalProperties": False},
        "scope": NAME,
    }],
}


def send(msg):
    sys.stdout.write(json.dumps(msg) + "\n")
    sys.stdout.flush()


def handle(method, params):
    if method == "hostd.handshake":
        return {"protocol": 1}
    if method == "manifest":
        return MANIFEST
    if method in ("start", "stop", "validate"):
        return {}
    if method == "handle":
        who = (params["action"].get("args") or {}).get("who") or "world"
        return {"status": "applied", "data": {"text": "hello, " + who}}
    raise ValueError("no method " + method)


def main():
    for line in sys.stdin:
        req = json.loads(line)
        if "method" not in req:
            continue  # an answer to a request this module made: it makes none
        try:
            result = {"result": handle(req["method"], req.get("params") or {})}
        except Exception as e:  # noqa: BLE001
            result = {"error": {"code": -32000, "message": str(e), "data": {"code": "internal"}}}
        if "id" in req:
            send({"jsonrpc": "2.0", "id": req["id"], **result})


if __name__ == "__main__":
    main()
`
