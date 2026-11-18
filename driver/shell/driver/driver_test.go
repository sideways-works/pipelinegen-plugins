package driver

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

func request(t *testing.T, conf map[string]any, names ...string) plugin.DriverRequest {
	t.Helper()

	tree := t.TempDir()
	if err := os.CopyFS(tree, os.DirFS("testdata/sh")); err != nil {
		t.Fatal(err)
	}

	req := plugin.DriverRequest{
		Protocol: plugin.Protocol, Work: t.TempDir(), Cache: t.TempDir(), Config: conf,
		Tree: tree, Suite: "unit",
	}

	for _, n := range names {
		req.Slices = append(req.Slices, plugin.DriverSlice{Name: n, Sources: []string{n}, Extras: t.TempDir()})
	}

	return req
}

func runOf(t *testing.T, req plugin.DriverRequest) (map[string]plugin.DriverEvent, string) {
	t.Helper()

	var log bytes.Buffer

	got := map[string]plugin.DriverEvent{}

	err := run(t.Context(), req, func(ev plugin.DriverEvent) error {
		if _, dup := got[ev.Slice]; dup {
			t.Errorf("a second end for %s", ev.Slice)
		}

		got[ev.Slice] = ev

		return nil
	}, &log)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, log.String())
	}

	return got, log.String()
}

func failedTests(ev plugin.DriverEvent) []string {
	var out []string
	for _, f := range ev.Failures {
		out = append(out, f.Test)
	}

	return out
}

func TestDescribe(t *testing.T) {
	d := Describe()

	if d.Kind != plugin.KindDriver || !slices.Equal(d.Verbs, plugin.DriverVerbs) || d.Results != plugin.ResultsExit || d.Apart {
		t.Errorf("describe: %+v", d)
	}

	for _, k := range []string{"script", "command", "functions", "shell", "dir", "env", "timeout", "stop", "checks", "caches"} {
		if d.Settings[k] == "" {
			t.Errorf("setting %s undeclared", k)
		}
	}
}

func TestSettingsRefused(t *testing.T) {
	cases := []struct {
		verb string
		conf map[string]any
		want string
	}{
		{plugin.CallRun, map[string]any{"script": "true", "command": []string{"true"}}, "one of script"},
		{plugin.CallRun, map[string]any{}, "a run step needs"},
		{plugin.CallPrepare, map[string]any{}, "a prepare step needs"},
		{plugin.CallSetup, map[string]any{}, "a set-up needs"},
		{plugin.CallPrepare, map[string]any{"functions": "test_"}, "run step's setting"},
		{plugin.CallRun, map[string]any{"script": "true", "stop": "true"}, "set-up's setting"},
		{plugin.CallRun, map[string]any{"functions": "test-"}, "no start of a shell function"},
		{plugin.CallRun, map[string]any{"script": "true", "timeout": "soon"}, "no positive duration"},
		{plugin.CallRun, map[string]any{"script": "true", "shell": []string{"no-such-shell-here"}}, "needs no-such-shell-here"},
	}

	for _, c := range cases {
		_, err := settingsOf(plugin.DriverRequest{Config: c.conf}, c.verb)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %v: %v, want %q", c.verb, c.conf, err, c.want)
		}
	}

	set, err := settingsOf(plugin.DriverRequest{Config: map[string]any{"script": "true"}}, plugin.CallRun)
	if err != nil || !slices.Equal(set.Shell, []string{"sh", "-eu"}) {
		t.Errorf("a script's default shell: %v %v", set.Shell, err)
	}

	set, err = settingsOf(plugin.DriverRequest{Config: map[string]any{"functions": "test_"}}, plugin.CallRun)
	if err != nil || !slices.Equal(set.Shell, []string{"sh"}) {
		t.Errorf("test functions' default shell: %v %v", set.Shell, err)
	}
}

