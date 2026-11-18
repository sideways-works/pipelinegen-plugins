// Package boundary marks a slice's start and stop from inside its Bazel test
// action, as the tests' --run_under prefix: Bazel can't be paused from
// outside, so the test itself waits until the tool has marked the boundary.
// It fails closed: an unmarked boundary fails the test.
package boundary

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// The variables a test action gets: the marks directory, and the file
// naming each target's slice.
const (
	EnvDir    = "PIPELINEGEN_MARKS"
	EnvSlices = "PIPELINEGEN_MARK_SLICES"
)

const (
	Start = "start"
	Stop  = "stop"
)

// ErrUnmarked is a boundary the tool didn't mark, or marked failed.
var ErrUnmarked = errors.New("the boundary was not marked")

const poll = 50 * time.Millisecond

type request struct {
	Mark  string `json:"mark"`
	Slice string `json:"slice"`
}

// Name is the request's name for a test action's boundary. Shards, runs and
// retries are actions of their own, so a retry adds to the recording and
// never hides.
func Name(label, action, at string) string {
	sum := sha256.Sum256([]byte(label))

	return "bazel-" + hex.EncodeToString(sum[:8]) + "-" + action + "-" + at
}

func action() string {
	return "s" + os.Getenv("TEST_SHARD_INDEX") + "-p" + strconv.Itoa(os.Getpid())
}

func Mark(dir, label, slice, at string, limit time.Duration) error {
	name := Name(label, action(), at)

	body, err := json.Marshal(request{Mark: at, Slice: slice})
	if err != nil {
		return fmt.Errorf("boundary: %w", err)
	}

	// A stale answer from an earlier batch must not count.
	for _, ext := range []string{".done", ".failed"} {
		os.Remove(filepath.Join(dir, name+ext)) //nolint:errcheck,gosec // absent is fine; the tool's marks directory
	}

	if err := writeAtomic(filepath.Join(dir, name+".json"), body); err != nil {
		return err
	}

	until := time.Now().Add(limit)

	for {
		if _, err := os.Stat(filepath.Join(dir, name+".done")); err == nil { //nolint:gosec // the tool's marks directory
			return nil
		}

		if data, err := os.ReadFile(filepath.Join(dir, name+".failed")); err == nil { //nolint:gosec // the tool's marks directory
			return fmt.Errorf("%w: %s of %s: %s", ErrUnmarked, at, slice, data)
		}

		if time.Now().After(until) {
			return fmt.Errorf("%w: %s of %s: no answer after %s", ErrUnmarked, at, slice, limit)
		}

		time.Sleep(poll)
	}
}

func Slices(path string) (map[string]string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the driver's own file
	if err != nil {
		return nil, fmt.Errorf("boundary: %w", err)
	}

	var out map[string]string
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("boundary: %w", err)
	}

	return out, nil
}

func WriteSlices(path string, byLabel map[string]string) error {
	body, err := json.Marshal(byLabel)
	if err != nil {
		return fmt.Errorf("boundary: %w", err)
	}

	return writeAtomic(path, body)
}

func Wrap(limit time.Duration, argv []string) int {
	label, dir := os.Getenv("TEST_TARGET"), os.Getenv(EnvDir)
	if label == "" || dir == "" || len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "boundary: no TEST_TARGET, no", EnvDir, "or no test command: the test did not run")

		return 1
	}

	slices, err := Slices(os.Getenv(EnvSlices))
	if err != nil {
		fmt.Fprintln(os.Stderr, err, "- the test did not run")

		return 1
	}

	slice, ok := slices[label]
	if !ok {
		fmt.Fprintln(os.Stderr, "boundary: no slice for", label, "- the test did not run")

		return 1
	}

	if err := Mark(dir, label, slice, Start, limit); err != nil {
		fmt.Fprintln(os.Stderr, "boundary:", err, "- the test did not run")

		return 1
	}

	code, stopped := run(argv)

	limitStop := limit
	if stopped {
		// Bazel kills a test 15 seconds after its SIGTERM by default: mark the
		// stop within that, so the next test's start is still answered.
		limitStop = stopGrace
	}

	if err := Mark(dir, label, slice, Stop, limitStop); err != nil {
		// Fail rather than keep a recording that mixes two slices.
		fmt.Fprintln(os.Stderr, "boundary:", err, "- failed: its recording would mix with the next slice's")

		return 1
	}

	return code
}

const stopGrace = 10 * time.Second

func run(argv []string) (int, bool) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)

	defer signal.Stop(sig)

	// Bazel's own timeout stops the test.
	cmd := exec.CommandContext(context.Background(), argv[0], argv[1:]...) //nolint:gosec // the test Bazel runs
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "boundary:", err)

		return 1, false
	}

	done := make(chan error, 1)

	go func() { done <- cmd.Wait() }()

	stopped := false

	var err error

	select {
	case err = <-done:
	case s := <-sig:
		stopped = true

		cmd.Process.Signal(s) //nolint:errcheck // it may have ended already

		select {
		case err = <-done:
		case <-time.After(stopGrace):
			cmd.Process.Kill() //nolint:errcheck // it may have ended already

			err = <-done
		}
	}

	var exit *exec.ExitError

	switch {
	case err == nil && !stopped:
		return 0, false
	case errors.As(err, &exit) && exit.ExitCode() >= 0 && !stopped:
		return exit.ExitCode(), false
	case stopped:
		return 143, true // 128 + SIGTERM, as a shell reports a terminated command.
	default:
		fmt.Fprintln(os.Stderr, "boundary:", err)

		return 1, false
	}
}

func writeAtomic(path string, body []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil { //nolint:gosec // the tool's marks directory
		return fmt.Errorf("boundary: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil { //nolint:gosec // the tool's marks directory
		return fmt.Errorf("boundary: %w", err)
	}

	return nil
}
