// Command oapi-codegen-correlator is a pipelinegen plugin that records who
// touches the operations of OpenAPI documents.
package main

import (
	"os"

	"github.com/sideways-works/pipelinegen-correlator-oapi-codegen/internal/cli"
)

// Version is stamped by a release build with -ldflags "-X main.Version=<tag>".
var Version = "dev"

func main() {
	cli.Version = Version

	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
