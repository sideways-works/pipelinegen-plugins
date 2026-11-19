package concourse

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

type flagUse struct {
	def           string
	reads, writes bool
	boolean       bool
	// file: the path may lie in the build's own directory.
	file bool
}

// toolCommand is a pipelinegen command as the tool's code defines it: every
// flag it takes, and what it reads beyond its flags.
type toolCommand struct {
	flags map[string]flagUse
	// named commands take a name before their flags (slice start <name>).
	named bool
	// rootIsTrigger: the command reads the config from --trigger whatever
	// --root says.
	rootIsTrigger bool
	// runFromEnv: PIPELINEGEN_CHANGE and PIPELINEGEN_COMMIT, else
	// PIPELINEGEN_RUN_FILE, else the checkout PIPELINEGEN_TRIGGER names.
	runFromEnv bool
	// also are directories it reads without a flag naming them.
	also []string
	// recording are directories it reads in a recording run.
	recording []string
	// taskFileRecording are directories it reads in a recording run of a suite whose batch is its own
	// task file.
	taskFileRecording []string
	tokenless         bool
	// linksConcourse: only writing the pipeline needs Concourse's address.
	linksConcourse bool
}

func reads(def string) flagUse  { return flagUse{def: def, reads: true} }
func writes(def string) flagUse { return flagUse{def: def, writes: true} }

var other = flagUse{}

func common(root string, more map[string]flagUse) map[string]flagUse {
	out := map[string]flagUse{"root": reads(root), "config": reads("")}
	maps.Copy(out, more)

	return out
}

// toolCommands mirror the flag sets of the tool's internal/cli.
var toolCommands = map[string]toolCommand{
	"git-auth": {named: true, tokenless: true, flags: map[string]flagUse{"host": other}},
	"pipeline": {tokenless: true, linksConcourse: true, flags: common(".", map[string]flagUse{
		"out": {writes: true, file: true}, "plugins": reads("tools/plugins"), "runner": reads(""), "gate": writes(""),
	})},
	"plan": {rootIsTrigger: true, also: []string{"tools/plugins"}, flags: common(".", map[string]flagUse{
		"run": other, "suite": other, "trigger": reads("trigger"),
		"cache": {reads: true, writes: true}, "bazel-diff": reads(""),
		"out": {writes: true}, "from": reads(""), "gate": writes(""), "begin": {boolean: true},
	})},
	"unplanned": {flags: common(".", map[string]flagUse{
		"run": other, "out": writes("sweep"), "cache": {reads: true, writes: true},
	})},
	"tree": {rootIsTrigger: true, flags: common(".", map[string]flagUse{
		"run": other, "suite": other, "trigger": reads("trigger"),
		"out": writes("source"), "gate": writes("gate"),
	})},
	"batches": {flags: common(".", map[string]flagUse{
		"run": other, "suite": other, "trigger": reads("trigger"), "out": writes("batches"),
	})},
	"report start":  {runFromEnv: true, flags: common(".", nil)},
	"report suite":  {runFromEnv: true, flags: common(".", map[string]flagUse{"batches": reads("")})},
	"report finish": {runFromEnv: true, flags: common(".", nil)},
	"report slice": {runFromEnv: true, flags: common(".", map[string]flagUse{
		"state": other, "result": reads(""), "coverage": reads(""),
	})},
	"report whole": {runFromEnv: true, flags: common(".", map[string]flagUse{
		"name": other, "result": reads(""),
	})},
	"report batch-end": {
		runFromEnv: true,
		flags: common(".", map[string]flagUse{
			"batch": other, "batches": reads("batches"), "result": reads("result"),
			"coverage": reads(""), "tools": reads("tools"), "started": reads(""),
		}),
		// The journal's ends name the tree and coverage by their defaults; the tree is read only to
		// observe what the batch left unobserved, every slice of a task file's batch.
		recording:         []string{"coverage"},
		taskFileRecording: []string{"source"},
	},
	"report collect": {runFromEnv: true, flags: common(".", map[string]flagUse{
		"suite": other, "out": writes("slices"),
	})},
	"slice start":     {named: true, runFromEnv: true, flags: sliceFlags()},
	"slice end":       {named: true, runFromEnv: true, flags: sliceFlags()},
	"slice setup-end": {runFromEnv: true, flags: common("source", map[string]flagUse{"coverage": writes("coverage"), "result": writes("result"), "tools": reads("tools")})},
	"slice flush":     {runFromEnv: true, flags: common("source", map[string]flagUse{"result": reads("result"), "tools": reads("tools")})},
	"slice list":      {tokenless: true, flags: map[string]flagUse{"batches": reads("batches")}},
	"slice bookkeeping-files": {tokenless: true, flags: map[string]flagUse{
		"root": reads("source"), "out": writes("bookkeeping"),
	}},
}

