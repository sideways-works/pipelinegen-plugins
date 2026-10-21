package concourse

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

func jobNamed(t *testing.T, p M, name string) M {
	t.Helper()

	for _, j := range asSlice(p["jobs"]) {
		if job := asMap(j); job["name"] == name {
			return job
		}
	}

	t.Fatalf("no job %s", name)

	return nil
}

func resourceNamed(t *testing.T, p M, name string) M {
	t.Helper()

	for _, r := range asSlice(p["resources"]) {
		if res := asMap(r); res["name"] == name {
			return res
		}
	}

	t.Fatalf("no resource %s", name)

	return nil
}

func firstGets(job M) map[string]M {
	out := map[string]M{}
	for _, g := range asSlice(asMap(asSlice(job["plan"])[0])["in_parallel"]) {
		out[fmt.Sprint(asMap(g)["get"])] = asMap(g)
	}

	return out
}

// Without passed, Concourse takes the hand-offs in the order the puts landed,
// not the order the planning builds began.
func TestPullRequestSuitesTakeEveryHandOff(t *testing.T) {
	spec, root := fixture(t)

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	planned := resourceNamed(t, p, "planned-pr")
	trigger := resourceNamed(t, p, "trigger-pr")

	if planned["type"] != "github-pr" || planned["check_every"] != "never" {
		t.Errorf("hand-off resource: %v", planned)
	}

	// A source of its own, since Concourse shares the versions of equal sources.
	src, tsrc := asMap(planned["source"]), asMap(trigger["source"])
	if fmt.Sprint(src) == fmt.Sprint(tsrc) || src["repository"] != tsrc["repository"] || src["access_token"] != tsrc["access_token"] {
		t.Errorf("hand-off source %v, trigger's %v", src, tsrc)
	}

	if got := fmt.Sprint(src["ignore_paths"]); !strings.Contains(got, spec.Pipeline+" pr") {
		t.Errorf("hand-off source not the pipeline's and run's own: %s", got)
	}

	for _, suite := range []string{"pr-backend", "pr-e2e"} {
		get := firstGets(jobNamed(t, p, suite))["trigger"]
		if get["resource"] != "planned-pr" || get["version"] != "every" || get["trigger"] != true || get["passed"] != nil ||
			fmt.Sprint(get["params"]) != "map[integration_tool:checkout]" {
			t.Errorf("%s: trigger %v", suite, get)
		}
	}

	plan := asSlice(jobNamed(t, p, "plan-pr")["plan"])
	if get := firstGets(jobNamed(t, p, "plan-pr"))["trigger"]; get["resource"] != "trigger-pr" || get["version"] != "every" {
		t.Errorf("plan-pr: trigger %v", get)
	}

	task, put := asMap(plan[len(plan)-3]), asMap(asSlice(asMap(plan[len(plan)-1])["do"])[0])
	if task["task"] != "write" || !slices.Contains(mounts(asMap(task["config"]), "outputs"), "handoff") {
		t.Errorf("plan-pr: the write step writes no hand-off: %v", task)
	}

	if put["put"] != "planned-pr" || put["no_get"] != true || fmt.Sprint(put["inputs"]) != "[handoff]" ||
		fmt.Sprint(put["params"]) != "map[path:handoff]" {
		t.Errorf("plan-pr: put %v", put)
	}

	if get := firstGets(jobNamed(t, p, "main-e2e"))["trigger"]; get["version"] != "every" || fmt.Sprint(get["passed"]) != "[plan-main]" {
		t.Errorf("main-e2e: trigger %v", get)
	}

	if plan := asSlice(jobNamed(t, p, "plan-main")["plan"]); len(plan) != 2 {
		t.Errorf("plan-main: %d steps", len(plan))
	}
}