func TestFunctions(t *testing.T) {
	for _, batch := range []bool{false, true} {
		req := request(t, map[string]any{"functions": "test_"}, "calc/add_test.sh", "calc/neg_test.sh", "calc/gone_test.sh")
		req.Batch = batch

		got, log := runOf(t, req)

		add, neg, gone := got["calc/add_test.sh"], got["calc/neg_test.sh"], got["calc/gone_test.sh"]

		if !add.Passed || add.Tests == nil || *add.Tests != 2 || len(add.Failures) != 0 {
			t.Errorf("add: %+v", add)
		}

		if neg.Passed || neg.Tests == nil || *neg.Tests != 2 || !slices.Equal(failedTests(neg), []string{"test_neg_twice"}) {
			t.Errorf("neg: %+v", neg)
		}

		if tail := neg.Failures[0].Tail; tail != "neg of neg\nneg(neg 4) = 4, want 5\n" {
			t.Errorf("neg's tail: %q", tail)
		}

		if gone.Passed || !slices.Equal(failedTests(gone), []string{"calc/gone_test.sh"}) || gone.Failures[0].Tail != "no such file\n" {
			t.Errorf("a missing file: %+v", gone)
		}

		for _, line := range []string{
			"ok   calc/add_test.sh test_add\n", "ok   calc/add_test.sh test_add_negative\n",
			"ok   calc/neg_test.sh test_neg\n", "FAIL calc/neg_test.sh test_neg_twice\n     neg of neg\n     neg(neg 4) = 4, want 5\n",
		} {
			if !strings.Contains(log, line) {
				t.Errorf("the log lacks %q:\n%s", line, log)
			}
		}

		if strings.Contains(log, "helper_not_a_test") || strings.Contains(log, "never runs") {
			t.Errorf("a function without the prefix ran:\n%s", log)
		}
	}
}

func TestFunctionsTimeout(t *testing.T) {
	req := request(t, map[string]any{"functions": "test_", "timeout": "300ms"}, "slow_test.sh")
	write(t, req.Tree, "slow_test.sh", "test_slow() {\n\techo started\n\tsleep 30\n}\n\ntest_after() {\n\ttrue\n}\n")

	start := time.Now()
	got, _ := runOf(t, req)

	ev := got["slow_test.sh"]
	if ev.Passed || !slices.Equal(failedTests(ev), []string{"test_slow"}) || !strings.Contains(ev.Failures[0].Tail, "time limit of 300ms ran out") {
		t.Errorf("a slow test: %+v", ev)
	}

	if took := time.Since(start); took > 15*time.Second {
		t.Errorf("the limit held only after %s", took)
	}
}

func write(t *testing.T, dir, name, text string) {
	t.Helper()

	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("read: %v", err)
	}

	return string(data)
}

func TestScriptPerSlice(t *testing.T) {
	script := `echo "slice=$PIPELINEGEN_SLICE source=$PIPELINEGEN_SOURCE tests=$PIPELINEGEN_TESTS here=$(pwd) greet=$GREETING"
case $PIPELINEGEN_SLICE in
a) exit 0 ;;
*) echo "b fails"; exit 3 ;;
esac`
	req := request(t, map[string]any{"script": script, "dir": "calc", "env": map[string]string{"GREETING": "hi"}}, "a", "b")
	req.Slices[0].Tests = []string{"TestOne", "TestTwo"}

	got, log := runOf(t, req)

	if !got["a"].Passed || got["a"].Seconds <= 0 {
		t.Errorf("a: %+v", got["a"])
	}

	b := got["b"]
	if b.Passed || !slices.Equal(failedTests(b), []string{"b"}) || !strings.HasSuffix(b.Failures[0].Tail, "b fails\n") {
		t.Errorf("b: %+v", b)
	}

	want := "slice=a source=" + filepath.Join(req.Tree, "a") + " tests=TestOne,TestTwo here=" + filepath.Join(req.Tree, "calc") + " greet=hi\n"
	if !strings.Contains(log, want) {
		t.Errorf("the log lacks %q:\n%s", want, log)
	}
}

func TestCommand(t *testing.T) {
	req := request(t, map[string]any{"command": []string{"test", "-f", "calc/lib.sh"}}, "calc/add_test.sh")

	got, _ := runOf(t, req)
	if !got["calc/add_test.sh"].Passed {
		t.Errorf("a command that exits 0: %+v", got)
	}

	req = request(t, map[string]any{"command": []string{"no-such-command-here"}}, "calc/add_test.sh")

	got, _ = runOf(t, req)
	if ev := got["calc/add_test.sh"]; ev.Passed || !strings.Contains(ev.Failures[0].Tail, "it did not run") {
		t.Errorf("a command that can't start: %+v", ev)
	}
}

