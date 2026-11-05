// Package bazel runs one `bazel test` or `bazel coverage` invocation. It is
// the one place that knows Bazel's command line.
package bazel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// Invocation is one run of Bazel over some test targets.
type Invocation struct {
	Dir string
	// Record runs `bazel coverage`, instrumenting what Filter matches.
	Record  bool
	Filter  string
	Targets []string
	// TestFilter, when set, runs only the matching tests of the targets.
	TestFilter string
	// Events is the build event file the invocation writes.
	Events string
	// Startup are Bazel's startup options (--output_user_root first).
	Startup []string
	Flags   []string
	// TestEnv names variables that reach the tests, which inherit none.
	TestEnv []string
	// Timeout, when positive, is each test's limit in seconds.
	Timeout int
	// Log receives Bazel's output.
	Log string
	Env []string
}

// Runner runs invocations; the driver's tests replace it.
type Runner interface {
	Run(ctx context.Context, inv Invocation) error
}

// Exec runs Bazel (or bazelisk) as a command.
type Exec struct {
	Bazel string
}

// ErrBazel wraps an invocation that couldn't start.
var ErrBazel = errors.New("bazel")

// Args is the invocation's command line after the command's name.
func Args(inv Invocation) []string {
	cmd := "test"
	if inv.Record {
		cmd = "coverage"
	}

	args := append([]string{}, inv.Startup...)
	args = append(args, cmd, "--keep_going", "--build_event_json_file="+inv.Events)

	if inv.Record {
		filter := inv.Filter
		if filter == "" {
			filter = "^//"
		}

		// Every package, not only the tests' own: what a test runs elsewhere is
		// what its recording is for.
		args = append(args, "--instrumentation_filter="+filter, "--combined_report=none")
	}

	if inv.TestFilter != "" {
		args = append(args, "--test_filter="+inv.TestFilter)
	}

	if inv.Timeout > 0 {
		args = append(args, "--test_timeout="+strconv.Itoa(inv.Timeout))
	}

	for _, name := range inv.TestEnv {
		args = append(args, "--test_env="+name)
	}

	args = append(args, inv.Flags...)
	args = append(args, "--")

	return append(args, inv.Targets...)
}

// Run ignores Bazel's exit code unless Bazel failed to start: the build
// event file says what happened.
func (e Exec) Run(ctx context.Context, inv Invocation) error {
	bin := e.Bazel
	if bin == "" {
		bin = "bazel"
	}

	cmd := exec.CommandContext(ctx, bin, Args(inv)...) //nolint:gosec // the configured Bazel
	cmd.Dir = inv.Dir
	cmd.Env = inv.Env

	if inv.Log != "" {
		f, err := os.Create(inv.Log)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrBazel, err)
		}
		defer f.Close() //nolint:errcheck // a log

		cmd.Stdout, cmd.Stderr = f, f
	}

	// Interrupt as Ctrl-C does, so Bazel still closes its event file.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = interruptGrace

	err := cmd.Run()

	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) && ctx.Err() == nil {
		return fmt.Errorf("%w: %w", ErrBazel, err)
	}

	return nil
}

const interruptGrace = 30 * time.Second
