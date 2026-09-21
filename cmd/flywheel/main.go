// Command flywheel is the entry point for the CLI binary. It only parses
// os.Args and delegates everything else to internal/cli.
package main

import (
	"os"

	"github.com/masanami/flywheel/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
