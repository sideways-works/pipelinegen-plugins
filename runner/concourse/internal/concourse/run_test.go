package concourse

import (
	"errors"
	"os/exec"
	"strconv"
	"testing"
)

// stepScript is the script a step's task runs, as the pipeline holds it
// without the bootstrap before it.
func stepScript(t *testing.T, name string, args ...string) string {
	t.Helper()

	switch name {
	case "tools":
		return bootstrapScript
	case "plan":
		return planLib + planScript
	case "continuous-plan":
		minutes := 60

		if len(args) == 2 && args[0] == "--plan-minutes" {
			n, err := strconv.Atoi(args[1])
			if err != nil {
				t.Fatal(err)
			}

			minutes = n
		}

		return planLib + continuousPlanFor(minutes)
	case "begin":
		return proposedBegin
	case "proposed-plan":
		return planLib + proposedPlan
	case "proposed-write":
		return planLib + proposedWrite
	case "sweep":
		return planLib + sweepScript
	case "sweep-plan":
		return planLib + sweepPlan
	case "sweep-write":
		if len(args) == 1 && args[0] == "--connector" {
			return planLib + connectorHandOff + sweepWrite
		}

		return planLib + sweepWrite
	case "stale":
		return staleScript
	case "start":
		return startScript
	case "end":
		return endTaskScript()
	case "report-error":
		return fallbackScript
	}

	t.Fatalf("no step %s", name)

	return ""
}

// errExit is a step that exited other than 0.
var errExit = errors.New("exit")

// runStep runs a step in work, with env as the task's whole environment,
// and returns what it printed and its exit code.
func runStep(t *testing.T, work string, env []string, name string, args ...string) ([]byte, int) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "sh", "-ec", stepScript(t, name, args...))
	cmd.Dir, cmd.Env = work, env

	out, err := cmd.CombinedOutput()

	var exit *exec.ExitError

	switch {
	case err == nil:
		return out, 0
	case errors.As(err, &exit):
		return out, exit.ExitCode()
	}

	t.Fatalf("%s: %v\n%s", name, err, out)

	return nil, 0
}

// mustStep runs a step that has to succeed.
func mustStep(t *testing.T, work string, env []string, name string, args ...string) []byte {
	t.Helper()

	out, code := runStep(t, work, env, name, args...)
	if code != 0 {
		t.Fatalf("%s %v: exit %d\n%s", name, args, code, out)
	}

	return out
}
