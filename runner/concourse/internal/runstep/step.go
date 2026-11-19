// Package runstep runs the tasks of the pipeline the runner writes. Each task
// runs `concourse-runner step <name>`: the step fetches the pinned tools,
// calls pipelinegen as its task needs, and exits as the task should.
package runstep

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// tool is pipelinegen as the bootstrap places it.
const tool = "tools/pipelinegen"

const planCmd = "plan"

// Task is the task a step runs in.
type Task struct {
	// Dir is the task's directory, where its inputs and outputs are.
	Dir string
	// Env is the task's whole environment.
	Env            []string
	Stdout, Stderr io.Writer
	// Replace runs a step's last command in the step's place, as a shell's
	// exec does, so the command gets Concourse's signals itself.
	Replace bool
	// Fetch fetches the pinned tools before a step that runs them.
	Fetch bool
	HTTP  *http.Client
	Now   func() time.Time
}

// Main runs a step as a task's command.
func Main(args []string, stdout, stderr io.Writer) int {
	t := &Task{
		Dir: ".", Env: os.Environ(), Stdout: stdout, Stderr: stderr,
		Replace: true, Fetch: true, HTTP: &http.Client{}, Now: time.Now,
	}

	return t.Run(args)
}

type stepFunc func(t *Task, args []string) int

type def struct {
	run stepFunc
	// tools: the step runs pipelinegen, so the tools are fetched first.
	tools bool
}

var byName = map[string]def{
	"tools":           {run: func(*Task, []string) int { return 0 }, tools: true},
	planCmd:           {run: (*Task).plan, tools: true},
	"continuous-plan": {run: (*Task).continuousPlan, tools: true},
	"begin":           {run: (*Task).begin, tools: true},
	"proposed-plan":   {run: (*Task).proposedPlan, tools: true},
	"proposed-write":  {run: (*Task).proposedWrite, tools: true},
	"sweep":           {run: (*Task).sweep, tools: true},
	"sweep-plan":      {run: (*Task).sweepPlan, tools: true},
	"sweep-write":     {run: (*Task).sweepWrite, tools: true},
	"pipeline":        {run: (*Task).pipeline, tools: true},
	"stale":           {run: (*Task).stale, tools: true},
	"start":           {run: (*Task).start, tools: true},
	// A batch's end fetches the tools it calls itself, kept as its cache.
	"batch-end": {run: (*Task).batchEnd, tools: true},
	// The end fetches the tools only when the build broke before it had them.
	"end":          {run: (*Task).end},
	"report-error": {run: (*Task).reportError},
}

// Names are the steps, sorted.
func Names() []string {
	out := make([]string, 0, len(byName))
	for name := range byName {
		out = append(out, name)
	}

	slices.Sort(out)

	return out
}

// Run runs the step args names with the rest of args.
func (t *Task) Run(args []string) int {
	if len(args) == 0 {
		t.sayf("usage: concourse-runner step <%s> [flags]", strings.Join(Names(), "|"))

		return 2
	}

	d, ok := byName[args[0]]
	if !ok {
		t.sayf("concourse-runner step: no step %q", args[0])

		return 2
	}

	if d.tools && t.Fetch {
		if code := t.fetchTools(t.withoutPlanner(t.Env)); code != 0 {
			return code
		}
	}

	return d.run(t, args[1:])
}

// say prints a line on the task's stderr, where a build's log shows it.
func (t *Task) sayf(format string, args ...any) {
	fmt.Fprintf(t.Stderr, format+"\n", args...) //nolint:errcheck // a log line that can't be written can't be reported either
}

func (t *Task) path(p string) string { return filepath.Join(t.Dir, p) }

func (t *Task) exists(p string) bool {
	_, err := os.Stat(t.path(p))

	return err == nil
}

func (t *Task) isDir(p string) bool {
	info, err := os.Stat(t.path(p))

	return err == nil && info.IsDir()
}

