package concourse

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func placedSteps(p M) map[string][]string {
	out := map[string][]string{}

	for _, kind := range []string{"resource_types", "resources"} {
		for _, r := range asSlice(p[kind]) {
			out[strings.TrimSuffix(kind, "s")+" "+fmt.Sprint(asMap(r)["name"])] = tagsOf(asMap(r))
		}
	}

	for _, j := range asSlice(p["jobs"]) {
		job := asMap(j)
		walkSteps(job, func(step M) {
			for _, verb := range []string{"task", "get", "put"} {
				if name, ok := step[verb]; ok {
					out[fmt.Sprintf("%s %s %v", job["name"], verb, name)] = tagsOf(step)
				}
			}
		})
	}

	return out
}

func walkSteps(v any, f func(M)) {
	switch x := v.(type) {
	case M:
		f(x)

		for _, val := range x {
			walkSteps(val, f)
		}
	case []any:
		for _, e := range x {
			walkSteps(e, f)
		}
	}
}

func generated(t *testing.T, spec plugin.Spec, root string, config map[string]any) M {
	t.Helper()

	p, err := GenerateWith(spec, root, config)
	if err != nil {
		t.Fatal(err)
	}

	return p
}

func placementOf(def any, rules ...any) map[string]any {
	pl := map[string]any{"rules": rules}
	if def != nil {
		pl["default"] = def
	}

	return map[string]any{"placement": pl}
}

func on(what string, tags []any, more ...string) map[string]any {
	r := map[string]any{"for": what, "tags": tags}
	for i := 0; i+1 < len(more); i += 2 {
		r[more[i]] = more[i+1]
	}

	return r
}

func TestPlacementPlacesEachKindOfStep(t *testing.T) {
	spec, root := fixture(t)

	p := generated(t, spec, root, placementOf([]any{"small"},
		on(forPlanning, []any{}, "run", "nightly"),
		on(forSuite, []any{"nightly"}, "run", "nightly"),
		on(forSuite, []any{"browsers"}, "suite", "e2e"),
		on(forBookkeeping, []any{"tiny"}, "run", "pr"),
	))

	steps := placedSteps(p)

	for k, v := range steps {
		if strings.HasPrefix(k, "resource_type ") && !slices.Equal(v, []string{"small"}) {
			t.Errorf("%s: tags %v, want the default", k, v)
		}
	}

	for step, want := range map[string][]string{
		"pipeline task stale":              {"small"},
		"resource planner":                 {"small"},
		"plan-main task plan":              {"small"},
		"plan-nightly task plan":           nil,
		"sweep-pr task sweep":              {"small"},
		"nightly-e2e task start":           {"nightly"},
		"nightly-e2e task batch":           {"nightly"},
		"nightly-e2e task end":             {"nightly"},
		"main-e2e task batch":              {"browsers"},
		"main-e2e task end":                {"browsers"},
		"main-backend task batch":          {"small"},
		"pr-e2e task batch":                {"browsers"},
		"pr-e2e task batch-end":            {"tiny"},
		"pr-e2e task end":                  {"tiny"},
		"pr-e2e get " + triggerBookkeeping: {"tiny"},
	} {
		got, ok := steps[step]
		if !ok {
			t.Errorf("no step %q", step)

			continue
		}

		if !slices.Equal(got, want) {
			t.Errorf("%s: tags %v, want %v", step, got, want)
		}
	}
}

func TestPlacementLeftToConcourse(t *testing.T) {
	spec, root := fixture(t)

	for _, config := range []map[string]any{
		placementOf(nil), placementOf([]any{}), placementOf(nil, on(forSuite, []any{}, "suite", "e2e")),
	} {
		for step, tags := range placedSteps(generated(t, spec, root, config)) {
			if len(tags) > 0 {
				t.Errorf("%v: %s has tags %v", config, step, tags)
			}
		}
	}
}

