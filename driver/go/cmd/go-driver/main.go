package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/sideways-works/pipelinegen-driver-go/driver"
	"github.com/sideways-works/pipelinegen/plugin"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == driver.ExecVerb {
		fmt.Fprintln(os.Stderr, driver.ExecTest(os.Args[2:]))

		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := plugin.ServeDriver(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, driver.Handlers(os.Stderr))

	stop()
	os.Exit(code)
}
