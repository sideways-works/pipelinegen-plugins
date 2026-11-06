package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

func fixture(t *testing.T) string {
	t.Helper()

	tree := t.TempDir()

	err := filepath.WalkDir("testdata/web", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		rel, _ := filepath.Rel("testdata/web", p) //nolint:errcheck // p is under it

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

func cache(t *testing.T) string {
	t.Helper()

	if dir := os.Getenv("PLAYWRIGHT_DRIVER_TEST_CACHE"); dir != "" {
		return dir
	}

	dir, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}

	return filepath.Join(dir, "playwright-driver-test")
}

var specs = []string{"e2e/a.spec.mjs", "e2e/b.spec.mjs", "e2e/c.spec.mjs"}

func request(t *testing.T, tree string, batch, record bool, marks *plugin.Marks) plugin.DriverRequest {
	t.Helper()

	req := plugin.DriverRequest{
		Protocol: plugin.Protocol, Work: t.TempDir(), Cache: cache(t),
		Tree: tree, Batch: batch, Record: record, Marks: marks,
		Env: map[string]string{},
	}

	for _, s := range specs {
		sl := plugin.DriverSlice{Name: s, Sources: []string{s}}
		if record {
			sl.Recording = filepath.Join(tree, ".coverage", s)
		}

		req.Slices = append(req.Slices, sl)
	}

	return req
}

func runAll(t *testing.T, req plugin.DriverRequest) (map[string]plugin.DriverEvent, string) {
	t.Helper()

	calls := [][]plugin.DriverSlice{req.Slices}
	if !req.Batch {
		calls = nil
		for _, sl := range req.Slices {
			calls = append(calls, []plugin.DriverSlice{sl})
		}
	}

	got := map[string]plugin.DriverEvent{}

	var log bytes.Buffer

	for _, c := range calls {
		r := req
		r.Slices = c

		if err := run(t.Context(), r, func(ev plugin.DriverEvent) error {
			got[ev.Slice] = ev

			return nil
		}, &log); err != nil {
			t.Fatalf("%v\n%s", err, log.String())
		}
	}

	return got, log.String()
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

	return fmt.Sprintf("passed %v, tests %d, failures %v", ev.Passed, tests, failed)
}

func TestBundledAndNotTheResultsAreTheSame(t *testing.T) {
	tree := fixture(t)

	one, _ := runAll(t, request(t, tree, false, false, nil))
	all, _ := runAll(t, request(t, tree, true, false, nil))

	for _, s := range specs {
		if judged(one[s]) != judged(all[s]) {
			t.Errorf("%s: one by one %s, bundled %s", s, judged(one[s]), judged(all[s]))
		}
	}

	if got := judged(all["e2e/a.spec.mjs"]); got != "passed true, tests 2, failures []" {
		t.Fatalf("a: %s", got)
	}

	if got := judged(all["e2e/b.spec.mjs"]); got != "passed false, tests 1, failures [multiplies wrong on purpose]" {
		t.Fatalf("b: %s", got)
	}

	if tail := all["e2e/b.spec.mjs"].Failures[0].Tail; !strings.Contains(tail, "toBe") {
		t.Fatalf("b's tail: %q", tail)
	}
}

func TestEverySpecThatRanLeftItsRecording(t *testing.T) {
	tree := fixture(t)

	got, _ := runAll(t, request(t, tree, true, true, nil))

	for _, s := range specs {
		if !got[s].Recorded {
			t.Errorf("%s left no recording", s)
		}

		if _, err := os.Stat(filepath.Join(tree, ".coverage", s, "fake", "ran.txt")); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
}

func answerMarks(ctx context.Context, dir string) (*[]string, *sync.Mutex) {
	var (
		mu   sync.Mutex
		took []string
		seen = map[string]bool{}
	)

	go func() {
		for ctx.Err() == nil {
			ents, _ := os.ReadDir(dir) //nolint:errcheck // read again on the next round

			names := make([]string, 0, len(ents))
			for _, e := range ents {
				names = append(names, e.Name())
			}

			slices.Sort(names)

			for _, n := range names {
				id, ok := strings.CutSuffix(n, ".json")
				if !ok || strings.HasPrefix(n, ".") || seen[id] {
					continue
				}

				seen[id] = true

				var m struct {
					Mark  string `json:"mark"`
					Slice string `json:"slice"`
				}

				data, _ := os.ReadFile(filepath.Join(dir, n)) //nolint:errcheck // the test's own marks
				_ = json.Unmarshal(data, &m)                  //nolint:errcheck // a broken one shows as a wrong order

				mu.Lock()

				took = append(took, m.Mark+" "+m.Slice)
				mu.Unlock()

				tmp := filepath.Join(dir, "."+id+".answer")
				if os.WriteFile(tmp, nil, 0o600) == nil {
					_ = os.Rename(tmp, filepath.Join(dir, id+".done")) //nolint:errcheck // the mark times out and says so
				}
			}

			time.Sleep(10 * time.Millisecond)
		}
	}()

	return &took, &mu
}

func TestARecordedStackBundleRunsOneWorkerAndMarksEachSpec(t *testing.T) {
	tree := fixture(t)
	dir := t.TempDir()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	took, mu := answerMarks(ctx, dir)

	req := request(t, tree, true, true, &plugin.Marks{Dir: dir})
	req.Config = map[string]any{"workers": 4}

	got, log := runAll(t, req)

	if !strings.Contains(log, "run with one worker") {
		t.Fatalf("the log doesn't say the bundle ran with one worker:\n%s", log)
	}

	mu.Lock()
	marks := slices.Clone(*took)
	mu.Unlock()

	var want []string
	for _, s := range specs {
		want = append(want, "start "+s, "stop "+s)
	}

	if !slices.Equal(marks, want) {
		t.Fatalf("marks %q, want %q", marks, want)
	}

	if judged(got["e2e/b.spec.mjs"]) != "passed false, tests 1, failures [multiplies wrong on purpose]" {
		t.Fatalf("b: %s", judged(got["e2e/b.spec.mjs"]))
	}
}

func TestTheFixtureShipsTheDriversMarks(t *testing.T) {
	ours, err := os.ReadFile("../marks.mjs")
	if err != nil {
		t.Fatal(err)
	}

	theirs, err := os.ReadFile("testdata/web/e2e/marks.mjs")
	if err != nil || !bytes.Equal(ours, theirs) {
		t.Fatalf("testdata/web/e2e/marks.mjs is not marks.mjs (%v)", err)
	}
}

func TestDescribe(t *testing.T) {
	d, err := describe()
	if err != nil || d.Kind != plugin.KindDriver || !d.HasVerb(plugin.CallRun) || !d.Apart {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestAMessageNamesTheTreesRoot(t *testing.T) {
	for dir, want := range map[string]string{"": "the tree's root", ".": "the tree's root", "apps/web": "apps/web"} {
		if got := (&job{set: Settings{Dir: dir}}).where(); got != want {
			t.Errorf("dir %q: %q, want %q", dir, got, want)
		}
	}
}

func TestAStoppedRunPassesNoFileOnASkip(t *testing.T) {
	tree := fixture(t)
	req := request(t, tree, true, false, nil)
	req.Slices = []plugin.DriverSlice{{Name: "e2e/stopped.spec.mjs", Sources: []string{"e2e/stopped.spec.mjs"}}}
	req.Config = map[string]any{"flags": []string{"--global-timeout=3000"}}

	got, log := runAll(t, req)

	if ev, ok := got["e2e/stopped.spec.mjs"]; ok {
		t.Errorf("a file the run stopped in ended: %s\n%s", judged(ev), log)
	}

	if !strings.Contains(log, "the run says: Timed out waiting") {
		t.Errorf("the log doesn't say why the run stopped:\n%s", log)
	}
}

func TestSkipsCountOnlyInARunThatEnded(t *testing.T) {
	ft := &fileTests{
		titles: []string{"ran", "skipped"},
		tests: []test{
			{Status: "expected", Results: []result{{Status: "passed"}}},
			{Status: "skipped"},
		},
	}

	if ev, ok := end("e2e/x.spec.mjs", ft, false); !ok || !ev.Passed || *ev.Tests != 2 {
		t.Errorf("a run that ended: %v %s", ok, judged(ev))
	}

	if ev, ok := end("e2e/x.spec.mjs", ft, true); ok {
		t.Errorf("a stopped run: %s", judged(ev))
	}

	failed := &fileTests{
		titles: []string{"failed", "skipped"},
		tests: []test{
			{Status: "unexpected", Results: []result{{Status: "failed", Errors: []testError{{Message: "boom"}}}}},
			{Status: "skipped"},
		},
	}

	if ev, ok := end("e2e/x.spec.mjs", failed, true); !ok || ev.Passed || len(ev.Failures) != 1 {
		t.Errorf("a stopped run keeps the failures it saw: %v %s", ok, judged(ev))
	}
}
