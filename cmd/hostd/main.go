// Command hostd is the display and services daemon: the core plus the
// built-in modules.
package main

import (
	"fmt"
	"os"

	"github.com/davitizhgenti/hostd/internal/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("hostd", version.Version)
		return
	}
	fmt.Fprintln(os.Stderr, "hostd: not implemented yet")
	os.Exit(1)
}
