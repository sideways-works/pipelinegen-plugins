// Command launch is what this repository's `bazel run` targets run (see tools/launch).
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/sideways-works/pipelinegen-plugins/tools/launch"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}

	l := &launch.Launch{Env: os.Environ(), Executable: exe, Stdout: os.Stdout, Stderr: os.Stderr}
	code := l.Main(ctx, os.Args[1:])

	stop()
	os.Exit(code)
}
