package main

import (
	"os"

	"github.com/sideways-works/pipelinegen-plugins/sbom/go/internal/cli"
)

var Version = "dev"

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, Version))
}