func sliceFlags() map[string]flagUse {
	return common("source", map[string]flagUse{
		"batches": reads("batches"), "result": {reads: true, writes: true},
		"coverage": writes("coverage"), "tools": reads("tools"),
	})
}

var errCall = errors.New("pipelinegen call")

var withSubcommands = map[string]bool{"report": true, "slice": true, "lists": true}

type call struct {
	command string
	flags   map[string]string
	name    string
}

// parseCall reads flags the way Go's flag package does: one or two dashes,
// a value after a space or an equals sign.
func parseCall(args []string) (call, error) {
	if len(args) == 0 {
		return call{}, errCall
	}

	c := call{command: args[0], flags: map[string]string{}}
	args = args[1:]

	if withSubcommands[c.command] {
		if len(args) == 0 {
			return c, fmt.Errorf("%w: %s without a subcommand", errCall, c.command)
		}

		c.command += " " + args[0]
		args = args[1:]
	}

	tc, ok := toolCommands[c.command]
	if !ok {
		return c, fmt.Errorf("%w: pipelinegen has no command %q", errCall, c.command)
	}

	if tc.named {
		if len(args) == 0 || strings.HasPrefix(args[0], "-") {
			return c, fmt.Errorf("%w: %s without its name", errCall, c.command)
		}

		c.name, args = args[0], args[1:]
	}

	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			return c, fmt.Errorf("%w: %s: %q after the flags", errCall, c.command, a)
		}

		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if _, ok := tc.flags[name]; !ok {
			return c, fmt.Errorf("%w: %s has no flag -%s", errCall, c.command, name)
		}

		if !hasValue && tc.flags[name].boolean {
			value = "true"
		} else if !hasValue {
			if i+1 >= len(args) {
				return c, fmt.Errorf("%w: %s: -%s without a value", errCall, c.command, name)
			}

			i++
			value = args[i]
		}

		c.flags[name] = value
	}

	return c, nil
}

func top(p string) string {
	p = path.Clean(p)
	if i := strings.Index(p, "/"); i >= 0 {
		return p[:i]
	}

	return p
}

func (c call) needs(params M, recording, taskFile bool) (read, written []string, err error) { //nolint:nonamedreturns // two lists and an error
	tc := toolCommands[c.command]

	value := func(name string) string {
		if v, ok := c.flags[name]; ok {
			return v
		}

		return tc.flags[name].def
	}

	for name, use := range tc.flags {
		v := value(name)
		if name == "config" && v == "" {
			continue // <root>/pipelinegen.yaml, read with the root
		}

		if name == "root" && tc.rootIsTrigger {
			v = value("trigger")
		}

		if v == "" {
			continue
		}

		if use.reads {
			read = append(read, top(v))
		}

		// A file in the build's own directory lives only in the step.
		if use.writes && (!use.file || strings.Contains(path.Clean(v), "/")) {
			written = append(written, top(v))
		}
	}

	read = append(read, "tools") // the executable itself
	for _, d := range tc.also {
		read = append(read, top(d))
	}

	if recording {
		for _, d := range tc.recording {
			read = append(read, top(d))
		}
	}

	if recording && taskFile {
		for _, d := range tc.taskFileRecording {
			read = append(read, top(d))
		}
	}

	env := func(k string) string { s, _ := params[k].(string); return s } //nolint:errcheck // absent is empty

	if b := env("PIPELINEGEN_BAZELISK"); b != "" {
		read = append(read, top(b))
	}

	if tc.runFromEnv && (env("PIPELINEGEN_CHANGE") == "" || env("PIPELINEGEN_COMMIT") == "") {
		switch {
		case env("PIPELINEGEN_RUN_FILE") != "":
			read = append(read, top(env("PIPELINEGEN_RUN_FILE")))
		case env("PIPELINEGEN_TRIGGER") != "":
			read = append(read, top(env("PIPELINEGEN_TRIGGER")))
		default:
			return nil, nil, fmt.Errorf("%w: %s has no way to learn its run", errCall, c.command)
		}
	}

	return read, written, nil
}

