// Package pipeline writes example-runner's Concourse pipeline from a spec.
package pipeline

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"
)

// The pipeline's directories: what each step mounts and what the tool's commands read and write.
const (
	dirTools   = "tools"
	dirTrigger = "trigger"
	dirSource  = "source"
	dirGate    = "gate"
	dirBatches = "batches"
	dirResult  = "result"
	dirRepo    = "repo"
	cacheTools = "cache"
	cachePlan  = "plan-cache"
	planner    = "planner"
	tool       = "tools/pipelinegen"
	// File is the pipeline's file in the call's work directory.
	File = "pipeline.yml"
)

// ErrPlacement is a placement the runner can't follow.
var ErrPlacement = errors.New("placement")

// m is a map of the pipeline's YAML: yaml.v3 writes its keys sorted, so a spec always gives the
// same bytes.
type m = map[string]any

// Placement reads the worker tags every step runs on, the config's placement.default; it refuses
// any other key, since a placement it ignored would put steps where nobody meant.
func Placement(config map[string]any) ([]string, error) {
	raw, ok := config["placement"]
	if !ok {
		return nil, nil
	}

	p, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: placement is no map", ErrPlacement)
	}

	var tags []string

	for key, v := range p {
		list, isList := v.([]any)
		if key != "default" || !isList {
			return nil, fmt.Errorf("%w: placement.%s: this runner reads placement.default, a list of worker tags", ErrPlacement, key)
		}

		for _, t := range list {
			s, isString := t.(string)
			if !isString || s == "" {
				return nil, fmt.Errorf("%w: placement.default: %v is no worker tag", ErrPlacement, t)
			}

			tags = append(tags, s)
		}
	}

	return tags, nil
}

// writer writes one spec's pipeline.
type writer struct {
	s        plugin.Spec
	ci       m
	token    string
	notices  []string
	resource map[string]m
}

// Build is the pipeline of the spec, every step on the workers tags names, and the notices of
// what it leaves out.
func Build(s plugin.Spec, tags []string) (map[string]any, []string) {
	w := &writer{s: s, ci: s.CI, token: "((" + s.GitHub.TokenVar + "))", resource: map[string]m{}}

	w.resource[planner] = m{
		"name": planner, "type": "registry-image", "check_every": "24h",
		"source":  m{"repository": w.str("planner", "image", "repository"), "tag": w.str("planner", "image", "tag")},
		"version": m{"digest": w.str("planner", "digest")},
	}
	w.resource["config"] = w.git("config", s.Branch, s.ConfigFiles)

	if inc := w.str("include"); inc != "" {
		w.notices = append(w.notices, "concourse.include ("+inc+") is not merged: this runner writes the generated jobs alone")
	}

	jobs := []any{w.pipelineJob()}

	for _, r := range s.Runs {
		code, ok := w.trigger(r)
		if !ok {
			continue
		}

		jobs = append(jobs, w.planJob(r, code))

		for _, rs := range r.Suites {
			jobs = append(jobs, w.suiteJob(r, rs, code))
		}
	}

	resources := make([]any, 0, len(w.resource))
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for n := range w.resource {
			if !yield(n) {
				return
			}
		}
	}) {
		resources = append(resources, w.resource[name])
	}

	p := m{"resources": resources, "jobs": jobs}
	if len(tags) > 0 {
		place(p["jobs"], tags)
		place(p["resources"], tags)
	}

	return p, w.notices
}

// place puts every step and resource check on the workers tags names.
func place(v any, tags []string) {
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			place(e, tags)
		}
	case m:
		for _, kind := range []string{"task", "get", "put", "set_pipeline"} {
			if _, ok := x[kind]; ok {
				x["tags"] = tags
			}
		}

		if _, isResource := x["source"]; isResource && x["type"] != nil {
			x["tags"] = tags
		}

		for _, v := range x {
			place(v, tags)
		}
	}
}

func (w *writer) str(path ...string) string {
	var v any = w.ci

	for _, p := range path {
		mm, ok := v.(map[string]any)
		if !ok {
			return ""
		}

		v = mm[p]
	}

	s, _ := v.(string) //nolint:errcheck // a missing setting is empty

	return s
}

func (w *writer) git(name, branch string, paths []string) m {
	src := m{"uri": w.str("repository", "uri"), "branch": branch}

	for _, k := range []string{"username", "password"} {
		if v := w.str("repository", k); v != "" {
			src[k] = v
		}
	}

	if len(paths) > 0 {
		src["paths"] = paths
	}

	return m{"name": name, "type": "git", "source": src}
}

