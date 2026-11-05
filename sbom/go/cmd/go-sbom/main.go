// Command go-sbom is a pipelinegen plugin that lists a Go repository's
// declarations, the modules it depends on and its toolchain.
package main

import (
	"os"

	"github.com/sideways-works/pipelinegen-sbom-go/internal/cli"
)

// Version is set by the release build.
var Version = "dev"

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, Version))
}