// pipelinegenCalls are the calls of the tool a task makes. A task that runs
// the runner's step is run against a stand-in for the tool that notes each
// call, opens each gate but a decision's, and writes what each call names
// as its output, so the step takes every branch that calls the tool.
func pipelinegenCalls(t *testing.T, cfg M) [][]string {
	t.Helper()

	run := asMap(cfg["run"])

	args := make([]string, 0)

	for _, a := range asSlice(run["args"]) {
		s, ok := a.(string)
		if !ok {
			t.Fatalf("an argument that is no string: %v", a)
		}

		args = append(args, s)
	}

	switch {
	case run["path"] == "tools/pipelinegen":
		return [][]string{args}
	case run["path"] != "sh" || len(args) < 3:
		return nil
	case args[2] == "tools/pipelinegen":
		// a tool's call that installs packages first
		return [][]string{args[3:]}
	case strings.Contains(args[1], bootstrapScript):
		return stepCalls(t, cfg, args[3:])
	}

	return nil
}

const recordingTool = `#!/bin/sh
printf '%s\n' "$*" >> "$(dirname "$0")/../calls"
[ "$1" = git-auth ] && exit 0
prev=
for a in "$@"; do
  case "$prev" in
  --gate) [ "$2" = --decide ] || { mkdir -p "$a" && echo '["1"]' > "$a/gate.json"; } ;;
  --out)
    case "$1" in
    pipeline) mkdir -p "$(dirname "$a")" && echo 'jobs: []' > "$a" ;;
    unplanned)
      for d in "$a/plan/7" "$a/handoff/9"; do
        mkdir -p "$d/.git/resource" && echo '{"commit":"abc","pr":"7"}' > "$d/.git/resource/version.json"
      done
      ;;
    *) mkdir -p "$a" ;;
    esac
    ;;
  esac
  prev=$a
done
`

func stepCalls(t *testing.T, cfg M, step []string) [][]string {
	t.Helper()

	work := t.TempDir()

	for _, in := range mounts(cfg, "inputs", "caches") {
		if err := os.MkdirAll(filepath.Join(work, in), 0o750); err != nil {
			t.Fatal(err)
		}
	}

	for _, d := range []string{"trigger", "sweep/plan/7", "sweep/handoff/9"} {
		if !slices.Contains(mounts(cfg, "inputs"), top(d)) {
			continue
		}

		writeTestFile(t, filepath.Join(work, d, ".git", "resource", "version.json"), `{"commit":"abc","pr":"7"}`)
		writeTestFile(t, filepath.Join(work, d, "run.json"), "{}\n")
	}

	if slices.Contains(mounts(cfg, "inputs"), "plans") {
		writeTestFile(t, filepath.Join(work, "plans", "7", "plan.json"), "{}")
	}

	for _, out := range mounts(cfg, "outputs") {
		if err := os.MkdirAll(filepath.Join(work, out), 0o750); err != nil {
			t.Fatal(err)
		}
	}

	writeFakeTool(t, work, recordingTool)

	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}

	params := asMap(cfg["params"])
	for _, k := range slices.Sorted(maps.Keys(params)) {
		env = append(env, k+"="+fmt.Sprint(params[k]))
	}

	// a prepare command, so its git-auth call is seen
	if v, ok := params["PIPELINEGEN_PREPARE"]; ok && v == "" {
		env = append(env, "PIPELINEGEN_PREPARE=true")
	}

	taskStep(t, work, env, step[0], step[1:]...)

	data, err := os.ReadFile(filepath.Join(work, "calls"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	var out [][]string

	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if line != "" {
			out = append(out, strings.Fields(line))
		}
	}

	return out
}

func asSlice(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []M:
		out := make([]any, 0, len(x))
		for _, m := range x {
			out = append(out, m)
		}

		return out
	case []string:
		out := make([]any, 0, len(x))
		for _, s := range x {
			out = append(out, s)
		}

		return out
	}

	return nil
}

func mounts(cfg M, keys ...string) []string {
	var out []string

	for _, k := range keys {
		for _, it := range asSlice(cfg[k]) {
			if m, ok := it.(M); ok {
				if k == "caches" {
					out = append(out, top(fmt.Sprint(m["path"])))
				} else {
					out = append(out, fmt.Sprint(m["name"]))
				}
			}
		}
	}

	return out
}

