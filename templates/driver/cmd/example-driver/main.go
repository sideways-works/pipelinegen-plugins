// Command example-driver is a minimal driver: a slice is a shell test file (*_test.sh), run with sh
// in the tree, and it passes when the script exits 0. It reads no per-test events, so it judges by
// the exit code alone and says so (results "exit"), and it records nothing.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

// Version is set to the release's tag when it is built.
var Version = "dev"

// tailBytes is how much of a failed script's output its failure keeps.
const tailBytes = 4 << 10

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	code := plugin.ServeDriver(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, plugin.DriverHandlers{
		Describe: describe,
		Run:      run,
	})

	stop()
	os.Exit(code)
}

func describe() (plugin.Description, error) {
	return plugin.Description{
		Protocol: plugin.Protocol, Kind: plugin.KindDriver, Name: "example-driver", Version: Version,
		Capabilities: []string{},
		Needs:        []string{"sh"},
		Verbs:        []string{plugin.CallRun},
		Routes:       &plugin.Routes{Files: []string{"**/*_test.sh"}},
		Results:      plugin.ResultsExit,
	}, nil
}

// run runs the slices one after another, a bundle the same as one by one, within the task's
// deadline, and emits each slice's start and end.
func run(ctx context.Context, req plugin.DriverRequest, emit func(plugin.DriverEvent) error) error {
	if req.Deadline != "" {
		at, err := time.Parse(time.RFC3339, req.Deadline)
		if err != nil {
			return fmt.Errorf("deadline %q: %w", req.Deadline, err)
		}

		var cancel context.CancelFunc

		ctx, cancel = context.WithDeadline(ctx, at)
		defer cancel()
	}

	env := os.Environ()
	for k, v := range req.Env {
		env = append(env, k+"="+v)
	}

	for _, sl := range req.Slices {
		if err := emit(plugin.DriverEvent{Slice: sl.Name, Event: plugin.EventStart}); err != nil {
			return err
		}

		if err := emit(runSlice(ctx, req.Tree, env, sl)); err != nil {
			return err
		}
	}

	return nil
}

func runSlice(ctx context.Context, tree string, env []string, sl plugin.DriverSlice) plugin.DriverEvent {
	start := time.Now()

	var out bytes.Buffer

	cmd := exec.CommandContext(ctx, "sh", filepath.FromSlash(sl.Name)) //nolint:gosec // the slice the core hands in, a file of the tree
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = tree, env, &out, &out

	err := cmd.Run()
	one := 1
	ev := plugin.DriverEvent{Slice: sl.Name, Event: plugin.EventEnd, Tests: &one, Seconds: time.Since(start).Seconds()}

	var exit *exec.ExitError

	switch {
	case err == nil:
		ev.Passed = true
	case errors.As(err, &exit) || ctx.Err() != nil:
		ev.Failures = []plugin.Failure{{Test: sl.Name, Tail: tail(out.Bytes()) + fmt.Sprintf("%v\n", err)}}
	default:
		ev.Failures = []plugin.Failure{{Test: sl.Name, Tail: fmt.Sprintf("it did not run: %v\n", err)}}
	}

	return ev
}

func tail(b []byte) string {
	if len(b) > tailBytes {
		b = b[len(b)-tailBytes:]
	}

	return string(b)
}
