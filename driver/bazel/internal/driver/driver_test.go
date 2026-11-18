package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen-plugins/driver/bazel/internal/bazel"
)

// A target in cut is never reached: the file then ends without its last
// message, as a dead server leaves it.
type fakeBazel struct {
	t     *testing.T
	out   string
	table map[string]string // label -> PASSED, FAILED, BROKEN, CACHED
	cut   map[string]bool
	calls []bazel.Invocation
}

func (f *fakeBazel) Run(_ context.Context, inv bazel.Invocation) error {
	f.calls = append(f.calls, inv)

	var lines []string

	complete := true

	for _, label := range inv.Targets {
		if f.cut[label] {
			complete = false

			continue
		}

		status := f.table[label]
		if status == "BROKEN" {
			lines = append(lines, fmt.Sprintf(`{"id":{"targetCompleted":{"label":%q}},"completed":{"success":false,"failureDetail":{"message":"compile error in %s"}}}`, label, label))

			continue
		}

		dir := filepath.Join(f.out, strings.NewReplacer("//", "", ":", "/").Replace(label))
		if err := os.MkdirAll(dir, 0o750); err != nil {
			f.t.Fatal(err)
		}

		write := func(name, body string) {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				f.t.Fatal(err)
			}
		}

		write("test.log", "log of "+label+"\n")
		write("test.xml", `<testsuites><testsuite><testcase name="TestA"/><testcase name="TestB">`+map[bool]string{true: `<failure message="boom">it broke</failure>`}[status == "FAILED"]+`</testcase></testsuite></testsuites>`)
		write("coverage.dat", "SF:"+label+"\nDA:1,1\nend_of_record\n")

		cached := status == "CACHED"
		if cached {
			status = "PASSED"
		}

		outs := []map[string]string{
			{"name": "test.log", "uri": "file://" + filepath.Join(dir, "test.log")},
			{"name": "test.xml", "uri": "file://" + filepath.Join(dir, "test.xml")},
		}
		if inv.Record {
			outs = append(outs, map[string]string{"name": "test.lcov", "uri": "file://" + filepath.Join(dir, "coverage.dat")})
		}

		body, err := json.Marshal(map[string]any{"status": status, "cachedLocally": cached, "testAttemptDuration": "1.5s", "testActionOutput": outs})
		if err != nil {
			f.t.Fatal(err)
		}

		lines = append(lines, fmt.Sprintf(`{"id":{"testResult":{"label":%q,"shard":1,"run":1,"attempt":1}},"testResult":%s}`, label, body),
			fmt.Sprintf(`{"id":{"testSummary":{"label":%q}},"testSummary":{"overallStatus":%q}}`, label, status))
	}

	if complete {
		lines = append(lines, `{"id":{"lastMessage":{}},"lastMessage":true}`)
	}

	if err := os.WriteFile(inv.Events, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return fmt.Errorf("events: %w", err)
	}

	return nil
}

func setup(t *testing.T, batch bool, table map[string]string) (*fakeBazel, Request) {
	t.Helper()

	dir := t.TempDir()
	fb := &fakeBazel{t: t, out: filepath.Join(dir, "testlogs"), table: table, cut: map[string]bool{}}

	req := Request{Workspace: dir, Root: filepath.Join(dir, "root"), Work: filepath.Join(dir, "work"), Settings: Settings{Batch: batch}}

	for _, label := range slices.Sorted(func(yield func(string) bool) {
		for l := range table {
			if !yield(l) {
				return
			}
		}
	}) {
		name := strings.TrimPrefix(label, "//")
		req.Slices = append(req.Slices, Slice{
			Name: name, Label: label, Kind: "go_test",
			Extras: filepath.Join(dir, "extras", name), Recording: filepath.Join(dir, "coverage", name),
		})
	}

	return fb, req
}

func collect(t *testing.T, fb *fakeBazel, req Request) map[string]Result {
	t.Helper()

	out := map[string]Result{}

	if err := Run(t.Context(), fb, req, func(r Result) { out[r.Slice] = r }); err != nil {
		t.Fatal(err)
	}

	return out
}

var mixed = map[string]string{"//a:a_test": "PASSED", "//b:b_test": "FAILED", "//c:c_test": "BROKEN", "//d:d_test": "CACHED"}