func TestASweepPerPullRequestRun(t *testing.T) {
	spec, root := fixture(t)

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	if timer := resourceNamed(t, p, "sweep-pr-timer"); timer["type"] != "time" || fmt.Sprint(timer["source"]) != "map[interval:5m]" {
		t.Errorf("timer %v", timer)
	}

	job := jobNamed(t, p, "sweep-pr")
	if _, ok := job["max_in_flight"]; ok {
		t.Errorf("sweep: max_in_flight %v", job["max_in_flight"])
	}

	gets := firstGets(job)
	if gets["sweep-pr-timer"]["trigger"] != true || gets["planned"]["resource"] != "planned-pr" ||
		gets["planned"]["trigger"] != true || fmt.Sprint(gets["planned"]["params"]) != "map[skip_download:true]" ||
		gets["config"] == nil || gets["config"]["trigger"] == true {
		t.Errorf("sweep: gets %v", gets)
	}

	plan := asSlice(job["plan"])
	list := asMap(plan[1])

	if list["task"] != "sweep" || !strings.Contains(fmt.Sprint(asMap(list["config"])["run"]), "tools/pipelinegen unplanned") {
		t.Errorf("sweep: first task %v", list["task"])
	}

	if fmt.Sprint(plan[2]) != "map[file:gate/gate.json load_var:gate]" {
		t.Errorf("sweep: gate %v", plan[2])
	}

	gated := asMap(plan[3])
	work := asSlice(asMap(asSlice(gated["do"])[0])["do"])

	if fmt.Sprint(gated["across"]) != "[map[values:((.:gate)) var:taken]]" || len(work) != 4 ||
		asMap(work[0])["task"] != "plan" || asMap(work[1])["task"] != "write" {
		t.Fatalf("sweep: gated work %v", gated)
	}

	if fmt.Sprint(work[2]) != "map[file:handoffs/list.json load_var:handoffs]" {
		t.Errorf("sweep: load_var %v", work[2])
	}

	across := asMap(work[3])
	put := asMap(asSlice(across["do"])[0])

	if fmt.Sprint(across["across"]) != "[map[values:((.:handoffs)) var:handoff]]" || put["put"] != "planned-pr" ||
		put["no_get"] != true || fmt.Sprint(put["params"]) != "map[path:handoffs/((.:handoff))]" {
		t.Errorf("sweep: puts %v", across)
	}

	for _, j := range jobNames(t, p) {
		if strings.HasPrefix(j, "sweep-") && j != "sweep-pr" {
			t.Errorf("a sweep for another run: %s", j)
		}
	}
}

func TestSerialGroupsPerRun(t *testing.T) {
	spec, root := fixture(t)
	spec.Suites[suiteIndex(t, spec, "e2e")].SerialGroup = "proof"
	spec.Suites[suiteIndex(t, spec, "backend")].SerialGroup = "heavy"
	spec.CI["serial_groups"] = map[string]any{"proof": map[string]any{"per_run": true}, "heavy": map[string]any{"per_run": false}}

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"pr-e2e": "[proof-pr]", "main-e2e": "[proof-main]", "nightly-e2e": "[proof-nightly]",
		"pr-backend": "[heavy]", "main-backend": "[heavy]", "nightly-backend": "[heavy]",
	}

	for job, groups := range want {
		if got := fmt.Sprint(jobNamed(t, p, job)["serial_groups"]); got != groups {
			t.Errorf("%s: serial groups %s, want %s", job, got, groups)
		}
	}

	delete(spec.CI, "serial_groups")

	if p, err = Generate(spec, root); err != nil {
		t.Fatal(err)
	}

	if got := fmt.Sprint(jobNamed(t, p, "pr-e2e")["serial_groups"]); got != "[proof]" {
		t.Errorf("pr-e2e without the key: %s", got)
	}
}

