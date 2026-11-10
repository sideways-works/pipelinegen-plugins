// Command go-correlator is a pipelinegen plugin that records who uses which Go
// declaration and module, and which declarations a test ran.
package main

import (
	"os"

	"github.com/sideways-works/pipelinegen-correlator-go/internal/cli"
)

// Version is set by the release build.
var Version = "dev"

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, Version))
}
