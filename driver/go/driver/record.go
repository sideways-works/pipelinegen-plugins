package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/sideways-works/pipelinegen/plugin"
)

// recordBundle builds the coverage binary once and runs it per file, laid
// out as go test lays it out: a child whose environment was cut down finds
// gocoverdir/ beside the binary.
func (j *job) recordBundle(ctx context.Context, g group) error {
	dir := filepath.Join(j.req.Work, "go-driver", strings.NewReplacer("/", "_", ".", "_").Replace(g.pkg))
	bin := filepath.Join(dir, "pkg.test")

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("work: %w", err)
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("work: %w", err)
	}

	built, out, err := j.build(ctx, g.pkg, bin)
	if err != nil {
		return err
	}

	if !built {
		// The package's tests didn't build: every file fails with the build's
		// words.
		o := newOutcome()
		o.buildFailed = true
		o.out = strings.Split(strings.TrimRight(out, "\n"), "\n")

		for _, s := range g.slices {
			if ev, ok := end(s.DriverSlice, []*outcome{o}, j.set.held); ok {
				if err := j.done(s, ev); err != nil {
					return err
				}
			}
		}

		return nil
	}

	pkgDir := filepath.Join(j.module, filepath.FromSlash(g.pkg))
	cover := filepath.Join(dir, "gocoverdir")

	for _, s := range g.slices {
		if err := j.runBinary(ctx, s, bin, pkgDir, cover); err != nil {
			return err
		}
	}

	return nil
}

func (j *job) build(ctx context.Context, pkg, bin string) (bool, string, error) {
	args := []string{"test", "-c", "-o", bin}
	if j.set.Race {
		args = append(args, "-race")
	}

	if tags := j.tags(); len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}

	args = append(args, j.coverArgs()...)
	args = append(append(args, j.set.Flags...), pkg)

	cmd := exec.CommandContext(ctx, "go", args...) //nolint:gosec // go itself, on the tree under test
	cmd.Dir = j.module
	cmd.Env = j.env

	var out bytes.Buffer

	cmd.Stdout, cmd.Stderr = &out, &out

	err := cmd.Run()

	sayf(j.log, "%s", out.String())

	// An exit status means go test -c ran and the tests didn't build, which is
	// the slices' failure.
	var exit *exec.ExitError

	switch {
	case errors.As(err, &exit):
		return false, out.String(), nil
	case err != nil:
		return false, "", fmt.Errorf("go test -c %s: %w", pkg, err)
	case !exists(bin):
		return false, "go test -c wrote no binary for " + pkg + ": " + out.String(), nil
	default:
		return true, "", nil
	}
}

func exists(p string) bool {
	_, err := os.Stat(p)

	return err == nil
}

func (j *job) runBinary(ctx context.Context, s *slice, bin, pkgDir, cover string) error {
	if err := os.RemoveAll(cover); err != nil {
		return fmt.Errorf("gocoverdir: %w", err)
	}

	if err := os.MkdirAll(cover, 0o750); err != nil {
		return fmt.Errorf("gocoverdir: %w", err)
	}

	if err := os.MkdirAll(s.Recording, 0o750); err != nil {
		return fmt.Errorf("recording: %w", err)
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("go-driver: %w", err)
	}

	// go tool exports Go's own settings to what it runs; the test binary gets
	// exactly what go test gives it instead.
	env := filepath.Join(filepath.Dir(cover), "env.json")
	if err := writeEnv(env, append(slices.Clone(j.env), "PWD="+pkgDir, "GOCOVERDIR="+cover)); err != nil {
		return err
	}

	args := []string{
		"tool", "test2json", "-t", self, ExecVerb, env, bin, "-test.v=test2json", "-test.paniconexit0",
		"-test.gocoverdir=" + cover, "-test.coverprofile=" + filepath.Join(s.Recording, profileName(0)),
		"-test.run=" + runPattern(s.Tests),
	}

	if limit := j.timeout(1); limit > 0 {
		args = append(args, "-test.timeout="+limit.String())
	}

	if err := j.mark(ctx, plugin.MarkStart, s.Name); err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, "go", args...) //nolint:gosec // go's own test2json around the binary it built
	cmd.Dir = pkgDir

	cmd.Env = j.env
	cmd.Stderr = j.log

	o, err := j.events(cmd)
	if err != nil {
		return err
	}

	if err := j.mark(ctx, plugin.MarkStop, s.Name); err != nil {
		return err
	}

	if ev, ok := end(s.DriverSlice, []*outcome{o}, j.set.held); ok {
		return j.done(s, ev)
	}

	return nil
}

var errExecArgs = errors.New(ExecVerb)

// ExecVerb is the verb test2json runs the driver with: it execs the test
// binary under the environment a file holds, so a test sees what it sees
// under go test, not what go tool adds.
const ExecVerb = "exec-test"

func writeEnv(p string, env []string) error {
	data, err := json.Marshal(env)
	if err == nil {
		err = os.WriteFile(p, data, 0o600)
	}

	if err != nil {
		return fmt.Errorf("environment: %w", err)
	}

	return nil
}

// ExecTest is the exec-test verb: args are the environment's file, the
// binary and its arguments. It returns only when the exec failed.
func ExecTest(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("%w: an environment file and a binary are needed", errExecArgs)
	}

	data, err := os.ReadFile(args[0]) //nolint:gosec // the file the driver itself wrote for this run
	if err != nil {
		return fmt.Errorf("%s: %w", ExecVerb, err)
	}

	var env []string
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("%s: %w", ExecVerb, err)
	}

	return fmt.Errorf("%s: %w", ExecVerb, syscall.Exec(args[1], args[1:], env)) //nolint:gosec // the test binary the driver built, with go test's arguments
}