func steppedSpecs(t *testing.T) map[string]plugin.Spec {
	t.Helper()

	plain, _ := fixture(t)

	locked, _ := fixture(t)
	locked.Suites[suiteIndex(t, locked, "e2e")].Lock = &plugin.SpecLock{Name: "browsers", Minutes: 90}
	locked.Suites[suiteIndex(t, locked, "backend")].Lock = &plugin.SpecLock{Name: "db", Minutes: 20}

	self, _ := fixture(t)
	self.CI["self_set"] = true

	return map[string]plugin.Spec{"plain": plain, "an older tool's locks": locked, "self_set": self}
}

func TestEveryPipelinegenStepMountsWhatItReads(t *testing.T) {
	seen := map[string]bool{}

	for variant, spec := range steppedSpecs(t) {
		p, err := Generate(spec, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}

		recording, taskFile := map[string]bool{}, map[string]bool{}

		for _, r := range spec.Runs {
			for _, rs := range r.Suites {
				recording[SuiteJob(r.Name, rs.Suite)] = rs.Records
				taskFile[SuiteJob(r.Name, rs.Suite)] = spec.Suites[suiteIndex(t, spec, rs.Suite)].Steps == nil
			}
		}

		for _, j := range asSlice(p["jobs"]) {
			job := asMap(j)
			jobName := fmt.Sprint(job["name"])

			walkTasks(job, func(task M) {
				cfg := asMap(task["config"])

				// Concourse names inputs and outputs, and caches by their path.
				for key, field := range map[string]string{"inputs": "name", "outputs": "name", "caches": "path"} {
					for _, it := range asSlice(cfg[key]) {
						if s, ok := asMap(it)[field].(string); !ok || s == "" {
							t.Errorf("%s: job %s, step %v: one of its %s has no %s: %v", variant, jobName, task["task"], key, field, it)
						}
					}
				}

				// A task that begins with the bootstrap has the tools it fetched; each
				// later command sees what earlier ones wrote.
				present := mounts(cfg, "inputs", "caches")
				if args := asSlice(asMap(cfg["run"])["args"]); len(args) >= 2 && strings.Contains(fmt.Sprint(args[1]), bootstrapScript) {
					present = append(present, "tools")
				}

				for _, args := range pipelinegenCalls(t, cfg) {
					where := fmt.Sprintf("%s: job %s, step %v: pipelinegen %s", variant, jobName, task["task"], strings.Join(args, " "))

					c, err := parseCall(args)
					if err != nil {
						t.Errorf("%s: %v", where, err)

						continue
					}

					seen[c.command] = true

					if !toolCommands[c.command].tokenless && asMap(cfg["params"])["GITHUB_TOKEN"] == nil {
						t.Errorf("%s: writes refs or statuses without the pipeline's token", where)
					}

					if url, ok := asMap(cfg["params"])["PIPELINEGEN_CONCOURSE_URL"]; toolCommands[c.command].linksConcourse && url != spec.CI["url"] {
						t.Errorf("%s: writes the pipeline without Concourse's address", where)
					} else if !toolCommands[c.command].linksConcourse && ok {
						t.Errorf("%s: gets Concourse's address, which only writing the pipeline needs", where)
					}

					read, written, err := c.needs(asMap(cfg["params"]), recording[jobName], taskFile[jobName])
					if err != nil {
						t.Errorf("%s: %v", where, err)

						continue
					}

					mounted := present
					for _, d := range read {
						if d == "." {
							t.Errorf("%s: reads the build's own directory as a checkout", where)
						} else if !slices.Contains(mounted, d) {
							t.Errorf("%s: reads %s, which the step doesn't mount (it has %v)", where, d, mounted)
						}
					}

					kept := mounts(cfg, "outputs", "caches")

					for _, w := range written {
						present = append(present, w)

						if !slices.Contains(kept, w) {
							t.Errorf("%s: writes %s, which is no output or cache of the step (it has %v)", where, w, kept)
						}
					}
				}
			})
		}
	}

	for _, want := range []string{
		"plan", "tree", "batches", "report batch-end", "report whole", "report collect",
		"report suite", "pipeline", "git-auth",
	} {
		if !seen[want] {
			t.Errorf("no generated step runs pipelinegen %s", want)
		}
	}
}