func TestShellSetting(t *testing.T) {
	req := request(t, map[string]any{"script": `[[ "$PIPELINEGEN_SLICE" == a ]]`, "shell": []string{"bash", "-eu"}}, "a")

	got, log := runOf(t, req)
	if !got["a"].Passed {
		t.Errorf("a bash script: %+v\n%s", got["a"], log)
	}
}

func TestBundleResults(t *testing.T) {
	script := `printf '%s\n' "$PIPELINEGEN_SLICES" >"$PIPELINEGEN_WORK/seen"
cp "$PIPELINEGEN_SLICES_FILE" "$PIPELINEGEN_WORK/file"
echo '{"passed":false,"tests":4,"failures":[{"test":"TestX","tail":"x broke\n"}]}' >"$PIPELINEGEN_RESULTS/dir/json.json"
echo 0 >"$PIPELINEGEN_RESULTS/status0.status"
echo 2 >"$PIPELINEGEN_RESULTS/status2.status"
echo 'not json' >"$PIPELINEGEN_RESULTS/broken.json"
exit "$EXIT"`

	for _, exit := range []string{"0", "1"} {
		req := request(t, map[string]any{"script": script, "env": map[string]string{"EXIT": exit}}, "dir/json", "status0", "status2", "broken", "none")
		req.Batch = true

		got, _ := runOf(t, req)

		if j := got["dir/json"]; j.Passed || *j.Tests != 4 || !slices.Equal(failedTests(j), []string{"TestX"}) {
			t.Errorf("exit %s: a result file: %+v", exit, j)
		}

		if !got["status0"].Passed || got["status2"].Passed || !strings.HasSuffix(got["status2"].Failures[0].Tail, "exit status 2\n") {
			t.Errorf("exit %s: status files: %+v %+v", exit, got["status0"], got["status2"])
		}

		if b := got["broken"]; b.Passed || !strings.Contains(b.Failures[0].Tail, "is no result") {
			t.Errorf("exit %s: a broken result file: %+v", exit, b)
		}

		none, ok := got["none"]

		switch {
		case exit == "0" && (!ok || !none.Passed):
			t.Errorf("a bundle that exits 0 passes a slice it wrote nothing for: %+v %v", none, ok)
		case exit == "1" && ok:
			t.Errorf("a bundle that failed ends a slice it wrote nothing for: %+v", none)
		}

		seen, file := read(t, filepath.Join(req.Work, "seen")), read(t, filepath.Join(req.Work, "file"))

		if want := "dir/json\nstatus0\nstatus2\nbroken\nnone\n"; seen != want || file != want {
			t.Errorf("the names: %q and %q, want %q", seen, file, want)
		}
	}
}

func TestBundleTimeoutPerSlice(t *testing.T) {
	// Two slices of 300ms each: a bundle sleeping 0.4s keeps within its 600ms.
	req := request(t, map[string]any{"script": "sleep 0.4", "timeout": "300ms"}, "a", "b")
	req.Batch = true

	got, _ := runOf(t, req)
	if !got["a"].Passed || !got["b"].Passed {
		t.Errorf("a bundle's limit is the timeout per slice: %+v", got)
	}

	req = request(t, map[string]any{"script": "sleep 30", "timeout": "200ms"}, "a")

	got, _ = runOf(t, req)
	if a := got["a"]; a.Passed || !strings.Contains(a.Failures[0].Tail, "time limit of 200ms ran out") {
		t.Errorf("a slice past its limit: %+v", a)
	}
}

func TestDeadline(t *testing.T) {
	req := request(t, map[string]any{"script": "sleep 30"}, "a", "b")
	// The minute kept for reporting is already gone.
	req.Deadline = time.Now().Add(deadlineMargin + time.Second).UTC().Format(time.RFC3339)

	start := time.Now()
	got, _ := runOf(t, req)

	if len(got) != 0 {
		t.Errorf("slices the step's time ran out on got ends: %+v", got)
	}

	if took := time.Since(start); took > 20*time.Second {
		t.Errorf("the deadline held only after %s", took)
	}
}

