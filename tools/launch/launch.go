// Package launch is what this repository's `bazel run` targets run: each plugin's and template's
// tests, lint and release, and the targets over all of them, with the Go SDK, golangci-lint, Node
// and gh that MODULE.bazel pins.
package launch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

const usage = `usage: launch --go=RLOCATION [--lint=RLOCATION] [--node=RLOCATION] [--gh=RLOCATION] COMMAND
  test DIR... | lint DIR... | all test|lint | all release build OUT | checks | templates | hooks | subjects
  release DIR build OUT [VERSION] | release DIR notice | release DIR publish VERSION OUT | release DIR check VERSION`

var errUsage = errors.New(usage)

// The commands and tools the targets name.
const (
	cmdAll   = "all"
	cmdBuild = "build"
	cmdLint  = "lint"
	cmdTest  = "test"
	toolGo   = "go"
	toolNode = "node"
	toolGh   = "gh"
)

// Launch is one run of a target: the environment `bazel run` gave it and where it writes.
type Launch struct {
	Env            []string
	Executable     string
	Stdout, Stderr io.Writer

	workspace, working string
	goTool, lint, node string
	// paths are the directories that come first on PATH: the Go SDK's, Node's and gh's
	paths []string
}

// Main runs args (the target's own arguments, then the ones given after `bazel run ... --`) and
// returns the exit code.
func (l *Launch) Main(ctx context.Context, args []string) int {
	code, err := l.main(ctx, args)
	if err != nil {
		sayf(l.Stderr, "%v\n", err)

		return max(code, 1)
	}

	return code
}

func (l *Launch) main(ctx context.Context, args []string) (int, error) {
	tools := map[string]string{}

	for len(args) > 0 {
		name, value, ok := strings.Cut(strings.TrimPrefix(args[0], "--"), "=")
		if !ok || !strings.HasPrefix(args[0], "--") || !slices.Contains([]string{toolGo, cmdLint, toolNode, toolGh}, name) {
			break
		}

		tools[name] = value
		args = args[1:]
	}

	if tools[toolGo] == "" || len(args) == 0 {
		return 2, errUsage
	}

	if err := l.setUp(tools); err != nil {
		return 2, err
	}

	return l.dispatch(ctx, args)
}

func (l *Launch) dispatch(ctx context.Context, args []string) (int, error) {
	switch {
	case args[0] == cmdTest && len(args) > 1:
		return 0, l.each(args[1:], func(dir string) error { return l.Test(ctx, dir) })
	case args[0] == cmdLint && len(args) > 1 && l.lint != "":
		if err := l.versions(ctx); err != nil {
			return 1, err
		}

		return 0, l.each(args[1:], func(dir string) error { return l.Lint(ctx, dir) })
	case args[0] == cmdAll && len(args) == 2 && args[1] == cmdTest:
		return 0, l.All(ctx, false)
	case args[0] == cmdAll && len(args) == 2 && args[1] == cmdLint && l.lint != "":
		return 0, l.All(ctx, true)
	case args[0] == cmdAll && len(args) == 4 && args[1] == "release" && args[2] == cmdBuild:
		return 0, l.ReleaseTagged(ctx, l.abs(args[3]))
	case args[0] == "checks" && len(args) == 1 && l.lint != "":
		return 0, l.Checks(ctx)
	case args[0] == "templates" && len(args) == 1:
		return 0, l.Templates(ctx)
	case args[0] == "hooks" && len(args) == 1:
		return 0, l.Hooks(ctx)
	case args[0] == "subjects" && len(args) == 1:
		return 0, l.Subjects(ctx)
	case args[0] == "release" && len(args) > 2:
		return l.Release(ctx, args[1], args[2:])
	}

	return 2, errUsage
}

func (l *Launch) setUp(tools map[string]string) error {
	l.workspace = getenv(l.Env, "BUILD_WORKSPACE_DIRECTORY")
	if l.workspace == "" {
		return errors.New("launch: run it with bazel run, which names the checkout in BUILD_WORKSPACE_DIRECTORY") //nolint:err113 // said once, to the person
	}

	l.working = cmpOr(getenv(l.Env, "BUILD_WORKING_DIRECTORY"), l.workspace)

	for _, name := range []string{toolGo, toolNode, toolGh, cmdLint} {
		if tools[name] == "" {
			continue
		}

		p, err := Rlocation(l.Env, l.Executable, tools[name])
		if err != nil {
			return err
		}

		switch name {
		case toolGo:
			l.goTool = p
		case toolNode:
			l.node = p
		case cmdLint:
			l.lint = p

			continue
		}

		l.paths = append(l.paths, filepath.Dir(p))
	}

	return nil
}

// each runs run for every directory, all of them even when one fails, and names those that failed.
func (l *Launch) each(dirs []string, run func(dir string) error) error {
	var failed []string

	for _, dir := range dirs {
		if err := run(filepath.ToSlash(filepath.Clean(dir))); err != nil {
			sayf(l.Stderr, "%v\n", err)

			failed = append(failed, dir)
		}
	}

	if len(failed) > 0 {
		return fmt.Errorf("failed: %s", strings.Join(failed, " ")) //nolint:err113 // each failure is above
	}

	return nil
}