// trigger adds the resources a run starts from and names the one its code comes from; a run of a
// kind this runner doesn't write gets a notice instead of jobs.
func (w *writer) trigger(r plugin.SpecRun) (string, bool) {
	switch r.Trigger.Kind {
	case "branch", "timer":
	default:
		w.notices = append(w.notices, fmt.Sprintf("run %s: a %s run is not written: this runner writes branch and timer runs", r.Name, r.Trigger.Kind))

		return "", false
	}

	code := "branch-" + r.Trigger.Branch
	w.resource[code] = w.git(code, r.Trigger.Branch, nil)

	if t := r.Trigger.Timer; t != nil {
		src := m{"start": t.Start, "stop": t.Stop}
		if t.Location != "" {
			src["location"] = t.Location
		}

		if len(t.Days) > 0 {
			src["days"] = t.Days
		}

		w.resource["timer-"+r.Name] = m{"name": "timer-" + r.Name, "type": "time", "source": src}
	}

	return code, true
}

func minutes(n int) string { return strconv.Itoa(max(n, 1)) + "m" }

// toolParams are what every step of the tool's gets: the spec's trusted keys as they are, the mark
// of a pipeline's step and the run, plus extra.
func (w *writer) toolParams(extra m) m {
	p := m{plugin.EnvTrusted: w.s.Trusted, plugin.EnvPipelineStep: "1", "PIPELINEGEN_BAZELISK": "tools/bazelisk"}

	maps.Copy(p, extra)

	return p
}

func (w *writer) jobURL(job string) string {
	return w.str("url") + "/teams/" + w.str("team") + "/pipelines/" + w.s.Pipeline + "/jobs/" + job
}

// toolTask is a task of the tool's in the planner image: args run it directly, or a script does.
func toolTask(name string, inputs, outputs, caches []string, params m, timeout string, run m) m {
	cfg := m{"platform": "linux", "params": params, "run": run}

	if len(inputs) > 0 {
		cfg["inputs"] = names(inputs)
	}

	if len(outputs) > 0 {
		cfg["outputs"] = names(outputs)
	}

	if len(caches) > 0 {
		cs := make([]any, 0, len(caches))
		for _, c := range caches {
			cs = append(cs, m{"path": c})
		}

		cfg["caches"] = cs
	}

	return m{"task": name, "image": planner, "config": cfg, "timeout": timeout}
}

func names(dirs []string) []any {
	var out []any

	for _, d := range dirs {
		if name, ok := strings.CutSuffix(d, "?"); ok {
			out = append(out, m{"name": name, "optional": true})
		} else {
			out = append(out, m{"name": d})
		}
	}

	return out
}

func direct(args ...string) m { return m{"path": tool, "args": args} }

func script(text string) m { return m{"path": "sh", "args": []string{"-ec", text}} }

// bootstrap fetches the tool, bazelisk and the plugins the spec pins into tools: a release by its
// checksum, or a commit with go install.
func (w *writer) bootstrap() m {
	lines := make([]string, 0, len(w.s.Tools))

	for _, t := range w.s.Tools {
		dest, program := "tools/plugins/"+t.Name, plugin.Program(t.Repository)

		switch t.Kind {
		case plugin.ToolPipelinegen:
			dest, program = "tools/"+t.Name, plugin.ToolPipelinegen
		case plugin.ToolBazelisk:
			dest, program = "tools/bazelisk", "bazelisk"
			t.Repository = "bazelbuild/bazelisk"
		}

		lines = append(lines, strings.Join([]string{dest, t.Repository, program, or(t.Version), or(t.SHA256), or(t.Commit)}, " "))
	}

	return toolTask("tools", nil, []string{dirTools}, nil, m{"PIPELINEGEN_TOOLS": strings.Join(lines, "\n") + "\n"},
		minutes(w.s.Limits.Steps), script(bootstrapScript))
}

func or(s string) string {
	if s == "" {
		return "-"
	}

	return s
}

