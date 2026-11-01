package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func outsideRun(t *testing.T, tree, cache, says string, batch bool, conf map[string]any) map[string]plugin.DriverEvent {
	t.Helper()

	if err := os.WriteFile(filepath.Join(filepath.Dir(tree), "outside.txt"), []byte(says), 0o600); err != nil {
		t.Fatal(err)
	}

	req := plugin.DriverRequest{
		Protocol: plugin.Protocol, Work: t.TempDir(), Cache: cache, Config: conf, Tree: tree, Batch: batch,
		Slices: []plugin.DriverSlice{
			{Name: "outside/outside_test.go", Sources: []string{"outside/outside_test.go"}, Tests: []string{"TestOutside"}},
			{Name: "calc/add_test.go", Sources: []string{"calc/add_test.go"}, Tests: []string{"TestAdd", "TestAddSub"}},
		},
	}

	got := map[string]plugin.DriverEvent{}

	if err := run(t.Context(), req, func(ev plugin.DriverEvent) error {
		got[ev.Slice] = ev

		return nil
	}, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}

	return got
}

func TestAnUncachedFileRunsEveryTime(t *testing.T) {
	for _, batch := range []bool{false, true} {
		// outside.txt sits beside the tree, outside its module.
		tree := filepath.Join(t.TempDir(), "tree")
		if err := os.CopyFS(tree, os.DirFS(fixture(t))); err != nil {
			t.Fatal(err)
		}

		cache := t.TempDir()

		outsideRun(t, tree, cache, "pass\n", batch, nil)

		got := outsideRun(t, tree, cache, "fail\n", batch, nil)
		if ev := got["outside/outside_test.go"]; !ev.Passed || !ev.Replayed {
			t.Fatalf("batch %v: without uncached, the changed state was served a cached pass, which this test relies on: %+v", batch, ev)
		}

		conf := map[string]any{"uncached": []any{"outside/*_test.go"}}

		outsideRun(t, tree, cache, "pass\n", batch, conf)

		got = outsideRun(t, tree, cache, "fail\n", batch, conf)
		if ev := got["outside/outside_test.go"]; ev.Passed || ev.Replayed {
			t.Errorf("batch %v: uncached, the test didn't run again: %+v", batch, ev)
		}

		if ev := got["calc/add_test.go"]; !ev.Passed || !ev.Replayed {
			t.Errorf("batch %v: a file uncached doesn't name lost its cached result: %+v", batch, ev)
		}
	}
}

func TestUncachedNamesTestFiles(t *testing.T) {
	for _, p := range []string{"outside", "outside/*.go", "/abs/a_test.go", "../a_test.go", "a/[_test.go", "", "./a_test.go"} {
		if _, err := settingsFor(map[string]any{"uncached": []any{p}}); err == nil || !strings.Contains(err.Error(), "uncached[0]") {
			t.Errorf("%q: %v", p, err)
		}
	}

	for p, files := range map[string][]string{
		"**/git_test.go":   {"git_test.go", "a/git_test.go", "a/b/git_test.go"},
		"api/*_test.go":    {"api/x_test.go"},
		"api/**/*_test.go": {"api/x_test.go", "api/v1/x_test.go"},
		"outside_test.go":  {"outside_test.go"},
	} {
		if _, err := settingsFor(map[string]any{"uncached": []any{p}}); err != nil {
			t.Errorf("%q: %v", p, err)
		}

		for _, f := range files {
			if !matchFile(p, f) {
				t.Errorf("%q doesn't match %s", p, f)
			}
		}
	}

	if matchFile("api/*_test.go", "api/v1/x_test.go") || matchFile("**/git_test.go", "a/other_test.go") {
		t.Error("a pattern matched a file it doesn't name")
	}
}