func sayf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...) //nolint:errcheck // the log is for people
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}

	return b
}

// Rlocation is the file a target's runfiles hold at path (an $(rlocationpath) value), with its
// symbolic links resolved: a Go SDK finds its root from where its go binary really is.
func Rlocation(env []string, executable, path string) (string, error) {
	if manifest := getenv(env, "RUNFILES_MANIFEST_FILE"); manifest != "" && getenv(env, "RUNFILES_DIR") == "" {
		return fromManifest(manifest, path)
	}

	dir := cmpOr(getenv(env, "RUNFILES_DIR"), executable+".runfiles")

	p, err := filepath.EvalSymlinks(filepath.Join(dir, filepath.FromSlash(path)))
	if err != nil {
		return "", fmt.Errorf("launch: %s is not in the runfiles: %w", path, err)
	}

	return p, nil
}

func fromManifest(manifest, path string) (string, error) {
	data, err := os.ReadFile(manifest) //nolint:gosec // the manifest Bazel names
	if err != nil {
		return "", fmt.Errorf("launch: %w", err)
	}

	for line := range strings.SplitSeq(string(data), "\n") {
		if from, to, ok := strings.Cut(line, " "); ok && from == path {
			return filepath.EvalSymlinks(to) //nolint:wrapcheck // the path says it
		}
	}

	return "", fmt.Errorf("launch: %s is not in the runfiles manifest %s", path, manifest) //nolint:err113 // said once, to the person
}

func getenv(env []string, name string) string {
	for _, kv := range slices.Backward(env) {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			return v
		}
	}

	return ""
}

// bazelRun are the variables `bazel run` sets for the target alone: a test or a program started
// from it that runs Bazel or reads runfiles of its own must not see them.
var bazelRun = []string{"BUILD_WORKSPACE_DIRECTORY", "BUILD_WORKING_DIRECTORY", "BUILD_EXECROOT", "BUILD_ID", "JAVA_RUNFILES", "PYTHON_RUNFILES"}

// fromBazelRun says whether a variable is one Bazel sets for what it runs. TEST_TMPDIR stays: Bazel
// sets it only for a test, never for `bazel run`, so one that is set is the person's own.
func fromBazelRun(name string) bool {
	return slices.Contains(bazelRun, name) || strings.HasPrefix(name, "RUNFILES_") ||
		(strings.HasPrefix(name, "TEST_") && name != "TEST_TMPDIR")
}

// ChildEnv is env for what a target starts: PATH as it is with the pinned tools' directories first;
// Go used as it is (no GOROOT of another SDK, no switch to another toolchain), each module on its
// own (no go.work) and built with -trimpath; and nothing `bazel run` set.
func ChildEnv(env []string, paths ...string) []string {
	out := make([]string, 0, len(env)+4)

	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if fromBazelRun(name) || slices.Contains([]string{"GOROOT", "GOTOOLCHAIN", "GOWORK", "GOFLAGS", "PATH"}, name) {
			continue
		}

		out = append(out, kv)
	}

	path := strings.Join(paths, string(filepath.ListSeparator))
	if p := getenv(env, "PATH"); p != "" {
		path += string(filepath.ListSeparator) + p
	}

	flags := getenv(env, "GOFLAGS")
	if !slices.Contains(strings.Fields(flags), "-trimpath") {
		flags = strings.TrimSpace(flags + " -trimpath")
	}

	return append(out, "PATH="+path, "GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS="+flags)
}

func (l *Launch) env() []string { return ChildEnv(l.Env, l.paths...) }

func (l *Launch) command(ctx context.Context, dir string, env []string, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // the repository's own tools and programs
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = l.Stdout, l.Stderr
	// a stopped target stops what it started, and gives it time to stop its own servers
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 30 * time.Second

	return cmd
}

// run runs name in dir with the children's environment, its output the target's.
func (l *Launch) run(ctx context.Context, dir, name string, args ...string) error {
	if err := l.command(ctx, dir, l.env(), name, args...).Run(); err != nil {
		return fmt.Errorf("%s %s in %s: %w", filepath.Base(name), strings.Join(args, " "), l.rel(dir), err)
	}

	return nil
}

func (l *Launch) rel(dir string) string {
	if r, err := filepath.Rel(l.workspace, dir); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.ToSlash(r)
	}

	return dir
}

func (l *Launch) scratch(pattern string) (string, func(), error) {
	dir, err := os.MkdirTemp("", pattern)
	if err != nil {
		return "", nil, fmt.Errorf("launch: %w", err)
	}

	return dir, func() { _ = os.RemoveAll(dir) }, nil //nolint:errcheck // a scratch directory
}

// tool builds the main package pkg of a module the tools module requires (pipelinegen's command,
// its release command) into dir, at the version tools/go.mod pins.
func (l *Launch) tool(ctx context.Context, dir, pkg string) (string, error) {
	exe := filepath.Join(dir, filepath.Base(pkg))

	if err := l.run(ctx, filepath.Join(l.workspace, "tools"), l.goTool, "build", "-o", exe, pkg); err != nil {
		return "", err
	}

	return exe, nil
}