func TestParseCallRefusesWhatTheToolRefuses(t *testing.T) {
	for _, bad := range [][]string{
		{"slice", "start", "e2e.a", "--minutes", "90", "--root", "source"},
		{"report", "batch-end", "--root"},
		{"report", "nothing"},
		{"slice", "start", "--root", "source"},
		{"lock", "acquire", "browsers"},
		{"unknown"},
	} {
		if _, err := parseCall(bad); err == nil {
			t.Errorf("pipelinegen %s parsed", strings.Join(bad, " "))
		}
	}

	c, err := parseCall([]string{"report", "batch-end", "--batch", "((.:batch))", "--result=result", "-root", "source"})
	if err != nil {
		t.Fatal(err)
	}

	read, _, err := c.needs(M{"PIPELINEGEN_TRIGGER": "trigger"}, true, true)
	if err != nil {
		t.Fatal(err)
	}

	slices.Sort(read)

	if got := strings.Join(slices.Compact(read), ","); got != "batches,coverage,result,source,tools,trigger" {
		t.Errorf("batch-end reads %s", got)
	}

	// a driven batch's end reads its root and the batch's outputs, and no tree
	c, err = parseCall([]string{"report", "batch-end", "--batch", "((.:batch))", "--result=result", "-root", "bookkeeping"})
	if err != nil {
		t.Fatal(err)
	}

	read, _, err = c.needs(M{"PIPELINEGEN_TRIGGER": "trigger"}, true, false)
	if err != nil {
		t.Fatal(err)
	}

	slices.Sort(read)

	if got := strings.Join(slices.Compact(read), ","); got != "batches,bookkeeping,coverage,result,tools,trigger" {
		t.Errorf("a driven batch's end reads %s", got)
	}
}

func TestBatchEndsTakeTheBookkeepingFilesWhenTheToolWritesThem(t *testing.T) {
	for _, tc := range []struct {
		pin  plugin.SpecTool
		want bool
	}{
		{plugin.SpecTool{Commit: "0123456789abcdef0123456789abcdef01234567"}, true},
		{plugin.SpecTool{Version: "v0.4.2", SHA256: "x"}, true},
		{plugin.SpecTool{Version: "v0.10.0", SHA256: "x"}, true},
		{plugin.SpecTool{Version: "v0.4.1", SHA256: "x"}, false},
		{plugin.SpecTool{Version: "v0.4.2-rc.1", SHA256: "x"}, false},
	} {
		spec, _ := fixture(t)

		for i, tl := range spec.Tools {
			if tl.Kind == plugin.ToolPipelinegen {
				tc.pin.Name, tc.pin.Kind, tc.pin.Repository = tl.Name, tl.Kind, tl.Repository
				spec.Tools[i] = tc.pin
			}
		}

		p, err := Generate(spec, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}

		roots := map[string]string{}

		for _, j := range asSlice(p["jobs"]) {
			job := asMap(j)

			walkTasks(job, func(task M) {
				if task["task"] != "batch-end" {
					return
				}

				args := asSlice(asMap(asMap(task["config"])["run"])["args"])
				roots[fmt.Sprint(job["name"])] = fmt.Sprint(args[len(args)-1])
			})
		}

		// e2e records and runs its batches by a task file, so its ends observe in the tree
		want := map[string]string{"main-backend": "source", "main-e2e": "source"}
		if tc.want {
			want["main-backend"] = bookkeeping
		}

		for job, root := range want {
			if roots[job] != root {
				t.Errorf("tool %+v: %s's batch end takes %q, want %q", tc.pin, job, roots[job], root)
			}
		}
	}
}

func TestARecordingDrivenSuitesBatchEndTakesTheBookkeepingFiles(t *testing.T) {
	spec, root := fixture(t)
	spec.Trusted = `{"git_remote":"x"}`

	i := suiteIndex(t, spec, "e2e")
	spec.Suites[i].Prepare, spec.Suites[i].RunSlice = "", ""
	spec.Suites[i].Image = "golang"
	spec.Suites[i].Steps = &plugin.SpecSteps{Drivers: []string{"go"}}

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	found := false

	for _, j := range asSlice(p["jobs"]) {
		job := asMap(j)
		if job["name"] != "main-e2e" {
			continue
		}

		walkTasks(job, func(task M) {
			if task["task"] != "batch-end" {
				return
			}

			found = true

			cfg := asMap(task["config"])
			if args := asSlice(asMap(cfg["run"])["args"]); fmt.Sprint(args[len(args)-1]) != bookkeeping || slices.Contains(mounts(cfg, "inputs"), "source") {
				t.Errorf("a driven recording suite's batch end: %v, inputs %v", args, mounts(cfg, "inputs"))
			}
		})
	}

	if !found {
		t.Fatal("main-e2e has no batch end")
	}
}
