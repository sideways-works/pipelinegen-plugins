package concourse

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func fixture(t *testing.T) (plugin.Spec, string) {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", "spec.json"))
	if err != nil {
		t.Fatal(err)
	}

	var s plugin.Spec
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}

	return s, t.TempDir()
}

func fixtureConfig() map[string]any {
	return map[string]any{"placement": map[string]any{"rules": []any{
		map[string]any{"for": "pipeline", "tags": []any{"planner"}},
		map[string]any{"for": "planning", "tags": []any{"planner"}},
		map[string]any{"for": "suite", "suite": "e2e", "tags": []any{"browsers"}},
	}}}
}

func placed(t *testing.T, spec plugin.Spec, root string, first ...any) M {
	t.Helper()

	rules := []any{
		map[string]any{"for": "pipeline", "tags": []any{"planner"}},
		map[string]any{"for": "planning", "tags": []any{"planner"}},
		map[string]any{"for": "suite", "suite": "e2e", "tags": []any{"browsers"}},
	}
	conf := map[string]any{"placement": map[string]any{"rules": append(first, rules...)}}

	p, err := GenerateWith(spec, root, conf)
	if err != nil {
		t.Fatal(err)
	}

	return p
}

func suiteIndex(t *testing.T, s plugin.Spec, name string) int {
	t.Helper()

	for i, su := range s.Suites {
		if su.Name == name {
			return i
		}
	}

	t.Fatalf("no suite %s", name)

	return -1
}

func runIndex(t *testing.T, s plugin.Spec, name string) int {
	t.Helper()

	for i, r := range s.Runs {
		if r.Name == name {
			return i
		}
	}

	t.Fatalf("no run %s", name)

	return -1
}

func text(t *testing.T, p M) string {
	t.Helper()

	out, err := Text(p)
	if err != nil {
		t.Fatal(err)
	}

	return string(out)
}

func jobNames(t *testing.T, p M) []string {
	t.Helper()

	jobs, ok := p["jobs"].([]any)
	if !ok {
		t.Fatal("no jobs")
	}

	var out []string

	for _, j := range jobs {
		job, ok := j.(M)
		if !ok {
			t.Fatal("a job is no mapping")
		}

		name, ok := job["name"].(string)
		if !ok {
			t.Fatal("a job without a name")
		}

		out = append(out, name)
	}

	return out
}

func TestShape(t *testing.T) {
	cfg, root := fixture(t)

	p, err := Generate(cfg, root)
	if err != nil {
		t.Fatal(err)
	}

	want := "pipeline,plan-main,main-backend,main-e2e,plan-nightly,nightly-backend,nightly-e2e,plan-pr,sweep-pr,pr-backend,pr-e2e"
	if got := strings.Join(jobNames(t, p), ","); got != want {
		t.Fatalf("jobs %s", got)
	}

	s := text(t, p)

	for _, want := range []string{
		"name: trigger-pr\n", "type: github-pr", "repository: cfcommunity/github-pr-resource",
		"name: trigger-nightly\n", "type: time",
		"name: branch-main\n",
		"version: every",
		"- plan-pr\n",
		"task: stale",
		"values: ((.:gate))",
		"values: ((.:batches))",
		"PIPELINEGEN_BATCH: ((.:batch))",
		"file: source/ci/e2e-slice.yml",
		"file: source/ci/e2e-prepare.yml",
		"PROJECTS: all",
		"PIPELINEGEN_RECORD: \"true\"",
		"jobs/pr-e2e",
		"task: start",
		"task: end",
		"path: tools/pipelinegen",
		"digest: sha256:",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("pipeline lacks %q", want)
		}
	}
}

