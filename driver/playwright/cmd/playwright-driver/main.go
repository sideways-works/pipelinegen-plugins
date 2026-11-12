// Command playwright-driver is a pipelinegen driver for Playwright spec
// files. Its own `marks <dir>` call writes marks.mjs into a directory.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	playwrightdriver "github.com/sideways-works/pipelinegen-driver-playwright"
	"github.com/sideways-works/pipelinegen-driver-playwright/driver"
	"github.com/sideways-works/pipelinegen/plugin"
)

const callMarks = "marks"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)

	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == callMarks {
		return marksCmd(args[1:], stdout, stderr)
	}

	return plugin.ServeDriver(ctx, args, stdin, stdout, stderr, driver.Handlers(stderr))
}

func marksCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		say(stderr, "usage: playwright-driver marks <dir>")

		return 2
	}

	path, err := playwrightdriver.WriteMarks(args[0])
	if err != nil {
		say(stderr, "playwright-driver marks: "+err.Error())

		return 1
	}

	say(stdout, path)

	return 0
}

func say(w io.Writer, msg string) {
	if _, err := fmt.Fprintln(w, msg); err != nil {
		return
	}
}