const bootstrapScript = `mkdir -p tools/plugins
case "$(uname -m)" in x86_64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) arch=$(uname -m) ;; esac
printf '%s' "$PIPELINEGEN_TOOLS" | while read -r dest repo program version sha256 commit; do
  if [ "$sha256" != - ]; then
    curl -fsSL -o "$dest" "https://github.com/$repo/releases/download/$version/$program-linux-$arch"
    echo "$sha256  $dest" | sha256sum -c - >/dev/null || { echo "$dest doesn't match its sha256" >&2; exit 1; }
    chmod +x "$dest"
  elif [ "$commit" != - ]; then
    GOBIN="$PWD/tools/.bin" go install "github.com/$repo/cmd/$program@$commit"
    mv "tools/.bin/$program" "$dest"
  else
    echo "$dest is pinned by neither a release's checksum nor a commit" >&2
    exit 1
  fi
done
`

func gets(timeout string, steps ...m) m {
	list := make([]any, 0, len(steps))
	for _, s := range steps {
		list = append(list, s)
	}

	return m{"in_parallel": list, "timeout": timeout}
}

// pipelineJob makes the pipeline again from the config whenever it changes, and sets it when the
// config says the pipeline sets itself.
func (w *writer) pipelineJob() m {
	steps := minutes(w.s.Limits.Steps)
	plan := []any{
		gets(steps, m{"get": dirRepo, "resource": "config", "trigger": true}, m{"get": planner}),
		w.bootstrap(),
		toolTask("pipeline", []string{dirTools, dirRepo}, []string{"pipeline"}, nil,
			w.toolParams(m{"PIPELINEGEN_CONCOURSE_URL": w.str("url")}), steps,
			direct("pipeline", "--root", dirRepo, "--plugins", "tools/plugins", "--out", "pipeline/"+File)),
	}

	if self, _ := w.ci["self_set"].(bool); self { //nolint:errcheck // absent is false
		plan = append(plan, m{"set_pipeline": "self", "file": "pipeline/" + File, "timeout": steps})
	}

	return m{"name": "pipeline", "plan": plan}
}

// planJob plans each commit the run's trigger brings: the planner's prepare command with git
// reading the token, then plan.
func (w *writer) planJob(r plugin.SpecRun, code string) m {
	name := "plan-" + r.Name
	in := []m{{"get": dirTrigger, "resource": code, "trigger": r.Trigger.Timer == nil}, {"get": planner}}

	if r.Trigger.Timer != nil {
		in = append(in, m{"get": "timer", "resource": "timer-" + r.Name, "trigger": true})
	}

	prepare := ""
	if p := w.str("planner", "prepare"); p != "" {
		prepare = "(cd trigger && " + p + ")\n"
	}

	params := w.toolParams(m{"GITHUB_TOKEN": w.token, "PIPELINEGEN_RUN": r.Name, "PIPELINEGEN_TRIGGER": dirTrigger, "PIPELINEGEN_JOB_URL": w.jobURL(name)})
	plan := toolTask("plan", []string{dirTools, dirTrigger}, nil, []string{cachePlan}, params, minutes(w.s.Limits.Plan),
		script(`eval "$(tools/pipelinegen git-auth GITHUB_TOKEN)"`+"\n"+prepare+
			`exec tools/pipelinegen plan --run "$PIPELINEGEN_RUN" --trigger trigger --root trigger --cache plan-cache`+"\n"))

	return m{"name": name, "plan": []any{gets(minutes(w.s.Limits.Steps), in...), w.bootstrap(), plan}}
}

func (w *writer) suite(name string) plugin.SpecSuite {
	for _, s := range w.s.Suites {
		if s.Name == name {
			return s
		}
	}

	return plugin.SpecSuite{Name: name}
}

