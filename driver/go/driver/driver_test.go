package driver

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func fixture(t *testing.T) string {
	t.Helper()

	tree := t.TempDir()

	err := filepath.WalkDir("testdata/mod", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		rel, _ := filepath.Rel("testdata/mod", p) //nolint:errcheck // p is under it

		data, err := os.ReadFile(p) //nolint:gosec // the test's fixture
		if err != nil {
			return fmt.Errorf("fixture: %w", err)
		}

		if err := os.MkdirAll(filepath.Join(tree, filepath.Dir(rel)), 0o750); err != nil {
			return fmt.Errorf("fixture: %w", err)
		}

		return os.WriteFile(filepath.Join(tree, rel), data, 0o600) //nolint:gosec // the test's own copy
	})
	if err != nil {
		t.Fatal(err)
	}

	return tree
}

var fixtureSlices = []plugin.DriverSlice{
	{Name: "calc/add_test.go", Sources: []string{"calc/add_test.go"}, Tests: []string{"TestAdd", "TestAddSub"}},
	{Name: "calc/mul_test.go", Sources: []string{"calc/mul_test.go"}, Tests: []string{"TestMul"}},
	{Name: "calc/neg_test.go", Sources: []string{"calc/neg_test.go"}, Tests: []string{"TestNeg"}},
	{Name: "text/upper_test.go", Sources: []string{"text/upper_test.go"}, Tests: []string{"TestUpper"}},
	{Name: "kid/kid_test.go", Sources: []string{"kid/kid_test.go"}, Tests: []string{"TestChild"}},
	{Name: "kid/other_test.go", Sources: []string{"kid/other_test.go"}, Tests: []string{"TestOther"}},
	{Name: "envs/a_test.go", Sources: []string{"envs/a_test.go"}, Tests: []string{"TestA"}},
	{Name: "envs/b_test.go", Sources: []string{"envs/b_test.go"}, Tests: []string{"TestB"}},
}

func runFixture(t *testing.T, tree string, batch, record bool, marks *plugin.Marks) map[string]plugin.DriverEvent {
	t.Helper()

	req := plugin.DriverRequest{
		Protocol: plugin.Protocol, Work: t.TempDir(), Cache: t.TempDir(),
		Tree: tree, Batch: batch, Record: record, Marks: marks,
	}

	// The envs fixture notes what its tests see.
	req.Env = map[string]string{"FIXTURE_ENV_LOG": filepath.Join(tree, "env.log")}

	for _, sl := range fixtureSlices {
		if record {
			sl.Recording = filepath.Join(tree, "..", filepath.Base(tree)+"-rec", sl.Name)
		}

		req.Slices = append(req.Slices, sl)
	}

	got := map[string]plugin.DriverEvent{}

	var log bytes.Buffer

	err := run(t.Context(), req, func(ev plugin.DriverEvent) error {
		got[ev.Slice] = ev

		return nil
	}, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}

	return got
}

func judged(ev plugin.DriverEvent) string {
	var failed []string
	for _, f := range ev.Failures {
		failed = append(failed, f.Test)
	}

	tests := -1
	if ev.Tests != nil {
		tests = *ev.Tests
	}

	return strings.Join([]string{boolWord(ev.Passed), strings.Join(failed, ","), string(rune('0' + tests))}, " ")
}

func boolWord(b bool) string {
	if b {
		return "passed"
	}

	return "failed"
}

func TestBundledAndNotTheResultsAreTheSame(t *testing.T) {
	tree := fixture(t)

	one := runFixture(t, tree, false, false, nil)
	all := runFixture(t, tree, true, false, nil)

	for _, sl := range fixtureSlices {
		if judged(one[sl.Name]) != judged(all[sl.Name]) {
			t.Errorf("%s: one by one %q, bundled %q", sl.Name, judged(one[sl.Name]), judged(all[sl.Name]))
		}
	}

	if got := judged(all["calc/mul_test.go"]); got != "failed TestMul 1" {
		t.Fatalf("mul: %q", got)
	}

	if got := judged(all["calc/add_test.go"]); got != "passed  2" {
		t.Fatalf("add: %q", got)
	}

	if f := all["calc/mul_test.go"].Failures[0].Tail; !strings.Contains(f, "fails on purpose") {
		t.Fatalf("mul's tail: %q", f)
	}
}

