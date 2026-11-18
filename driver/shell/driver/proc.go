package driver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const tailLines = 15

type tail struct {
	mu      sync.Mutex
	log     io.Writer
	lines   []string
	partial []byte
}

func newTail(log io.Writer) *tail { return &tail{log: log} }

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.log != nil {
		_, _ = t.log.Write(p) //nolint:errcheck // the log is for people; the tail is kept either way
	}

	t.partial = append(t.partial, p...)

	for {
		i := bytes.IndexByte(t.partial, '\n')
		if i < 0 {
			break
		}

		t.keep(string(t.partial[:i]))
		t.partial = t.partial[i+1:]
	}

	return len(p), nil
}

func (t *tail) keep(line string) {
	t.lines = append(t.lines, line)
	// Trimmed in bulk, so an append stays cheap.
	if len(t.lines) > 4*tailLines {
		t.lines = t.lines[len(t.lines)-tailLines:]
	}
}

// Tail is the last lines, each ending in a newline.
func (t *tail) Tail() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	lines := t.lines
	if len(t.partial) > 0 {
		lines = append(slices.Clip(lines), string(t.partial))
	}

	if len(lines) > tailLines {
		lines = lines[len(lines)-tailLines:]
	}

	if len(lines) == 0 {
		return ""
	}

	return strings.Join(lines, "\n") + "\n"
}

const stopWait = 10 * time.Second

// command stays in the driver's process group, so whatever stops the driver
// stops it too.
func command(ctx context.Context, argv []string, dir string, env []string, out io.Writer) *exec.Cmd {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // the suite's own command, from its trusted config
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Cancel = func() error { return signalTree(cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = stopWait

	return cmd
}

func signalTree(pid int, sig syscall.Signal) error {
	var errs []error

	for _, p := range append(descendants(pid), pid) {
		if err := syscall.Kill(p, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
			errs = append(errs, fmt.Errorf("signal %s: %w", sig, err))
		}
	}

	return errors.Join(errs...)
}

var progressEvery = time.Minute

type quiet struct {
	w    io.Writer
	last atomic.Int64
}

func newQuiet(w io.Writer) *quiet {
	q := &quiet{w: w}
	q.last.Store(time.Now().UnixNano())

	return q
}

func (q *quiet) Write(p []byte) (int, error) {
	q.last.Store(time.Now().UnixNano())

	n, err := q.w.Write(p)
	if err != nil {
		return n, fmt.Errorf("output: %w", err)
	}

	return n, nil
}

func (q *quiet) silentSince() time.Time { return time.Unix(0, q.last.Load()) }

// watch's lines go to the log only, never into a failure's tail. A nil
// silent counts the command silent throughout.
func watch(log io.Writer, what string, silent func() time.Time) (stop func()) {
	if log == nil || what == "" {
		return func() {}
	}

	done := make(chan struct{})
	start := time.Now()

	go func() {
		tick := time.NewTicker(progressEvery)
		defer tick.Stop()

		for {
			select {
			case <-done:
				return
			case now := <-tick.C:
				since := start
				if silent != nil {
					since = silent()
				}

				if now.Sub(since) >= progressEvery {
					sayf(log, "shell-driver: %s still runs after %s, silent for %s\n",
						what, now.Sub(start).Round(time.Second), now.Sub(since).Round(time.Second))
				}
			}
		}
	}()

	var once sync.Once

	return func() { once.Do(func() { close(done) }) }
}

// runCmd kills whatever still runs under the driver once the command ends.
// Output goes through the driver's own pipe, so a process left in the
// background holds nothing of the call open once it is stopped.
func runCmd(cmd *exec.Cmd, what string, log io.Writer) error {
	out := newQuiet(cmd.Stdout)

	r, w, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(cmd.Path), err)
	}

	cmd.Stdout, cmd.Stderr = w, w

	if err := cmd.Start(); err != nil {
		_ = r.Close() //nolint:errcheck // never read
		_ = w.Close() //nolint:errcheck // never written

		return fmt.Errorf("%s: %w", filepath.Base(cmd.Path), err)
	}

	_ = w.Close() //nolint:errcheck // the command holds its own copy

	stopWatch := watch(log, what, out.silentSince)
	defer stopWatch()

	copied := make(chan struct{})

	go func() {
		_, _ = io.Copy(out, r) //nolint:errcheck // the output is for people and the tail

		close(copied)
	}()

	err = cmd.Wait()

	stopWatch()
	stopLeftovers()

	select {
	case <-copied:
	case <-time.After(stopWait):
	}

	_ = r.Close() //nolint:errcheck // read as far as it goes

	if err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(cmd.Path), err)
	}

	return nil
}

const leftoverRounds = 100

// stopLeftovers looks again until nothing is left, since a process may
// start another between the look and its kill.
func stopLeftovers() []string {
	seen := map[int]bool{}
	count := map[string]int{}

	var order []string

	for range leftoverRounds {
		left := descendants(os.Getpid())
		if len(left) == 0 {
			break
		}

		for _, p := range left {
			if seen[p] || !isRunning(p) {
				continue
			}

			seen[p] = true

			name := commandOf(p)
			if count[name] == 0 {
				order = append(order, name)
			}

			count[name]++
		}

		for _, p := range left {
			_ = syscall.Kill(p, syscall.SIGKILL) //nolint:errcheck // a process gone already is stopped
		}

		for _, p := range left {
			var ws syscall.WaitStatus

			_, _ = syscall.Wait4(p, &ws, 0, nil) //nolint:errcheck // not the driver's child: its own parent reaps it
		}
	}

	names := make([]string, 0, len(order))

	for _, name := range order {
		if n := count[name]; n > 1 {
			name = fmt.Sprintf("%s (%d of them)", name, n)
		}

		names = append(names, name)
	}

	return names
}

func argv(set Settings, script, dir string) ([]string, error) {
	if script == "" {
		return set.Command, nil
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("script: %w", err)
	}

	f, err := os.CreateTemp(dir, "script-*.sh")
	if err == nil {
		_, err = f.WriteString(script)
		err = errors.Join(err, f.Close())
	}

	if err != nil {
		return nil, fmt.Errorf("script: %w", err)
	}

	return append(append([]string{}, set.Shell...), f.Name()), nil
}

func workDir(tree string, set Settings) string {
	return filepath.Join(tree, filepath.FromSlash(set.Dir))
}

func exitStatus(err error) (int, bool) {
	if err == nil {
		return 0, true
	}

	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return 0, false
	}

	if code := exit.ExitCode(); code >= 0 {
		return code, true
	}

	return -1, true
}

func readStatus(path string) (int, bool) {
	data, err := os.ReadFile(path) //nolint:gosec // the script's own result
	if err != nil {
		return 0, false
	}

	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}

	return n, true
}

func sayf(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}

	_, _ = fmt.Fprintf(w, format, args...) //nolint:errcheck // the log is for people
}
