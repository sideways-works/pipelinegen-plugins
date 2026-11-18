package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/sideways-works/pipelinegen-plugins/driver/shell/driver"
	"github.com/sideways-works/pipelinegen/plugin"
)

func main() {
	// What a script leaves running stays the driver's, to stop.
	driver.KeepDescendants()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := plugin.ServeDriver(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, driver.Handlers(os.Stderr))

	stop()
	os.Exit(code)
}
