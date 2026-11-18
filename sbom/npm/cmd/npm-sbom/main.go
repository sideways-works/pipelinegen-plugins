package main

import (
	"os"

	"github.com/sideways-works/pipelinegen-plugins/sbom/npm/internal/cli"
)

var Version = "dev"

func main() {
	cli.Version = Version

	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