// suiteJob runs a run's suite for each commit its planning job planned: the tree and the batches,
// the prepare task, every batch at once, each whole-suite check the plan selected, and the reports.
func (w *writer) suiteJob(r plugin.SpecRun, rs plugin.SpecRunSuite, code string) m {
	name, su, steps := r.Name+"-"+rs.Suite, w.suite(rs.Suite), minutes(w.s.Limits.Steps)
	tp := w.toolParams(m{
		"GITHUB_TOKEN": w.token, "PIPELINEGEN_RUN": r.Name, "PIPELINEGEN_SUITE": rs.Suite,
		"PIPELINEGEN_TRIGGER": dirTrigger, "PIPELINEGEN_JOB_URL": w.jobURL(name),
	})

	in := []m{{"get": dirTrigger, "resource": code, "passed": []string{"plan-" + r.Name}, "trigger": true}, {"get": planner}}
	if su.Image != "" {
		in = append(in, m{"get": su.Image})
	}

	start := toolTask("start", []string{dirTools, dirTrigger}, []string{dirSource, dirGate, dirBatches}, nil, tp, steps, script(
		`tools/pipelinegen tree --run "$PIPELINEGEN_RUN" --suite "$PIPELINEGEN_SUITE" --trigger trigger --out source --gate gate`+"\n"+
			`if [ "$(cat gate/gate.json)" = "[]" ]; then echo '[]' > batches/batches.json; exit 0; fi`+"\n"+
			`exec tools/pipelinegen batches --run "$PIPELINEGEN_RUN" --suite "$PIPELINEGEN_SUITE" --trigger trigger --out batches --root source`+"\n"))

	var planned []any

	if su.Prepare != "" {
		planned = append(planned, w.repoTask("prepare", su, r, rs, su.Prepare, rs.Secrets.Prepare, nil))
	}

	endInputs := []string{dirTools, dirSource, dirTrigger, dirBatches, dirResult + "?"}
	if rs.Records {
		endInputs = append(endInputs, "coverage?")
	}

	batch := w.repoTask("batch", su, r, rs, su.RunSlice, rs.Secrets.Batch, m{"PIPELINEGEN_BATCH": "((.:batch))"})
	batch["ensure"] = toolTask("batch-end", endInputs, nil, nil, tp, steps,
		direct("report", "batch-end", "--batch", "((.:batch))", "--result", dirResult, "--root", dirSource))
	planned = append(planned, m{
		"across": []any{m{"var": "batch", "values": "((.:batches))", "max_in_flight": "all"}},
		"do":     []any{batch},
	})

	for i, wh := range su.WholeSuite {
		v, result := "whole_"+strconv.Itoa(i), "result-whole."+wh.Name
		check := w.repoTask("whole."+wh.Name, su, r, rs, wh.Task, rs.Secrets.Whole, nil)
		check["output_mapping"] = m{dirResult: result}
		check["ensure"] = toolTask("whole."+wh.Name+"-ends", []string{dirTools, dirSource, dirTrigger, result + "?"}, nil, nil, tp, steps,
			direct("report", "whole", "--name", wh.Name, "--result", result, "--root", dirSource))
		planned = append(planned,
			m{"load_var": v, "file": "gate/whole-" + wh.Name + ".json"},
			m{"across": []any{m{"var": v + "_on", "values": "((.:" + v + "))"}}, "do": []any{
				toolTask("collect-"+wh.Name, []string{dirTools, dirSource, dirTrigger}, []string{"slices"}, nil, tp, steps,
					direct("report", "collect", "--suite", rs.Suite, "--out", "slices", "--root", dirSource)),
				check,
			}})
	}

	return m{
		"name": name,
		"plan": []any{
			gets(steps, in...), w.bootstrap(), start,
			m{"load_var": "gate", "file": "gate/gate.json"},
			m{"load_var": "batches", "file": "batches/batches.json"},
			m{"across": []any{m{"var": "planned", "values": "((.:gate))"}}, "do": planned},
		},
		// the suite's end whatever happened before; a report that could not be written fails the job
		"ensure": toolTask("end", []string{dirTools + "?", dirTrigger + "?", dirBatches + "?"}, nil, nil, tp, steps,
			direct("report", "suite", "--root", dirTrigger, "--batches", dirBatches)),
	}
}

// repoTask runs one of the repository's task files with the run's params and the secrets the spec
// declares for it, and nothing else: no token and no trusted keys.
func (w *writer) repoTask(name string, su plugin.SpecSuite, r plugin.SpecRun, rs plugin.SpecRunSuite, file string, secrets map[string]string, extra m) m {
	params := m{"PIPELINEGEN_RUN": r.Name, "PIPELINEGEN_SUITE": rs.Suite, "PIPELINEGEN_RECORD": strconv.FormatBool(rs.Records)}

	for k, v := range rs.Params {
		params[k] = v
	}

	for k, v := range secrets {
		params[k] = "((" + v + "))"
	}

	maps.Copy(params, extra)

	t := m{"task": name, "file": dirSource + "/" + file, "params": params, "timeout": minutes(rs.Minutes)}
	if su.Image != "" {
		t["image"] = su.Image
	}

	if su.Privileged {
		t["privileged"] = true
	}

	return t
}