func profile(t *testing.T, dir string) []string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(dir, "tests.coverprofile"))
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	sort.Strings(lines)

	return lines
}

func TestBundledAndNotTheRecordingsAreTheSame(t *testing.T) {
	treeOne, treeAll := fixture(t), fixture(t)

	one := runFixture(t, treeOne, false, true, nil)
	all := runFixture(t, treeAll, true, true, nil)

	for _, sl := range fixtureSlices {
		if judged(one[sl.Name]) != judged(all[sl.Name]) || !all[sl.Name].Recorded {
			t.Errorf("%s: one by one %q, bundled %q (recorded %v)", sl.Name, judged(one[sl.Name]), judged(all[sl.Name]), all[sl.Name].Recorded)
		}

		a := profile(t, filepath.Join(treeOne+"-rec", sl.Name))
		b := profile(t, filepath.Join(treeAll+"-rec", sl.Name))

		if !slices.Equal(a, b) {
			t.Errorf("%s: the recordings differ:\none by one %v\nbundled    %v", sl.Name, a, b)
		}
	}

	kid := profile(t, filepath.Join(treeAll+"-rec", "kid/kid_test.go"))
	if c := count(kid, "kid/kid.go:7."); c == "0" || c == "none" {
		t.Fatalf("the child's coverage is lost:\n%s", strings.Join(kid, "\n"))
	}

	// add's recording ran Add and not Mul, whose block is cut, or zero.
	add := profile(t, filepath.Join(treeAll+"-rec", "calc/add_test.go"))
	if c := count(add, "calc/calc.go:8."); count(add, "calc/calc.go:5.") == "0" || (c != "0" && c != "none") {
		t.Fatalf("add's recording:\n%s", strings.Join(add, "\n"))
	}
}

func count(lines []string, at string) string {
	for _, l := range lines {
		if strings.Contains(l, at) {
			f := strings.Fields(l)

			return f[len(f)-1]
		}
	}

	return "none"
}

func TestARecordedStackBundleMarksEveryFile(t *testing.T) {
	tree := fixture(t)
	log := filepath.Join(t.TempDir(), "marks")

	script := filepath.Join(t.TempDir(), "mark")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$1 $2\" >> "+log+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	runFixture(t, tree, true, true, &plugin.Marks{Command: []string{script}, Dir: t.TempDir()})

	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}

	got := strings.Split(strings.TrimSpace(string(data)), "\n")

	if len(got) != 2*len(fixtureSlices) {
		t.Fatalf("marks: %q", got)
	}

	for i := 0; i < len(got); i += 2 {
		start, stop := strings.Fields(got[i]), strings.Fields(got[i+1])
		if start[0] != "start" || stop[0] != "stop" || start[1] != stop[1] {
			t.Fatalf("marks out of order: %q", got)
		}
	}
}

func TestDescribe(t *testing.T) {
	d, err := describe()
	if err != nil || d.Kind != plugin.KindDriver || !d.HasVerb(plugin.CallRun) || d.Apart != Apart {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestABundleSeesTheEnvironmentGoTestGives(t *testing.T) {
	seen := map[bool]string{}

	for _, batch := range []bool{false, true} {
		tree := fixture(t)
		runFixture(t, tree, batch, true, nil)

		data, err := os.ReadFile(filepath.Join(tree, "env.log"))
		if err != nil {
			t.Fatal(err)
		}

		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		sort.Strings(lines)
		seen[batch] = strings.Join(lines, "\n")
	}

	if seen[false] != seen[true] {
		t.Fatalf("the tests saw other variables:\none by one %s\nbundled    %s", seen[false], seen[true])
	}
}