func handOffVersion(t *testing.T, version, metadata string) (map[string]string, string) {
	t.Helper()

	work := t.TempDir()
	writeTestFile(t, filepath.Join(work, "head", ".git", "resource", "version.json"), version)

	if metadata != "" {
		writeTestFile(t, filepath.Join(work, "head", ".git", "resource", "metadata.json"), metadata)
	}

	cmd := exec.CommandContext(t.Context(), "sh", "-ec", planLib+"handoff head out\n")
	cmd.Dir = work

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	var v map[string]string
	if err := json.Unmarshal(readTestFile(t, filepath.Join(work, "out", ".git", "resource", "version.json")), &v); err != nil {
		t.Fatal(err)
	}

	return v, string(readTestFile(t, filepath.Join(work, "out", ".git", "resource", "metadata.json")))
}

// A random fraction in "committed" makes two plans of one head two versions.
func TestEveryHandOffIsAVersionOfItsOwn(t *testing.T) {
	head := `{"commit":"0123456789abcdef0123456789abcdef01234567","committed":"2026-01-02T03:04:05Z","pr":"7","state":"OPEN"}`
	meta := `[{"name":"head_name","value":"topic"}]`

	first, gotMeta := handOffVersion(t, head, meta)
	second, _ := handOffVersion(t, head, meta)

	for _, v := range []map[string]string{first, second} {
		if v["pr"] != "7" || v["commit"] != "0123456789abcdef0123456789abcdef01234567" || v["state"] != "OPEN" {
			t.Errorf("hand-off lost the head: %v", v)
		}

		at, err := time.Parse(time.RFC3339Nano, v["committed"])
		if err != nil || time.Since(at) > time.Hour || time.Since(at) < -time.Minute {
			t.Errorf("committed %q: %v", v["committed"], err)
		}
	}

	if first["committed"] == second["committed"] {
		t.Errorf("two plans, one version: %s", first["committed"])
	}

	if gotMeta != meta {
		t.Errorf("metadata %q", gotMeta)
	}

	bare, bareMeta := handOffVersion(t, `{"commit":"abc","pr":"9"}`, "")
	if _, err := time.Parse(time.RFC3339Nano, bare["committed"]); err != nil || bare["pr"] != "9" || bare["commit"] != "abc" {
		t.Errorf("bare head: %v %v", bare, err)
	}

	if strings.TrimSpace(bareMeta) != "[]" {
		t.Errorf("bare metadata %q", bareMeta)
	}
}

func runScript(t *testing.T, work, script string, env ...string) []byte {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "sh", "-ec", script)
	cmd.Dir = work

	cmd.Env = append(os.Environ(), env...)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	return out
}

func TestAProposedChangeIsPlannedInTwoSteps(t *testing.T) {
	work := t.TempDir()
	writeTestFile(t, filepath.Join(work, "trigger", ".git", "resource", "version.json"), `{"commit":"abc","pr":"7"}`)
	writeTestFile(t, filepath.Join(work, "base", "README"), "base\n")
	writeFakeTool(t, work, "#!/bin/sh\necho \"$* ${SECRET_OF_PREPARE:-none}\" >> called\n")

	runScript(t, work, planLib+proposedPlan, "PIPELINEGEN_RUN=pr", "SECRET_OF_PREPARE=s",
		"PIPELINEGEN_PLANNER_ENV=SECRET_OF_PREPARE", `PIPELINEGEN_PREPARE=echo "$SECRET_OF_PREPARE" > prepared`)

	if got := strings.TrimSpace(string(readTestFile(t, filepath.Join(work, "base", "prepared")))); got != "s" {
		t.Errorf("prepare in the base: %q", got)
	}

	if _, err := os.Stat(filepath.Join(work, "trigger", "prepared")); err == nil {
		t.Error("the prepare command ran in the change's tree")
	}

	if got := string(readTestFile(t, filepath.Join(work, "called"))); got != "plan --run pr --trigger trigger --root trigger --cache plan-cache --out plan none\n" {
		t.Errorf("first step: %q", got)
	}

	if _, err := os.Stat(filepath.Join(work, "handoff")); err == nil {
		t.Error("the first step handed off")
	}

	if err := os.Remove(filepath.Join(work, "called")); err != nil {
		t.Fatal(err)
	}

	for _, gate := range []string{`["1"]`, `[]`} {
		writeFakeTool(t, work, "#!/bin/sh\necho \"$* ${SECRET_OF_PREPARE:-none}\" > called\nmkdir -p \"${13}\"\necho '"+gate+"' > \"${13}/gate.json\"\n")

		runScript(t, work, planLib+proposedWrite, "PIPELINEGEN_RUN=pr")

		if got := string(readTestFile(t, filepath.Join(work, "called"))); got != "plan --run pr --trigger trigger --root trigger --cache write-cache --from plan --gate handoff none\n" {
			t.Errorf("second step: %q", got)
		}

		if _, err := os.Stat(filepath.Join(work, "handoff", ".git", "resource", "version.json")); err != nil {
			t.Errorf("no hand-off: %v", err)
		}

		if got := strings.TrimSpace(string(readTestFile(t, filepath.Join(work, "handoff", "gate.json")))); got != gate {
			t.Errorf("the gate the job reads: %s, the tool wrote %s", got, gate)
		}
	}
}

