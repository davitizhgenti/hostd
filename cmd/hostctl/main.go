// Command hostctl is the command-line client of the hostd API.
package main

import (
	"fmt"
	"os"

	"github.com/davitizhgenti/hostd/internal/version"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println("hostctl", version.Version)
		return
	}
	fmt.Fprintln(os.Stderr, "hostctl: not implemented yet")
	os.Exit(1)
}
