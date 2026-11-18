package concourse

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen-runner-concourse/internal/runstep"
)

// errExit is a step that exited other than 0.
var errExit = errors.New("exit")

// taskStep runs the runner's step in work as its task does once the
// bootstrap placed the runner, with env as the task's whole environment,
// and returns what it printed and its exit code. Only the tools step
// fetches the tools.
func taskStep(t *testing.T, work string, env []string, name string, args ...string) ([]byte, int) {
	t.Helper()

	var out bytes.Buffer

	task := &runstep.Task{
		Dir: work, Env: env, Stdout: &out, Stderr: &out,
		Fetch: name == "tools", HTTP: &http.Client{}, Now: time.Now,
	}

	code := task.Run(append([]string{name}, args...))

	return out.Bytes(), code
}

// mustStep runs a step that has to succeed.
func mustStep(t *testing.T, work string, env []string, name string, args ...string) []byte {
	t.Helper()

	out, code := taskStep(t, work, env, name, args...)
	if code != 0 {
		t.Fatalf("%s %v: exit %d\n%s", name, args, code, out)
	}

	return out
}

// runsTheTool says whether a task's run runs pipelinegen, by itself or
// through the runner's step.
func runsTheTool(run M) bool {
	text := fmt.Sprint(run["path"], run["args"])

	return strings.HasPrefix(text, "tools/pipelinegen") || strings.Contains(text, "tools/pipelinegen ") ||
		strings.Contains(text, bootstrapScript)
}

// stepOf is the runner's step a task's run runs, with its args; nothing for
// a run that runs none.
func stepOf(run M) []string {
	args := asSlice(run["args"])
	if run["path"] != "sh" || len(args) < 4 || !strings.Contains(fmt.Sprint(args[1]), bootstrapScript) {
		return nil
	}

	out := make([]string, 0, len(args)-3)
	for _, a := range args[3:] {
		out = append(out, fmt.Sprint(a))
	}

	return out
}