func TestAPullRequestsPlanningJobPutsOnlyThroughItsGate(t *testing.T) {
	spec, root := fixture(t)

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	plan := asSlice(jobNamed(t, p, "plan-pr")["plan"])
	if len(plan) != 6 {
		t.Fatalf("plan-pr: %d steps, want the gets, three tasks, the gate and the gated put", len(plan))
	}

	if write := asMap(plan[3]); write["task"] != "write" {
		t.Fatalf("plan-pr's fourth step: %v", plan[3])
	}

	if load := asMap(plan[4]); load["load_var"] != "planned" || load["file"] != "handoff/gate.json" {
		t.Errorf("plan-pr loads %v", plan[4])
	}

	across := asMap(plan[5])
	vars := asSlice(across["across"])

	if len(vars) != 1 || asMap(vars[0])["values"] != "((.:planned))" {
		t.Fatalf("plan-pr's put isn't in an across over the gate: %v", plan[4])
	}

	do := asSlice(across["do"])
	if len(do) != 1 || asMap(do[0])["put"] != "planned-pr" {
		t.Errorf("the gated put: %v", do)
	}

	for _, step := range plan {
		if asMap(step)["put"] != nil {
			t.Errorf("plan-pr puts outside the gate: %v", step)
		}
	}
}

// A head whose plan or write fails is left for the next sweep.
func TestTheSweepPlansInItsOwnStep(t *testing.T) {
	work := t.TempDir()

	// Heads 7, 8 and 10 to plan, 9 to hand off again; 8's plan fails, and 10
	// was planned meanwhile, so its write step leaves the gate shut.
	fake := `#!/bin/sh
case "$1" in
unplanned)
  for pr in 7 8 10; do
    mkdir -p "sweep/plan/$pr/.git/resource"
    echo "{\"commit\":\"c$pr\",\"pr\":\"$pr\"}" > "sweep/plan/$pr/.git/resource/version.json"
  done
  mkdir -p sweep/handoff/9/.git/resource
  echo '{"commit":"c9","pr":"9"}' > sweep/handoff/9/.git/resource/version.json
  ;;
plan)
  head=$(cat "$5/.git/resource/version.json")
  echo "${10} $head ${SECRET_OF_PREPARE:-none}" >> called
  case "${10}" in
  --out) grep -q '"pr":"8"' "$5/.git/resource/version.json" && exit 1; mkdir -p "${11}"; echo "plan of $head" > "${11}/plan.json" ;;
  --from)
    grep -q "$head" "${11}/plan.json" || exit 1
    echo "${12} ${13}" >> gates
    mkdir -p "${13}"
    case "$head" in
    *'"pr":"10"'*) echo '[]' > "${13}/gate.json" ;;
    *) echo '["1"]' > "${13}/gate.json" ;;
    esac
    ;;
  esac
  ;;
esac
`
	writeFakeTool(t, work, fake)
	writeTestFile(t, filepath.Join(work, "base", "README"), "base\n")

	for _, d := range []string{"gate", "plans", "handoffs"} {
		if err := os.MkdirAll(filepath.Join(work, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}

	env := []string{
		"PIPELINEGEN_RUN=pr", "SECRET_OF_PREPARE=s", "PIPELINEGEN_PLANNER_ENV=SECRET_OF_PREPARE",
		`PIPELINEGEN_PREPARE=echo "$SECRET_OF_PREPARE" >> prepared`,
	}

	runScript(t, work, planLib+sweepScript, env...)

	if got := strings.TrimSpace(string(readTestFile(t, filepath.Join(work, "gate", "gate.json")))); got != `["1"]` {
		t.Fatalf("gate %s", got)
	}

	// The second step works on a copy of the first step's output, as a task's
	// input is; the last step reads the first step's own.
	pristine := t.TempDir()
	if out, err := exec.CommandContext(t.Context(), "cp", "-a", filepath.Join(work, "sweep"), pristine).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}

	runScript(t, work, planLib+sweepPlan, env...)

	if got := strings.TrimSpace(string(readTestFile(t, filepath.Join(work, "base", "prepared")))); got != strings.Repeat("s\n", 2)+"s" {
		t.Errorf("prepare in the base, once per head: %q", got)
	}

	if err := os.RemoveAll(filepath.Join(work, "sweep")); err != nil {
		t.Fatal(err)
	}

	if out, err := exec.CommandContext(t.Context(), "cp", "-a", filepath.Join(pristine, "sweep"), work).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}

	runScript(t, work, planLib+sweepWrite, "PIPELINEGEN_RUN=pr")

	want := `--out {"commit":"c10","pr":"10"} none
--out {"commit":"c7","pr":"7"} none
--out {"commit":"c8","pr":"8"} none
--from {"commit":"c10","pr":"10"} none
--from {"commit":"c7","pr":"7"} none
`
	if got := string(readTestFile(t, filepath.Join(work, "called"))); got != want {
		t.Errorf("plans:\n%s", got)
	}

	if got := strings.TrimSpace(string(readTestFile(t, filepath.Join(work, "handoffs", "list.json")))); got != `["7","9"]` {
		t.Errorf("hand-offs %s", got)
	}

	for _, pr := range []string{"7", "9"} {
		var v map[string]string
		if err := json.Unmarshal(readTestFile(t, filepath.Join(work, "handoffs", pr, ".git", "resource", "version.json")), &v); err != nil || v["pr"] != pr || v["commit"] != "c"+pr {
			t.Errorf("hand-off %s: %v %v", pr, v, err)
		}
	}

	for _, pr := range []string{"8", "10", ".gate"} {
		if _, err := os.Stat(filepath.Join(work, "handoffs", pr)); err == nil {
			t.Errorf("%s, not planned, planned meanwhile or the gate, is among the hand-offs", pr)
		}
	}

	if got := string(readTestFile(t, filepath.Join(work, "gates"))); got != "--gate handoffs/.gate\n--gate handoffs/.gate\n" {
		t.Errorf("the writes' gates: %q", got)
	}

	if err := os.RemoveAll(filepath.Join(work, "sweep")); err != nil {
		t.Fatal(err)
	}

	writeFakeTool(t, work, "#!/bin/sh\nmkdir -p sweep\n")
	runScript(t, work, planLib+sweepScript, "PIPELINEGEN_RUN=pr")

	if got := strings.TrimSpace(string(readTestFile(t, filepath.Join(work, "gate", "gate.json")))); got != "[]" {
		t.Errorf("nothing to take up: gate %s", got)
	}
}

