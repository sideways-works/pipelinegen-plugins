package driver

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

func checksConf(checks ...map[string]any) map[string]any {
	list := make([]any, 0, len(checks))
	for _, c := range checks {
		list = append(list, c)
	}

	return map[string]any{"checks": list}
}

func tailOf(ev plugin.DriverEvent, test string) string {
	for _, f := range ev.Failures {
		if f.Test == test {
			return f.Tail
		}
	}

	return "(no failure of " + test + ")"
}

func TestEveryCheckIsATest(t *testing.T) {
	conf := checksConf(
		map[string]any{"name": "first", "run": "echo first of $PIPELINEGEN_SLICE"},
		map[string]any{"name": "second fails", "run": "echo second went wrong\nexit 3"},
		map[string]any{"name": "third", "run": "echo third ran"},
	)

	got, log := runOf(t, request(t, conf, "a", "b"))

	for _, name := range []string{"a", "b"} {
		ev := got[name]
		if ev.Passed || !slices.Equal(failedTests(ev), []string{"second fails"}) || ev.Tests == nil || *ev.Tests != 3 {
			t.Errorf("%s: %+v", name, ev)
		}

		if tail := tailOf(ev, "second fails"); !strings.Contains(tail, "second went wrong") || !strings.Contains(tail, "exit status 3") || strings.Contains(tail, "first of") {
			t.Errorf("%s: the failing check's tail is its own: %q", name, tail)
		}
	}

	for _, want := range []string{"== a: first", "first of a", "ok   a first", "FAIL a second fails", "third ran", "ok   b third"} {
		if !strings.Contains(log, want) {
			t.Errorf("the log lacks %q:\n%s", want, log)
		}
	}

	conf = checksConf(map[string]any{"name": "only", "run": "true"})
	req := request(t, conf, "a", "b")
	req.Batch = true

	got, _ = runOf(t, req)
	if !got["a"].Passed || !got["b"].Passed {
		t.Errorf("a bundle's slices each run the checks: %+v", got)
	}
}

func TestAHungCheckFailsAsItself(t *testing.T) {
	dir := t.TempDir()
	conf := checksConf(
		map[string]any{"name": "hangs", "run": "echo started\nsleep 300 &\necho $! >" + dir + "/bg\nwait", "timeout": "300ms"},
		map[string]any{"name": "after", "run": "echo after >" + dir + "/after"},
	)

	start := time.Now()
	got, _ := runOf(t, request(t, conf, "a"))

	ev := got["a"]
	if ev.Passed || !slices.Equal(failedTests(ev), []string{"hangs"}) || ev.Tests == nil || *ev.Tests != 2 {
		t.Errorf("a: %+v", ev)
	}

	if tail := tailOf(ev, "hangs"); !strings.Contains(tail, "started") || !strings.Contains(tail, "the check's time limit of 300ms ran out") {
		t.Errorf("the hung check's tail: %q", tail)
	}

	if read(t, dir+"/after") != "after\n" {
		t.Error("the check after the hung one didn't run")
	}

	if alive(t, dir+"/bg") {
		t.Error("a process the hung check started outlived it")
	}

	if took := time.Since(start); took > 15*time.Second {
		t.Errorf("the check's limit held only after %s", took)
	}
}

func TestAFailedNeedLeavesItsChecksNotRun(t *testing.T) {
	dir := t.TempDir()
	conf := checksConf(
		map[string]any{"name": "build", "run": "exit 1"},
		map[string]any{"name": "test", "run": "touch " + dir + "/tested", "need": []any{"build"}},
		map[string]any{"name": "deploy check", "run": "true", "need": []any{"test"}},
		map[string]any{"name": "format", "run": "touch " + dir + "/formatted"},
	)

	got, _ := runOf(t, request(t, conf, "a"))

	ev := got["a"]
	if ev.Passed || !slices.Equal(failedTests(ev), []string{"build", "test", "deploy check"}) || ev.Tests == nil || *ev.Tests != 2 {
		t.Errorf("a: %+v", ev)
	}

	if tail := tailOf(ev, "test"); !strings.Contains(tail, "not run: it needs build") {
		t.Errorf("the check that needs a failed one: %q", tail)
	}

	if tail := tailOf(ev, "deploy check"); !strings.Contains(tail, "not run: it needs test") {
		t.Errorf("the check that needs one not run: %q", tail)
	}

	if _, err := os.Stat(filepath.Join(dir, "tested")); err == nil {
		t.Error("a check whose need failed ran")
	}

	if _, err := os.Stat(filepath.Join(dir, "formatted")); err != nil {
		t.Error("a check that needs nothing didn't run")
	}
}

