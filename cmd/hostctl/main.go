// Command hostctl is the command-line client of the hostd API. Anything it
// does, a script or a phone can do through the same API.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/davitizhgenti/hostd/internal/client"
	"github.com/davitizhgenti/hostd/sdk"
)

// Exit codes, one per error code, so scripts can branch on them.
const (
	exitOK           = 0
	exitError        = 1 // internal errors, cannot connect, anything else
	exitUsage        = 2
	exitAuth         = 3 // unauthorized or forbidden
	exitNotFound     = 4
	exitNotRunning   = 5
	exitPrecondition = 6
	exitTimeout      = 7
	exitUnavailable  = 8
	exitLoop         = 9
	exitInvalidArgs  = 10
)

func exitCode(err error) int {
	switch sdk.CodeOf(err) {
	case sdk.CodeUnauthorized, sdk.CodeForbidden:
		return exitAuth
	case sdk.CodeNotFound:
		return exitNotFound
	case sdk.CodeInstanceNotRunning:
		return exitNotRunning
	case sdk.CodePreconditionFailed:
		return exitPrecondition
	case sdk.CodeTimeout:
		return exitTimeout
	case sdk.CodeModuleUnavailable:
		return exitUnavailable
	case sdk.CodeLoopDetected:
		return exitLoop
	case sdk.CodeInvalidArgs:
		return exitInvalidArgs
	default:
		return exitError
	}
}

// usageError marks mistakes in how hostctl was called.
type usageError struct{ error }

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app := &app{stdin: stdin, stdout: stdout, stderr: stderr}
	root := app.rootCommand()
	if app.needsModules(args) {
		if err := app.addModuleCommands(ctx, root); err != nil && !isHelp(args) {
			fmt.Fprintln(stderr, "hostctl:", err)
			return exitCode(err)
		}
	}
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return exitOK
	}
	fmt.Fprintln(stderr, "hostctl:", err)
	var ue usageError
	if errors.As(err, &ue) || isCobraUsage(err) {
		return exitUsage
	}
	return exitCode(err)
}

type app struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	jsonOut        bool
	url            string
	c              *client.Client
}

// client connects lazily, so commands that need no server (help) work
// without one.
func (a *app) client() (*client.Client, error) {
	if a.c != nil {
		return a.c, nil
	}
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	if a.url != "" {
		cfg.URL = a.url
	}
	if cfg.URL == "" {
		return nil, usageError{errors.New("no hostd address: run hostctl login <address>, or set HOSTD_URL")}
	}
	if cfg.Token == "" {
		return nil, usageError{errors.New("no token: run hostctl login <address>, or set HOSTD_TOKEN")}
	}
	c, err := client.New(cfg.URL, cfg.Token)
	if err != nil {
		return nil, usageError{err}
	}
	a.c = c
	return c, nil
}

func isHelp(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			return true
		}
	}
	return false
}

func isCobraUsage(err error) bool {
	for _, p := range []string{"unknown command", "unknown flag", "unknown shorthand flag", "accepts ",
		"requires at least", "invalid argument", "flag needs an argument", "required flag"} {
		if strings.Contains(err.Error(), p) {
			return true
		}
	}
	return false
}