func TestEnvironment(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "tool-token")
	t.Setenv("GH_TOKEN", "tool-token")
	t.Setenv("PIPELINEGEN_TRUSTED", "trusted")
	t.Setenv("PIPELINEGEN_RUN", "main")
	t.Setenv("PIPELINEGEN_COMMIT", "0123abcd")
	t.Setenv("PIPELINEGEN_CHANGE", "pr-12")
	t.Setenv("PLAIN", "plain")

	script := `env >"$PIPELINEGEN_WORK/env"`
	req := request(t, map[string]any{"script": script}, "a")
	req.Env = map[string]string{"SECRET_DECLARED": "s", "WORDS": "http://127.0.0.1:8080"}
	req.Record = true
	req.Slices[0].Recording = filepath.Join(t.TempDir(), "rec")

	runOf(t, req)

	data, err := os.ReadFile(filepath.Join(req.Work, "env"))
	if err != nil {
		t.Fatal(err)
	}

	env := string(data)

	for _, want := range []string{"PLAIN=plain\n", "PIPELINEGEN_RUN=main\n", "PIPELINEGEN_COMMIT=0123abcd\n", "SECRET_DECLARED=s\n", "WORDS=http://127.0.0.1:8080\n",
		"PIPELINEGEN_TREE=" + req.Tree + "\n", "PIPELINEGEN_RECORDING=" + req.Slices[0].Recording + "\n"} {
		if !strings.Contains(env, want) {
			t.Errorf("the script's environment lacks %q", want)
		}
	}

	for _, gone := range []string{"tool-token", "PIPELINEGEN_TRUSTED", "PIPELINEGEN_CHANGE"} {
		if strings.Contains(env, gone) {
			t.Errorf("the script's environment holds %q", gone)
		}
	}

	// A token the suite declares for its batches reaches its scripts.
	req.Env["GITHUB_TOKEN"] = "declared"

	runOf(t, req)

	data = []byte(read(t, filepath.Join(req.Work, "env")))
	if !strings.Contains(string(data), "GITHUB_TOKEN=declared\n") || strings.Contains(string(data), "tool-token") {
		t.Errorf("a declared token: %s", data)
	}
}

func TestMarks(t *testing.T) {
	marks := filepath.Join(t.TempDir(), "marks")
	mark := filepath.Join(t.TempDir(), "mark.sh")
	write(t, filepath.Dir(mark), "mark.sh", "echo \"$1 $2\" >>"+marks+"\n[ \"$2\" != refused ] || [ \"$1\" != start ]\n")

	req := request(t, map[string]any{"script": `echo "ran $PIPELINEGEN_SLICE" >>` + marks}, "a", "b", "refused")
	req.Batch, req.Record, req.Stack = true, true, true
	req.Marks = &plugin.Marks{Command: []string{"sh", mark}, Dir: t.TempDir()}

	got, log := runOf(t, req)

	data := read(t, marks)
	if want := "start a\nran a\nstop a\nstart b\nran b\nstop b\nstart refused\n"; data != want {
		t.Errorf("marks %q, want %q", data, want)
	}

	if !got["a"].Passed || !got["b"].Passed || got["refused"].Passed || !strings.Contains(got["refused"].Failures[0].Tail, "start mark") {
		t.Errorf("results: %+v", got)
	}

	if !strings.Contains(log, "run one at a time, marked") {
		t.Errorf("the log doesn't say it marks:\n%s", log)
	}
}

func TestPrepare(t *testing.T) {
	req := request(t, map[string]any{"script": `echo built >"$PIPELINEGEN_PREPARED/out"`})
	req.Prepared = t.TempDir()

	var log bytes.Buffer
	if err := prepare(t.Context(), req, &log); err != nil {
		t.Fatalf("prepare: %v\n%s", err, log.String())
	}

	if data := read(t, filepath.Join(req.Prepared, "out")); data != "built\n" {
		t.Errorf("prepared %q", data)
	}

	req.Config = map[string]any{"script": "echo cannot; exit 4"}
	if err := prepare(t.Context(), req, &log); err == nil || !strings.Contains(err.Error(), "exit status 4") {
		t.Errorf("a failing prepare: %v", err)
	}

	req.Config = map[string]any{"script": "sleep 30", "timeout": "200ms"}
	if err := prepare(t.Context(), req, &log); err == nil || !strings.Contains(err.Error(), "time ran out") {
		t.Errorf("a prepare past its limit: %v", err)
	}
}