func TestTheSliceLimitHoldsOverItsChecks(t *testing.T) {
	conf := checksConf(
		map[string]any{"name": "quick", "run": "true"},
		map[string]any{"name": "slow", "run": "sleep 30"},
		map[string]any{"name": "last", "run": "true"},
	)
	conf["timeout"] = "500ms"

	got, _ := runOf(t, request(t, conf, "a"))

	ev := got["a"]
	if ev.Passed || !slices.Equal(failedTests(ev), []string{"slow", "last"}) {
		t.Errorf("a: %+v", ev)
	}

	if tail := tailOf(ev, "slow"); !strings.Contains(tail, "the slice's time limit of 500ms ran out") {
		t.Errorf("the stopped check: %q", tail)
	}

	if tail := tailOf(ev, "last"); !strings.Contains(tail, "not run: the slice's time limit of 500ms ran out") {
		t.Errorf("the check never run: %q", tail)
	}
}

func TestTheStepsTimeEndsChecksNeverPassed(t *testing.T) {
	conf := checksConf(
		map[string]any{"name": "slow", "run": "sleep 30"},
		map[string]any{"name": "last", "run": "true"},
	)

	req := request(t, conf, "a", "b")
	req.Deadline = time.Now().Add(deadlineMargin + 3*time.Second).UTC().Format(time.RFC3339)

	start := time.Now()
	got, log := runOf(t, req)

	if len(got) != 0 {
		t.Errorf("slices the step's time ran out on got ends: %+v", got)
	}

	for _, want := range []string{`during check "slow" of a`, "never run in a: last", "a has no result"} {
		if !strings.Contains(log, want) {
			t.Errorf("the log lacks %q:\n%s", want, log)
		}
	}

	conf = checksConf(
		map[string]any{"name": "broken", "run": "exit 1"},
		map[string]any{"name": "slow", "run": "sleep 30"},
		map[string]any{"name": "last", "run": "true"},
	)

	req = request(t, conf, "a")
	req.Deadline = time.Now().Add(deadlineMargin + 3*time.Second).UTC().Format(time.RFC3339)

	got, _ = runOf(t, req)

	ev := got["a"]
	if ev.Passed || !slices.Equal(failedTests(ev), []string{"broken", "slow", "last"}) {
		t.Errorf("a: %+v", ev)
	}

	if tail := tailOf(ev, "slow"); !strings.Contains(tail, "stopped: the step's time ran out") {
		t.Errorf("the stopped check: %q", tail)
	}

	if tail := tailOf(ev, "last"); !strings.Contains(tail, "not run: the step's time ran out") {
		t.Errorf("the check never run: %q", tail)
	}

	if took := time.Since(start); took > 40*time.Second {
		t.Errorf("the deadline held only after %s", took)
	}
}

func TestChecksRefused(t *testing.T) {
	cases := []struct {
		verb string
		conf map[string]any
		want string
	}{
		{plugin.CallRun, map[string]any{"script": "true", "checks": []any{map[string]any{"name": "a", "run": "true"}}}, "one of script"},
		{plugin.CallSetup, checksConf(map[string]any{"name": "a", "run": "true"}), "run step's setting"},
		{plugin.CallRun, checksConf(map[string]any{"run": "true"}), "has no name"},
		{plugin.CallRun, checksConf(map[string]any{"name": "a"}), "nothing to run"},
		{plugin.CallRun, checksConf(map[string]any{"name": "a", "run": "true"}, map[string]any{"name": "a", "run": "true"}), "two checks are named"},
		{plugin.CallRun, checksConf(map[string]any{"name": "a", "run": "true", "need": []any{"b"}}, map[string]any{"name": "b", "run": "true"}), "no check before it"},
		{plugin.CallRun, checksConf(map[string]any{"name": "a", "run": "true", "need": []any{"a"}}), "no check before it"},
		{plugin.CallRun, checksConf(map[string]any{"name": "a", "run": "true", "timeout": "soon"}), "no positive duration"},
		{plugin.CallRun, checksConf(map[string]any{"name": "a", "run": "true", "timout": "1m"}), "checks[0].timout: unknown key; the keys of a check are name, run, timeout, need"},
	}

	for _, c := range cases {
		_, err := settingsOf(plugin.DriverRequest{Config: c.conf}, c.verb)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %v: %v, want %q", c.verb, c.conf, err, c.want)
		}
	}
}