func TestSerialGroupAndInclude(t *testing.T) {
	cfg, _ := fixture(t)

	e2e := &cfg.Suites[suiteIndex(t, cfg, "e2e")]
	e2e.Lock = &plugin.SpecLock{Name: "browsers", Minutes: 90}
	e2e.SerialGroup = "e2e"
	e2e.MaxInFlight = 1

	pr := &cfg.Runs[runIndex(t, cfg, "pr")]
	for i := range pr.Suites {
		if pr.Suites[i].Suite == "e2e" {
			pr.Suites[i].Minutes = 30
		}
	}

	for i := range cfg.Runs {
		for j := range cfg.Runs[i].Suites {
			if cfg.Runs[i].Suites[j].Suite == "e2e" && cfg.Runs[i].Suites[j].Minutes == 0 {
				cfg.Runs[i].Suites[j].Minutes = 90
			}
		}
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "root.yml"), []byte("jobs:\n- name: title\n  plan: []\nresources:\n- name: old\n  type: git\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg.CI["include"] = "root.yml"

	p, err := Generate(cfg, dir)
	if err != nil {
		t.Fatal(err)
	}

	s := text(t, p)

	for _, want := range []string{"timeout: 30m", "timeout: 90m", "serial_groups:\n        - e2e", "max_in_flight: 1", "name: title", "name: old"} {
		if !strings.Contains(s, want) {
			t.Errorf("pipeline lacks %q", want)
		}
	}

	for _, unwanted := range []string{"PIPELINEGEN_LOCK", "lock acquire", "lock release"} {
		if strings.Contains(s, unwanted) {
			t.Errorf("an older tool's lock still reaches the pipeline: %q", unwanted)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "root.yml"), []byte("jobs:\n- name: pipeline\n  plan: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Generate(cfg, dir); err == nil {
		t.Fatal("an include took a generated job's name")
	}

	if err := os.WriteFile(filepath.Join(dir, "root.yml"), []byte("display: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Generate(cfg, dir); err == nil {
		t.Fatal("an include with an unknown key")
	}
}

func TestFlyValidates(t *testing.T) {
	fly, err := exec.LookPath("fly")
	if err != nil {
		t.Skip("no fly")
	}

	cfg, root := fixture(t)

	p, err := Generate(cfg, root)
	if err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(t.TempDir(), "p.yml")
	if err := os.WriteFile(file, []byte(text(t, p)), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := exec.CommandContext(t.Context(), fly, "validate-pipeline", "--strict", "-c", file, "-v", "github_token=x").CombinedOutput()
	if err != nil {
		t.Fatalf("fly: %v\n%s", err, out)
	}
}

func TestBootstrapScriptParses(t *testing.T) {
	file := filepath.Join(t.TempDir(), "bootstrap.sh")
	if err := os.WriteFile(file, []byte(bootstrapScript), 0o600); err != nil {
		t.Fatal(err)
	}

	if out, err := exec.CommandContext(t.Context(), "sh", "-n", file).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v\n%s", err, out)
	}
}

func triggering(t *testing.T, job M) int {
	t.Helper()

	plan, ok := job["plan"].([]any)
	if !ok || len(plan) == 0 {
		t.Fatal("no plan")
	}

	first, ok := plan[0].(M)
	if !ok {
		t.Fatal("no first step")
	}

	gets, ok := first["in_parallel"].([]any)
	if !ok {
		t.Fatal("the first step gets nothing")
	}

	n := 0

	for _, g := range gets {
		if step, ok := g.(M); ok && step["trigger"] == true {
			n++
		}
	}

	return n
}

func TestEveryJobTriggersOnce(t *testing.T) {
	cfg, root := fixture(t)

	manual := cfg.Runs[runIndex(t, cfg, "main")]
	manual.Name = "by-hand"
	manual.Trigger = plugin.SpecTrigger{Kind: "manual", Branch: "main"}
	cfg.Runs = append(cfg.Runs, manual)
	p := placed(t, cfg, root, map[string]any{"for": "checks", "tags": []any{"cloud-1"}})

	jobs, ok := p["jobs"].([]any)
	if !ok {
		t.Fatal("no jobs")
	}

	for _, j := range jobs {
		job, ok := j.(M)
		if !ok {
			t.Fatal("a job is no mapping")
		}

		name, _ := job["name"].(string) //nolint:errcheck // a name the generator wrote

		want := 1
		if name == "plan-by-hand" {
			want = 0 // started by hand
		}

		if strings.HasPrefix(name, "sweep-") {
			want = 2 // its timer and every hand-off
		}

		if got := triggering(t, job); got != want {
			t.Errorf("%s: %d triggering gets, want %d", name, got, want)
		}
	}

	s := text(t, p)
	for _, want := range []string{"on_error:", "on_abort:", "task: report-error", "paths:\n            - pipelinegen.yaml", "name: config\n"} {
		if !strings.Contains(s, want) {
			t.Errorf("pipeline lacks %q", want)
		}
	}

	if strings.Count(s, "- cloud-1\n") < 4 {
		t.Error("resources without their tags")
	}
}

func TestBootstrapPlacesPluginsFromTheCache(t *testing.T) {
	work := t.TempDir()

	for _, p := range []string{
		"cache/pipelinegen-commit-abc/pipelinegen",
		"cache/concourse-runner-release-v0.1.0/concourse-runner",
		"cache/go-sbom-commit-def/go-sbom",
		"cache/bazelisk-v1.27.0",
	} {
		file := filepath.Join(work, p)
		if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(file, []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	// Every cached file is the same script, so one checksum fits all.
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte("#!/bin/sh\n")))

	s := plugin.Spec{Tools: []plugin.SpecTool{
		{Name: "pipelinegen", Kind: plugin.ToolPipelinegen, Repository: "o/pipelinegen", Commit: "abc"},
		{Name: "concourse", Kind: plugin.KindRunner, Repository: "o/concourse-runner", Version: "v0.1.0", SHA256: sum},
		{Name: "go", Kind: plugin.KindSBOM, Repository: "o/go-sbom", Commit: "def"},
	}}

	mustStep(t, work, cleanEnv(t,
		"GITHUB_TOKEN=x",
		"PIPELINEGEN_TOOL_REPOSITORY=o/pipelinegen", "PIPELINEGEN_TOOL_COMMIT=abc",
		"PIPELINEGEN_TOOL_VERSION=", "PIPELINEGEN_TOOL_SHA256=",
		"PIPELINEGEN_BAZELISK_VERSION=v1.27.0", "PIPELINEGEN_BAZELISK_SHA256="+sum,
		"PIPELINEGEN_PLUGINS="+pluginLines(s), "PIPELINEGEN_PRIVATE="+privateModules(s),
	), "tools")

	for _, p := range []string{"tools/pipelinegen", "tools/bazelisk", "tools/plugins/concourse", "tools/plugins/go"} {
		if _, err := os.Stat(filepath.Join(work, p)); err != nil {
			t.Error(err)
		}
	}

	if got := privateModules(s); got != "github.com/o/go-sbom" {
		t.Errorf("private modules %q", got)
	}
}

func TestEveryPipelinegenStepFindsBazel(t *testing.T) {
	spec, root := fixture(t)

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	var walk func(v any)

	steps := 0

	walk = func(v any) {
		switch x := v.(type) {
		case M:
			if cfg, ok := x["config"].(M); ok {
				if runsTheTool(asMap(cfg["run"])) {
					steps++

					if asMap(cfg["params"])["PIPELINEGEN_BAZELISK"] != "tools/bazelisk" {
						t.Errorf("step %v runs pipelinegen without PIPELINEGEN_BAZELISK", x["task"])
					}
				}
			}

			for _, e := range x {
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}

	walk(p)

	if steps < 5 {
		t.Fatalf("only %d pipelinegen steps found", steps)
	}
}

func asMap(v any) M {
	if m, ok := v.(M); ok {
		return m
	}

	return M{}
}

func TestPlanRunsThePrepareFirst(t *testing.T) {
	work := t.TempDir()

	for _, d := range []string{"tools", "trigger"} {
		if err := os.MkdirAll(filepath.Join(work, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}

	fake := "#!/bin/sh\necho \"$@\" > called\n"
	if err := os.WriteFile(filepath.Join(work, "tools", "pipelinegen"), []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}

	mustStep(t, work, append(os.Environ(), "PIPELINEGEN_RUN=pr", `PIPELINEGEN_PREPARE=echo "$GOMODCACHE" > prepared`), "plan")

	prepared, err := os.ReadFile(filepath.Join(work, "trigger", "prepared"))
	if err != nil || strings.TrimSpace(string(prepared)) != filepath.Join(work, "plan-cache", "caches", "gomodcache") {
		t.Fatalf("prepare: %q %v", prepared, err)
	}

	called, err := os.ReadFile(filepath.Join(work, "called"))
	if err != nil || !strings.HasPrefix(string(called), "plan --run pr --trigger trigger") {
		t.Fatalf("plan: %q %v", called, err)
	}
}

func TestNoJobIsSerializedUnlessTheConfigAsks(t *testing.T) {
	spec, root := fixture(t)
	spec.Suites[suiteIndex(t, spec, "e2e")].MaxInFlight = 3

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]any{
		"main-backend": nil, "main-e2e": 3,
		"pr-backend": nil, "pr-e2e": 3,
		"nightly-backend": nil, "nightly-e2e": 3,
		"plan-main": nil, "plan-pr": nil, "plan-nightly": nil,
	}

	for _, j := range asSlice(p["jobs"]) {
		job := asMap(j)
		name := fmt.Sprint(job["name"])

		w, ok := want[name]
		if !ok {
			continue
		}

		if got := job["max_in_flight"]; got != w {
			t.Errorf("%s: max_in_flight %v, want %v", name, got, w)
		}

		delete(want, name)

		if _, ok := job["serial_groups"]; ok {
			t.Errorf("%s: serial_groups from nowhere", name)
		}
	}

	if len(want) > 0 {
		t.Errorf("jobs missing: %v", want)
	}

	for _, j := range asSlice(p["jobs"]) {
		if job := asMap(j); job["serial"] != nil {
			t.Errorf("%v: serial", job["name"])
		}
	}

	// Suite jobs take every planned commit in turn, never the timer, so a
	// nightly on an unchanged branch starts no suite. A branch run's
	// planning job takes every commit too, so none the suites wait for is
	// skipped.
	versions := map[string]any{
		"main-backend": "every", "main-e2e": "every", "plan-main": "every",
		"nightly-backend": nil, "nightly-e2e": nil, "plan-nightly": nil,
		"pr-backend": "every", "pr-e2e": "every", "plan-pr": "every",
	}

	for _, j := range asSlice(p["jobs"]) {
		job := asMap(j)
		name := fmt.Sprint(job["name"])

		w, ok := versions[name]
		if !ok {
			continue
		}

		for _, g := range asSlice(asMap(asSlice(job["plan"])[0])["in_parallel"]) {
			get := asMap(g)
			if get["get"] != "trigger" {
				continue
			}

			if get["version"] != w {
				t.Errorf("%s: trigger version %v, want %v", name, get["version"], w)
			}

			// The suite build's tree reads this very version's plan.
			if strings.HasPrefix(name, "main-") && fmt.Sprint(get["passed"]) != "[plan-main]" {
				t.Errorf("%s: passed %v", name, get["passed"])
			}
		}
	}

	// the batches step deals as many batches as run together, and all run
	batches := 0

	walkMaps(p, func(m M) {
		for _, a := range asSlice(m["across"]) {
			if asMap(a)["var"] == "batch" {
				batches++

				if asMap(a)["max_in_flight"] != "all" {
					t.Errorf("batches run %v at once", asMap(a)["max_in_flight"])
				}
			}
		}
	})

	if batches == 0 {
		t.Error("no suite runs batches")
	}
}

func TestAnUnchangedNightlyStartsNoSuite(t *testing.T) {
	spec, root := fixture(t)

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"nightly-backend", "nightly-e2e"} {
		walkMaps(jobByName(t, p, name), func(m M) {
			if m["get"] == triggerResource("nightly") || m["resource"] == triggerResource("nightly") {
				t.Errorf("%s takes the timer: %v", name, m)
			}
		})
	}

	walkMaps(jobByName(t, p, "plan-nightly"), func(m M) {
		if m["get"] == triggerResource("nightly") && m["trigger"] != true {
			t.Errorf("the planning job doesn't start on the timer: %v", m)
		}
	})
}