func (t *Task) isFile(p string) bool {
	info, err := os.Stat(t.path(p))

	return err == nil && info.Mode().IsRegular()
}

func (t *Task) executable(p string) bool {
	info, err := os.Stat(t.path(p))

	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

// holds says whether the file holds text; a file that can't be read holds
// nothing.
func (t *Task) holds(p, text string) bool {
	data, err := os.ReadFile(t.path(p))

	return err == nil && strings.Contains(string(data), text)
}

// getenv is the value of name in env, the last one when env holds two.
func getenv(env []string, name string) string {
	for _, kv := range slices.Backward(env) {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			return v
		}
	}

	return ""
}

func (t *Task) getenv(name string) string { return getenv(t.Env, name) }

// must is a variable the step can't do without, as `set -u` held it.
func (t *Task) must(names ...string) bool {
	for _, n := range names {
		found := false

		for _, kv := range t.Env {
			if k, _, ok := strings.Cut(kv, "="); ok && k == n {
				found = true
			}
		}

		if !found {
			t.sayf("%s: parameter not set", n)

			return false
		}
	}

	return true
}

// unset is env without names.
func unset(env []string, names ...string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")

		return slices.Contains(names, k)
	})
}

// with is env with each of kvs ("NAME=value") set.
func with(env []string, kvs ...string) []string {
	for _, kv := range kvs {
		k, _, _ := strings.Cut(kv, "=")
		env = append(unset(env, k), kv)
	}

	return env
}

// command is name with args, in the task's directory: a name with a slash
// is a path from there.
func (t *Task) command(env []string, dir, name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...) //nolint:gosec,noctx // the step's own commands; a task's limit ends it
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, t.Stdout, t.Stderr

	return cmd
}

// call runs a command to its end and returns its exit code.
func (t *Task) call(env []string, name string, args ...string) int {
	return t.exited(name, t.command(env, t.Dir, name, args...).Run())
}

// last runs pipelinegen as a step's last command: in the step's place when
// it replaces.
func (t *Task) last(env []string, args ...string) int {
	if !t.Replace {
		return t.call(env, tool, args...)
	}

	err := syscall.Exec(t.path(tool), append([]string{tool}, args...), env) //nolint:gosec // the step's own last command

	return t.exited(tool, err)
}

// exited is a command's exit code as a shell gives it: 128 and the signal
// for one a signal ended, 126 for one it can't run, 127 for one it can't
// find.
func (t *Task) exited(name string, err error) int {
	if err == nil {
		return 0
	}

	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}

		return exit.ExitCode()
	}

	t.sayf("%s: %v", name, err)

	if errors.Is(err, fs.ErrPermission) {
		return 126
	}

	return 127
}

// writeFile writes a file the job reads, as a shell's echo would.
func (t *Task) writeFile(p, text string) int {
	if err := os.WriteFile(t.path(p), []byte(text), 0o644); err != nil { //nolint:gosec // a file the next step reads
		t.sayf("%v", err)

		return 1
	}

	return 0
}

// move renames a directory as mv does, replacing nothing.
func (t *Task) move(from, to string) int {
	if err := os.Rename(t.path(from), t.path(to)); err != nil {
		t.sayf("mv: %v", err)

		return 1
	}

	return 0
}

func (t *Task) remove(paths ...string) int {
	for _, p := range paths {
		if err := os.RemoveAll(t.path(p)); err != nil {
			t.sayf("rm: %v", err)

			return 1
		}
	}

	return 0
}

// dirs are the directories in dir, sorted as a shell's glob sorts them,
// without the hidden ones a glob leaves out.
func (t *Task) dirs(dir string) []string {
	entries, err := os.ReadDir(t.path(dir))
	if err != nil {
		return nil
	}

	var out []string

	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") || !t.isDir(filepath.Join(dir, e.Name())) {
			continue
		}

		out = append(out, e.Name())
	}

	return out
}
