// Command concourse-runner is pipelinegen's runner plugin for Concourse.
package main

import (
	"os"

	"github.com/sideways-works/pipelinegen-plugins/runner/concourse/internal/concourse"
)

func main() {
	os.Exit(concourse.Serve(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
