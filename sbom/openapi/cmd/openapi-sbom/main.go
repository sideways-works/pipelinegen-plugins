// Command openapi-sbom is a pipelinegen plugin that lists the operations and
// components of OpenAPI documents.
package main

import (
	"os"

	"github.com/sideways-works/pipelinegen-sbom-openapi/internal/cli"
)

// Version is stamped by a release build with -ldflags "-X main.Version=<tag>".
var Version = "dev"

func main() {
	cli.Version = Version

	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
