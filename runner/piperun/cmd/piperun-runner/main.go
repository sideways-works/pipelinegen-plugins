// Command piperun-runner is pipelinegen's runner plugin for piperun.
package main

import (
	"os"

	"github.com/sideways-works/pipelinegen-plugins/runner/piperun/internal/generate"
)

func main() {
	os.Exit(generate.Serve(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
