package runstep

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"
)

// The pipelinegen tokens, each split by its purpose, which the prepare
// command never holds.
var toolTokens = []string{"PIPELINEGEN_READ_TOKEN", "PIPELINEGEN_CHANGES_TOKEN", "PIPELINEGEN_REPORT_TOKEN", "PIPELINEGEN_STORE_TOKEN"}

const githubToken = "GITHUB_TOKEN" //nolint:gosec // a variable's name

func (t *Task) plannerNames() []string { return strings.Fields(t.getenv("PIPELINEGEN_PLANNER_ENV")) }

// withoutPlanner is env without the planner's params and secrets, which are
// the prepare command's alone. A GITHUB_TOKEN the planner declares is the
// read token, which the tool reads too.
func (t *Task) withoutPlanner(env []string) []string {
	names := slices.DeleteFunc(t.plannerNames(), func(n string) bool { return n == githubToken })

	return unset(env, names...)
}

// prepare runs the planner's prepare command in dir. It fills the plugins'
// shared caches and may use the network, which the plugins never do. The
// pipeline's token reaches it only when the planner declares it.
func (t *Task) prepare(env []string, dir string) int {
	command := getenv(env, "PIPELINEGEN_PREPARE")
	if command == "" {
		return 0
	}

	root, err := filepath.Abs(t.Dir)
	if err != nil {
		t.sayf("%v", err)

		return 1
	}

	caches := filepath.Join(root, "plan-cache", "caches")

	for _, c := range []string{"gomodcache", "gocache", "npm"} {
		if err := os.MkdirAll(filepath.Join(caches, c), 0o750); err != nil {
			t.sayf("%v", err)

			return 1
		}
	}

	if !slices.Contains(t.plannerNames(), githubToken) {
		env = unset(env, githubToken)
	}

	env = unset(env, toolTokens...)

	// git asks a credential helper that reads the declared secret when it
	// needs it: the lines name the variable and never hold the token.
	if name := getenv(env, "PIPELINEGEN_GIT_TOKEN"); name != "" {
		var lines bytes.Buffer

		cmd := t.command(env, t.Dir, tool, "git-auth", name)
		cmd.Stdout = &lines

		// As the shell's eval of its output did, the step takes what it
		// printed whatever its exit.
		_ = cmd.Run() //nolint:errcheck // see above

		exports, err := exportsOf(lines.String())
		if err != nil {
			t.sayf("%v", err)

			return 1
		}

		env = with(env, exports...)
	}

	if !t.isDir(dir) {
		t.sayf("cd: %s: No such file or directory", dir)

		return 1
	}

	env = with(env, "GOMODCACHE="+filepath.Join(caches, "gomodcache"), "GOCACHE="+filepath.Join(caches, "gocache"),
		"npm_config_cache="+filepath.Join(caches, "npm"))

	return t.exited("prepare", t.command(env, t.path(dir), "sh", "-ec", command).Run())
}

var export = regexp.MustCompile(`^export ([A-Za-z_][A-Za-z0-9_]*)=(?:'([^']*)'|([^\s'"\\$` + "`" + `]*))$`)

// exportsOf reads the lines `pipelinegen git-auth` prints, `export NAME=value`
// with the value bare or in single quotes, as NAME=value.
func exportsOf(lines string) ([]string, error) {
	var out []string

	for line := range strings.SplitSeq(lines, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}

		m := export.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("%w: pipelinegen git-auth printed %q", errUnread, line)
		}

		out = append(out, m[1]+"="+m[2]+m[3])
	}

	return out, nil
}

// run is the run the task is for.
func (t *Task) run() (string, bool) {
	if !t.must("PIPELINEGEN_RUN") {
		return "", false
	}

	return t.getenv("PIPELINEGEN_RUN"), true
}

// plan plans a branch's commit in one step: a branch's code is trusted.
// The plan is the last command, so no command follows a Bazel server it
// leaves.
func (t *Task) plan([]string) int {
	if code := t.prepare(t.Env, "trigger"); code != 0 {
		return code
	}

	run, ok := t.run()
	if !ok {
		return 2
	}

	return t.last(t.withoutPlanner(t.Env), planCmd, "--run", run, "--trigger", "trigger", "--root", "trigger", "--cache", "plan-cache")
}

// continuousPlan decides first, and plans nothing for a commit that isn't
// the branch's newest or had its round; the newest waits while a round is
// in progress. The decision has the task's whole limit; the prepare and the
// plan share the plan's limit, counted from the decision's end.
func (t *Task) continuousPlan(args []string) int {
	fs := flag.NewFlagSet("continuous-plan", flag.ContinueOnError)
	fs.SetOutput(t.Stderr)
	minutes := fs.Int("plan-minutes", 0, "the plan's limit in minutes")

	if err := fs.Parse(args); err != nil || *minutes <= 0 {
		t.sayf("continuous-plan: --plan-minutes is the plan's limit, a positive number")

		return 2
	}

	run, ok := t.run()
	if !ok {
		return 2
	}

	plan := []string{"--run", run, "--trigger", "trigger", "--root", "trigger", "--cache", "plan-cache", "--gate", "handoff"}

	// The decision sees none of the prepare command's params or secrets.
	if code := t.call(t.withoutPlanner(t.Env), tool, append([]string{planCmd, "--decide"}, plan...)...); code != 0 {
		return code
	}

	if t.exists("handoff/gate.json") {
		return 0
	}

	env := with(t.Env, fmt.Sprintf("%s=%d", plugin.EnvDeadline, t.Now().Unix()+int64(*minutes)*60),
		fmt.Sprintf("%s=limits.plan=%d", plugin.EnvLimit, *minutes))

	if code := t.prepare(env, "trigger"); code != 0 {
		return code
	}

	return t.last(t.withoutPlanner(env), append([]string{planCmd}, plan...)...)
}