func splitSpec(t *testing.T) (plugin.Spec, string) {
	t.Helper()

	spec, root := fixture(t)
	spec.GitHub.Tokens = map[string]string{
		plugin.PurposeRead: "reader", plugin.PurposeChanges: "lister", plugin.PurposeReport: "reporter", plugin.PurposeStore: "storer",
	}
	spec.GitHub.TokenVar = "storer"

	return spec, root
}

func refVars(v any) []string {
	var out []string
	for _, m := range regexp.MustCompile(`\(\(([^.)][^)]*)\)\)`).FindAllStringSubmatch(fmt.Sprint(v), -1) {
		out = append(out, m[1])
	}

	return out
}

func TestAProposedChangesCodeRunsWithTheReadCredentialAlone(t *testing.T) {
	spec, root := splitSpec(t)

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	planner := map[string]bool{}
	for _, v := range asMap(asMap(spec.CI["planner"])["secrets"]) {
		planner[fmt.Sprint(v)] = true
	}

	for _, name := range []string{"plan-pr", "sweep-pr"} {
		job := jobNamed(t, p, name)
		seenWrite := false

		walkTasks(job, func(task M) {
			params := asMap(asMap(task["config"])["params"])

			switch task["task"] {
			case "plan":
				for _, v := range refVars(params) {
					if v != "reader" && !planner[v] {
						t.Errorf("%s: the task that runs the change's code references %s", name, v)
					}
				}

				if seenWrite && name == "plan-pr" {
					t.Errorf("%s: the change's code runs after the write step", name)
				}
			case "write", "sweep":
				seenWrite = seenWrite || task["task"] == "write"

				if params["PIPELINEGEN_STORE_TOKEN"] != "((storer))" || params["PIPELINEGEN_REPORT_TOKEN"] != "((reporter))" {
					t.Errorf("%s %s: writes without the tokens that write", name, task["task"])
				}

				for _, v := range refVars(params) {
					if planner[v] {
						t.Errorf("%s %s: a step that writes holds the planner's secret %s", name, task["task"], v)
					}
				}
			}
		})
	}

	// Before its write step, only the begin step holds a writing token (the
	// store's), and it runs none of the change's code.
	for _, step := range asSlice(jobNamed(t, p, "plan-pr")["plan"]) {
		if asMap(step)["task"] == "write" {
			break
		}

		for _, v := range refVars(step) {
			if v == "reporter" || v == "storer" && asMap(step)["task"] != "begin" {
				t.Errorf("plan-pr: a step before the write step holds the %s token: %v", v, asMap(step)["task"])
			}
		}
	}

	if plan := asSlice(jobNamed(t, p, "plan-main")["plan"]); len(plan) != 2 {
		t.Errorf("plan-main: %d steps, want one task", len(plan))
	}
}