func TestABundleAndOneByOneAgree(t *testing.T) {
	fb, req := setup(t, true, mixed)
	bundled := collect(t, fb, req)

	if len(fb.calls) != 1 || len(fb.calls[0].Targets) != 4 {
		t.Fatalf("bundled calls %+v", fb.calls)
	}

	fb2, req2 := setup(t, false, mixed)
	apart := collect(t, fb2, req2)

	if len(fb2.calls) != 4 {
		t.Fatalf("one by one: %d calls", len(fb2.calls))
	}

	if !reflect.DeepEqual(bundled, apart) {
		t.Errorf("bundled %+v\napart %+v", bundled, apart)
	}

	a, b, c, d := bundled["a:a_test"], bundled["b:b_test"], bundled["c:c_test"], bundled["d:d_test"]
	if !a.Passed || a.Tests != 2 || a.Replayed || a.Seconds != 1.5 {
		t.Errorf("a %+v", a)
	}

	if b.Passed || len(b.Failures) != 1 || b.Failures[0].Test != "TestB" || !strings.Contains(b.Failures[0].Tail, "it broke") {
		t.Errorf("b %+v", b)
	}

	if c.Passed || c.Status != "FAILED_TO_BUILD" || !strings.Contains(c.Failures[0].Tail, "compile error") {
		t.Errorf("c %+v", c)
	}

	if !d.Passed || !d.Replayed {
		t.Errorf("d %+v", d)
	}

	if data, err := os.ReadFile(filepath.Join(req.Slices[0].Extras, "test.log")); err != nil || !strings.Contains(string(data), "log of //a:a_test") {
		t.Errorf("a's log %q %v", data, err)
	}
}

func TestADeadServerLeavesSlicesWithoutAResult(t *testing.T) {
	fb, req := setup(t, true, map[string]string{"//a:a_test": "PASSED", "//b:b_test": "PASSED"})
	fb.cut["//b:b_test"] = true

	got := collect(t, fb, req)
	if _, ok := got["b:b_test"]; ok || !got["a:a_test"].Passed {
		t.Errorf("%+v", got)
	}
}

func TestARecordingKeepsEachTargetsCoverage(t *testing.T) {
	fb, req := setup(t, true, map[string]string{"//a:a_test": "PASSED", "//b:b_test": "FAILED"})
	req.Record = true

	got := collect(t, fb, req)

	for _, sl := range req.Slices {
		data, err := os.ReadFile(filepath.Join(sl.Recording, "coverage.lcov"))
		if err != nil || !strings.Contains(string(data), "SF:"+sl.Label) || !got[sl.Name].Recorded {
			t.Errorf("%s: %q %v %+v", sl.Name, data, err, got[sl.Name])
		}
	}

	if !fb.calls[0].Record {
		t.Error("a recording run is a coverage invocation")
	}
}

func TestFilteredSlicesOfOneTarget(t *testing.T) {
	fb, req := setup(t, true, map[string]string{"//a:a_test": "PASSED"})
	req.Slices = []Slice{
		{Name: "one", Label: "//a:a_test", Kind: "go_test", Tests: []string{"TestA"}},
		{Name: "two", Label: "//a:a_test", Kind: "go_test", Tests: []string{"TestB", "TestC.x"}},
		{Name: "shell", Label: "//a:a_test", Kind: "sh_test", Tests: []string{"t1"}},
	}

	got := collect(t, fb, req)

	if len(fb.calls) != 2 || fb.calls[0].TestFilter != "^(TestA)$" || fb.calls[1].TestFilter != `^(TestB|TestC\.x)$` {
		t.Errorf("calls %+v", fb.calls)
	}

	if got["shell"].Status != "UNFILTERED" || got["shell"].Passed || !got["one"].Passed || !got["two"].Passed {
		t.Errorf("%+v", got)
	}
}

func TestAStackSuiteIsNeverReplayed(t *testing.T) {
	fb, req := setup(t, true, map[string]string{"//a:a_test": "PASSED", "//b:b_test": "PASSED"})
	req.Stack = true
	collect(t, fb, req)

	if !slices.Contains(bazel.Args(fb.calls[0]), "--nocache_test_results") || slices.Contains(bazel.Args(fb.calls[0]), "--local_test_jobs=1") {
		t.Errorf("stack, not recording: %v", bazel.Args(fb.calls[0]))
	}

	fb, req = setup(t, true, map[string]string{"//a:a_test": "PASSED", "//b:b_test": "PASSED"})
	req.Stack, req.Record, req.Marks, req.Self = true, true, "/marks", "/tools/bazel-driver"
	collect(t, fb, req)

	args := bazel.Args(fb.calls[0])
	for _, want := range []string{"--nocache_test_results", "--local_test_jobs=1", "--run_under=/tools/bazel-driver boundary --",
		"--sandbox_writable_path=/marks", "--test_env=PIPELINEGEN_MARKS", "--test_env=PIPELINEGEN_MARK_SLICES"} {
		if !slices.Contains(args, want) {
			t.Errorf("recording a stack: no %s in %v", want, args)
		}
	}

	if !slices.Contains(fb.calls[0].Env, "PIPELINEGEN_MARKS=/marks") {
		t.Errorf("env %v", fb.calls[0].Env)
	}

	fb, req = setup(t, true, map[string]string{"//a:a_test": "PASSED"})
	req.Stack, req.Record, req.Marks, req.Self = true, true, "/marks", "/tools/bazel-driver"
	req.Settings.Flags = []string{"--remote_executor=grpc://ex"}

	got := collect(t, fb, req)
	if len(fb.calls) != 0 || got["a:a_test"].Status != "UNMARKED" || got["a:a_test"].Passed {
		t.Errorf("remote: %d calls, %+v", len(fb.calls), got)
	}
}
