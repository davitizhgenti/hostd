// Command hostd is the display and services daemon: the core plus the
// built-in modules. See internal/daemon.
package main

import (
	"context"
	"os"

	"github.com/davitizhgenti/hostd/internal/daemon"
)

func main() {
	os.Exit(daemon.Run(context.Background(), os.Args[1:], os.Stderr))
}