func TestPlacementRefused(t *testing.T) {
	for want, config := range map[string]map[string]any{
		"rule 1 {for: tests}: for is one of":     placementOf(nil, on("tests", []any{"x"})),
		"rule 1 {for: checks}: tags is missing":  placementOf(nil, map[string]any{"for": "checks"}),
		"rule 1 {for: checks}: an empty tag":     placementOf(nil, on(forChecks, []any{""})),
		"a rule for checks names no run":         placementOf(nil, on(forChecks, []any{"x"}, "run", "pr")),
		"a rule for planning names no suite":     placementOf(nil, on(forPlanning, []any{"x"}, "suite", "e2e")),
		"rule 1 {for: bookkeeping, run: weekly}": placementOf(nil, on(forBookkeeping, []any{"x"}, "run", "weekly")),
		"no suite lint":                          placementOf(nil, on(forBookkeeping, []any{"x"}, "suite", "lint")),
		"default has an empty tag":               placementOf([]any{""}),
		"rule 2 {for: bookkeeping, run: pr, suite: backend} places nothing: rule 1 {for: bookkeeping, run: pr} comes first": placementOf(nil,
			on(forBookkeeping, []any{"x"}, "run", "pr"), on(forBookkeeping, []any{"y"}, "run", "pr", "suite", "backend")),
		"rule 1 {for: bookkeeping, suite: specs} places nothing: the pipeline has no such step": placementOf(nil,
			on(forBookkeeping, []any{"x"}, "suite", "specs")),
		`unknown field "placment"`: {"placment": map[string]any{}},
		`unknown field "tag"`:      placementOf(nil, map[string]any{"for": "checks", "tag": []any{"x"}}),
	} {
		spec, root := fixture(t)

		_, err := GenerateWith(spec, root, config)
		if err == nil || !errors.Is(err, ErrPlacement) || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: got %v, want %q", config, err, want)
		}
	}
}

func TestPlacementRefusesTheOlderKeys(t *testing.T) {
	for want, change := range map[string]func(*plugin.Spec){
		"concourse.planner.tags":  func(s *plugin.Spec) { asMap(s.CI["planner"])["tags"] = []any{"planner"} },
		"concourse.resource_tags": func(s *plugin.Spec) { s.CI["resource_tags"] = []any{"cloud-1"} },
		"concourse.bookkeeping":   func(s *plugin.Spec) { s.CI["bookkeeping"] = map[string]any{"tags": []any{}} },
	} {
		spec, root := fixture(t)
		change(&spec)

		if _, err := GenerateWith(spec, root, fixtureConfig()); !errors.Is(err, ErrPlacement) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", want, err)
		}
	}

	// A suite's tags are no field of the spec: they show only in the raw
	// request.
	spec, root := fixture(t)

	req, err := json.Marshal(plugin.PipelineRequest{Protocol: plugin.Protocol, Work: t.TempDir(), Spec: spec, Root: root})
	if err != nil {
		t.Fatal(err)
	}

	old := strings.Replace(string(req), `"name":"e2e",`, `"name":"e2e","tags":["browsers"],`, 1)
	if old == string(req) {
		t.Fatal("no suite e2e in the request")
	}

	if _, err := pipeline([]byte(old)); !errors.Is(err, ErrPlacement) || !strings.Contains(err.Error(), "suites.e2e.run_on.tags") {
		t.Errorf("a suite's tags from an older pipelinegen: got %v", err)
	}
}

func TestDescribeSaysItPlaces(t *testing.T) {
	if !slices.Contains(describe().Capabilities, plugin.CapPlacement) {
		t.Error("describe doesn't say placement")
	}
}