func writeFakeTool(t *testing.T, work, script string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Join(work, "tools"), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(work, "tools", "pipelinegen"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func writeTestFile(t *testing.T, p, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, p string) []byte {
	t.Helper()

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func TestAPullRequestsPlanningBeginsWithItsRecord(t *testing.T) {
	spec, root := fixture(t)

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	plan := asSlice(jobNamed(t, p, "plan-pr")["plan"])

	begin, first := asMap(plan[1]), asMap(plan[2])
	if begin["task"] != "begin" || first["task"] != "plan" {
		t.Fatalf("plan-pr's tasks: %v, then %v", begin["task"], first["task"])
	}

	cfg := asMap(begin["config"])
	if !slices.Contains(mounts(cfg, "caches"), "write-cache") || fmt.Sprint(mounts(cfg, "outputs")) != "[tools]" {
		t.Errorf("begin mounts %v", cfg)
	}

	if _, ok := asMap(cfg["params"])["PIPELINEGEN_PREPARE"]; ok {
		t.Error("begin holds the planner's prepare")
	}

	work := t.TempDir()
	writeFakeTool(t, work, "#!/bin/sh\necho \"$*\" > called\n")
	runScript(t, work, proposedBegin, "PIPELINEGEN_RUN=pr")

	if got := string(readTestFile(t, filepath.Join(work, "called"))); got != "plan --begin --run pr --trigger trigger --root trigger --cache write-cache\n" {
		t.Errorf("begin calls %q", got)
	}
}