// begin runs none of the change's code. Its record makes the sweep leave
// the head to this build for the plan's limit instead of planning it beside
// a slow plan.
func (t *Task) begin([]string) int {
	run, ok := t.run()
	if !ok {
		return 2
	}

	return t.last(t.Env, planCmd, "--begin", "--run", run, "--trigger", "trigger", "--root", "trigger", "--cache", "write-cache")
}

// proposedPlan runs the change's code (Bazel reads its build files), so it
// holds no credential that writes; the prepare command runs the base's
// scripts, never the change's.
func (t *Task) proposedPlan([]string) int {
	if code := t.prepare(t.Env, "base"); code != 0 {
		return code
	}

	run, ok := t.run()
	if !ok {
		return 2
	}

	return t.last(t.withoutPlanner(t.Env), planCmd, "--run", run, "--trigger", "trigger", "--root", "trigger",
		"--cache", "plan-cache", "--out", "plan")
}

// proposedWrite runs none of the change's code. The hand-off is put only
// when the gate opens: the plan was written, or the head was planned and
// stalled; never for a head whose suites began or are on their way.
func (t *Task) proposedWrite([]string) int {
	if code := t.handoff("trigger", "handoff"); code != 0 {
		return code
	}

	run, ok := t.run()
	if !ok {
		return 2
	}

	return t.last(t.Env, planCmd, "--run", run, "--trigger", "trigger", "--root", "trigger",
		"--cache", "write-cache", "--from", planCmd, "--gate", "handoff")
}

// pipeline writes the pipeline a pipeline that sets itself sets.
func (t *Task) pipeline(args []string) int {
	forRunner, ok := t.forFlag("pipeline", args)
	if !ok {
		return 2
	}

	return t.last(t.Env, then(forRunner, "pipeline", "--root", "repo", "--gate", "pipeline", "--out", "pipeline/pipeline.yml")...)
}

// then is args with more after them.
func then(more []string, args ...string) []string { return slices.Concat(args, more) }

// forFlag reads a step's --for <runner>, which names the runner whose
// pipeline pipelinegen writes when the config has several.
func (t *Task) forFlag(step string, args []string) ([]string, bool) {
	switch {
	case len(args) == 0:
		return nil, true
	case len(args) == 2 && args[0] == "--for" && args[1] != "":
		return args, true
	}

	t.sayf("usage: concourse-runner step %s [--for <runner>]", step)

	return nil, false
}

var (
	committed = regexp.MustCompile(`"committed":"[^"]*"`)
	opening   = regexp.MustCompile(`^\{`)
)

// handoff writes what the pull request resource's put reads from a
// checkout's. Its "committed" is now, with a random fraction of a second,
// so a head handed on again is a new version.
func (t *Task) handoff(from, to string) int {
	src, dst := filepath.Join(from, ".git", "resource"), filepath.Join(to, ".git", "resource")

	if err := os.MkdirAll(t.path(dst), 0o750); err != nil {
		t.sayf("%v", err)

		return 1
	}

	version, err := os.ReadFile(t.path(filepath.Join(src, "version.json")))
	if err != nil {
		t.sayf("%v", err)

		return 2
	}

	var r [4]byte
	if _, err := io.ReadFull(rand.Reader, r[:]); err != nil {
		t.sayf("%v", err)

		return 1
	}

	now := t.Now().UTC().Format("2006-01-02T15:04:05") + fmt.Sprintf(".%09dZ", binary.LittleEndian.Uint32(r[:])%1_000_000_000)

	re, stamp := opening, `{"committed":"`+now+`",`
	if bytes.Contains(version, []byte(`"committed":`)) {
		re, stamp = committed, `"committed":"`+now+`"`
	}

	if code := t.writeFile(filepath.Join(dst, "version.json"), firstOnEachLine(string(version), re, stamp)); code != 0 {
		return code
	}

	if meta := filepath.Join(src, "metadata.json"); t.isFile(meta) {
		return t.copyFile(meta, filepath.Join(dst, "metadata.json"))
	}

	return t.writeFile(filepath.Join(dst, "metadata.json"), "[]\n")
}

// firstOnEachLine replaces the first match on each line, as sed's s does.
func firstOnEachLine(text string, re *regexp.Regexp, repl string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if loc := re.FindStringIndex(line); loc != nil {
			lines[i] = line[:loc[0]] + repl + line[loc[1]:]
		}
	}

	return strings.Join(lines, "\n")
}

func (t *Task) copyFile(from, to string) int {
	data, err := os.ReadFile(t.path(from))
	if err != nil {
		t.sayf("cp: %v", err)

		return 1
	}

	return t.writeFile(to, string(data))
}

// handoffRun hands on a connector's change: the mock resource's version is
// its run.json.
func (t *Task) handoffRun(from, to string) int {
	if err := os.MkdirAll(t.path(to), 0o750); err != nil {
		t.sayf("%v", err)

		return 1
	}

	return t.copyFile(filepath.Join(from, "run.json"), filepath.Join(to, "run.json"))
}

// quoted is a list of pull requests as the hand-offs' list holds them.
func quoted(items []string) string {
	q := make([]string, 0, len(items))
	for _, it := range items {
		q = append(q, `"`+it+`"`)
	}

	return strings.Join(q, ",")
}