func TestPlacementTakesAnOlderToolsEmptyLists(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "own", "request.json"))
	if err != nil {
		t.Fatal(err)
	}

	var req map[string]any
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatal(err)
	}

	spec, _ := req["spec"].(map[string]any)      //nolint:errcheck // the fixture's shape
	ci, _ := spec["ci"].(map[string]any)         //nolint:errcheck // the fixture's shape
	planner, _ := ci["planner"].(map[string]any) //nolint:errcheck // the fixture's shape
	planner["tags"], ci["resource_tags"] = []any{}, []any{}
	req["work"], req["protocol"] = t.TempDir(), plugin.Protocol
	req["root"] = filepath.Join("testdata", "own")

	older, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := pipeline(older); err != nil {
		t.Fatalf("an older pipelinegen's request with empty lists: %v", err)
	}

	ci["resource_tags"] = []any{"cloud-1"}

	if older, err = json.Marshal(req); err != nil {
		t.Fatal(err)
	}

	_, err = pipeline(older)
	if !errors.Is(err, ErrPlacement) || !strings.Contains(err.Error(), ": concourse.resource_tags place steps") {
		t.Errorf("an older pipelinegen's request with resource tags: %v", err)
	}
}

func TestPlacementSaysASuiteJobItLeavesUntagged(t *testing.T) {
	spec, root := fixture(t)

	say := func(job string) string {
		return "placement: " + job + " is placed by no rule and there is no default: its steps run on untagged workers"
	}

	for _, c := range []struct {
		what   string
		config map[string]any
		want   []string
	}{
		{"rules, no default", placementOf(nil, on(forPlanning, []any{"planner"}), on(forSuite, []any{"browsers"}, "suite", "e2e")),
			[]string{say("main-backend"), say("nightly-backend"), say("pr-backend")}},
		{"a rule chooses untagged", placementOf(nil, on(forSuite, []any{"browsers"}, "suite", "e2e"), on(forSuite, []any{}, "suite", "backend")), nil},
		{"one run's suite", placementOf(nil, on(forSuite, []any{"big"}, "run", "nightly"), on(forSuite, []any{}, "run", "main")),
			[]string{say("pr-backend"), say("pr-e2e")}},
		{"bookkeeping places no suite", placementOf(nil, on(forBookkeeping, []any{"tiny"})),
			[]string{say("main-backend"), say("main-e2e"), say("nightly-backend"), say("nightly-e2e"), say("pr-backend"), say("pr-e2e")}},
		{"a default", placementOf([]any{"small"}, on(forSuite, []any{"browsers"}, "suite", "e2e")), nil},
		{"an empty default", placementOf([]any{}, on(forSuite, []any{"browsers"}, "suite", "e2e")), nil},
		{"no rules", placementOf(nil), nil},
		{"no placement", nil, nil},
	} {
		if got := Notices(spec, c.config); !slices.Equal(got, c.want) {
			t.Errorf("%s: notices %q, want %q", c.what, got, c.want)
		}
	}

	config := placementOf(nil, on(forSuite, []any{"browsers"}, "suite", "e2e"))
	work := t.TempDir()

	req, err := json.Marshal(plugin.PipelineRequest{Protocol: plugin.Protocol, Work: work, Config: config, Spec: spec, Root: root})
	if err != nil {
		t.Fatal(err)
	}

	var out, errs bytes.Buffer
	if code := Serve([]string{plugin.CallPipeline}, bytes.NewReader(req), &out, &errs); code != 0 {
		t.Fatalf("pipeline: %d %s", code, errs.String())
	}

	var ans plugin.PipelineAnswer
	if err := json.Unmarshal(out.Bytes(), &ans); err != nil || len(ans.Notices) != 3 || ans.Notices[0] != say("main-backend") {
		t.Fatalf("answer %s: %v", out.String(), err)
	}

	text, err := os.ReadFile(filepath.Join(work, "pipeline.yml"))
	if err != nil {
		t.Fatal(err)
	}

	want, err := Text(generated(t, spec, root, config))
	if err != nil || !bytes.Equal(text, want) {
		t.Errorf("the pipeline with notices differs from the one generated: %v", err)
	}
}