func TestSetup(t *testing.T) {
	stopped := filepath.Join(t.TempDir(), "stopped")
	req := request(t, map[string]any{
		"script": "echo PORT=8080 >>\"$PIPELINEGEN_EXPORT\"\necho 'URL=http://127.0.0.1:8080/a=b' >>\"$PIPELINEGEN_EXPORT\"",
		"stop":   "echo \"stopped $PORT $URL\" >" + stopped,
	})

	stop := make(chan struct{})
	done := make(chan error, 1)
	exported := make(chan map[string]string, 1)

	var log bytes.Buffer

	go func() {
		done <- setup(t.Context(), req, func(env map[string]string) error {
			exported <- env

			return nil
		}, stop, &log)
	}()

	select {
	case env := <-exported:
		if env["PORT"] != "8080" || env["URL"] != "http://127.0.0.1:8080/a=b" || len(env) != 2 {
			t.Errorf("exports %v", env)
		}
	case err := <-done:
		t.Fatalf("the set-up ended before it was ready: %v\n%s", err, log.String())
	}

	if _, err := os.Stat(stopped); err == nil {
		t.Error("the stop script ran before the stop")
	}

	close(stop)

	if err := <-done; err != nil {
		t.Fatalf("stop: %v", err)
	}

	if data := read(t, stopped); data != "stopped 8080 http://127.0.0.1:8080/a=b\n" {
		t.Errorf("the stop script saw %q", data)
	}
}

func TestSetupRefusesBadExports(t *testing.T) {
	req := request(t, map[string]any{"script": "echo 'not an export' >>\"$PIPELINEGEN_EXPORT\""})

	err := setup(t.Context(), req, func(map[string]string) error {
		t.Error("ready after a bad export file")

		return nil
	}, make(chan struct{}), &bytes.Buffer{})
	if !errors.Is(err, errExport) {
		t.Errorf("a bad export line: %v", err)
	}
}

func TestSetupFails(t *testing.T) {
	req := request(t, map[string]any{"script": "exit 5"})

	err := setup(context.Background(), req, func(map[string]string) error {
		t.Error("ready after a failed script")

		return nil
	}, make(chan struct{}), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "exit status 5") {
		t.Errorf("a failing set-up: %v", err)
	}
}

func TestTestFunctions(t *testing.T) {
	src := "test_a() {\n}\ntest_b () {\n}\n  test_c() {\n}\ntest_d(){ true; }\nnot_test_e() {\n}\n"
	if got := testFunctions([]byte(src), "test_"); !slices.Equal(got, []string{"test_a", "test_d"}) {
		t.Errorf("functions %v", got)
	}
}

func alive(t *testing.T, pidFile string) bool {
	t.Helper()

	pid, err := strconv.Atoi(strings.TrimSpace(read(t, pidFile)))
	if err != nil {
		t.Fatalf("pid: %v", err)
	}

	return syscall.Kill(pid, 0) == nil
}

func TestNothingOutlivesItsCall(t *testing.T) {
	dir := t.TempDir()
	script := `echo "$(ps -o pgid= -p $$ | tr -d ' ')" >` + dir + `/pgid
sleep 300 &
echo $! >` + dir + `/bg`

	req := request(t, map[string]any{"script": script}, "a")

	got, _ := runOf(t, req)
	if !got["a"].Passed {
		t.Fatalf("a: %+v", got["a"])
	}

	if want := strconv.Itoa(syscall.Getpgrp()); strings.TrimSpace(read(t, dir+"/pgid")) != want {
		t.Errorf("the script ran in process group %s, the driver's is %s", read(t, dir+"/pgid"), want)
	}

	if alive(t, dir+"/bg") {
		t.Error("a process the script left running outlived it")
	}

	req = request(t, map[string]any{"script": "sleep 300 &\necho $! >" + dir + "/slow\nwait", "timeout": "300ms"}, "b")

	got, _ = runOf(t, req)
	if got["b"].Passed {
		t.Errorf("b passed past its limit: %+v", got["b"])
	}

	if alive(t, dir+"/slow") {
		t.Error("a process the script started outlived its time limit")
	}
}
