// Package concourse writes a repository's Concourse pipeline from the spec
// pipelinegen gives its runner. The slices a run selects never appear in
// it: a suite job reads them at run time from the run's plan.
package concourse

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/pipelinegen/plugin"
	"github.com/sideways-works/plugkit/pin"

	"github.com/sideways-works/pipelinegen-plugins/runner/concourse/internal/runstep"
)

// M is one mapping of a pipeline's YAML.
type M = map[string]any

// bootstrapScript is the one script a task runs: it places the runner the
// pipeline pins and runs the task's step with it, `concourse-runner step
// <name>`, which does the step's work in Go.
//
//go:embed bootstrap.sh
var bootstrapScript string

const staleHash = "PIPELINEGEN_PIPELINE_SHA256"

// runStep is a task's run: the bootstrap, then the runner's step with args.
// The bootstrap's $0 names it in the shell's own messages.
func runStep(args ...string) M {
	all := make([]any, 0, 3+len(args))
	all = append(all, "-ec", bootstrapScript, "bootstrap")

	for _, a := range args {
		all = append(all, a)
	}

	return M{"path": "sh", "args": all}
}

// defaultPullRequests is the maintained fork of telia-oss/github-pr-resource.
var defaultPullRequests = M{"repository": "cfcommunity/github-pr-resource", "tag": "0.26.0"}

// ErrInclude wraps an include file that can't be merged.
var ErrInclude = errors.New("include")

// ErrSteps is a suite run by drivers that names no image to run in.
var ErrSteps = errors.New("steps")

type gen struct {
	spec   plugin.Spec
	ci     settings
	place  placer
	runs   map[string]plugin.SpecRun
	suites map[string]plugin.SpecSuite
	// staleParams get the pipeline's hash once the pipeline is complete.
	staleParams M
	plannerFrom map[string]string
	pkgs        Packages
	// included are the include's registry-image sources, by name.
	included map[string]M
}

// Generate writes the pipeline for a spec; root is the repository's
// checkout, where the include lives.
func Generate(spec plugin.Spec, root string) (M, error) {
	return GenerateWith(spec, root, nil)
}

func GenerateWith(spec plugin.Spec, root string, config map[string]any) (M, error) {
	return GenerateAll(spec, Packages{}, root, config)
}

func GenerateAll(spec plugin.Spec, pkgs Packages, root string, config map[string]any) (M, error) {
	ci, err := readSettings(spec.CI)
	if err != nil {
		return nil, err
	}

	if err := checkEnv(spec, ci); err != nil {
		return nil, err
	}

	conf, err := readConfig(config)
	if err != nil {
		return nil, err
	}

	place := newPlacer(spec, conf)
	if err := place.check(spec); err != nil {
		return nil, err
	}

	g := &gen{spec: spec, ci: ci, place: place, runs: map[string]plugin.SpecRun{}, suites: map[string]plugin.SpecSuite{}, pkgs: pkgs}

	for _, r := range spec.Runs {
		g.runs[r.Name] = r
	}

	for _, s := range spec.Suites {
		if s.Steps != nil && s.Image == "" {
			return nil, fmt.Errorf("%w: suite %s is run by drivers and has no task file to name its image: name one in suites.%s.image", ErrSteps, s.Name, s.Name)
		}

		g.suites[s.Name] = s
	}

	if g.ci.Include != "" {
		if err := g.sharePlannerImage(filepath.Join(root, g.ci.Include)); err != nil {
			return nil, err
		}

		if err := g.readIncluded(filepath.Join(root, g.ci.Include)); err != nil {
			return nil, err
		}
	}

	checks := g.place.tags(forChecks, "", "")
	types := []any{withTags(g.pullRequestsType(), checks)}

	resources := append(g.resources(), g.imageResources()...)
	for _, r := range resources {
		if m, ok := r.(M); ok {
			withTags(m, checks)
		}
	}

	p, err := g.pipeline(types, resources)
	if err != nil {
		return nil, err
	}

	if g.ci.Include != "" {
		if err := include(p, filepath.Join(root, g.ci.Include)); err != nil {
			return nil, err
		}
	}

	p["groups"] = append(g.groups(p), groupsOf(p)...)

	if err := g.sealStale(p); err != nil {
		return nil, err
	}

	return p, nil
}

// pipeline is the generated pipeline before the include joins it: every
// task that runs pipelinegen installs its packages first.
func (g *gen) pipeline(types, resources []any) (M, error) {
	jobs, err := g.jobs()
	if err != nil {
		return nil, err
	}

	images, err := g.imageJobs()
	if err != nil {
		return nil, err
	}

	p := M{
		"resource_types": types,
		"resources":      resources,
		"jobs":           append(jobs, images...),
	}

	g.withInstalls(p)

	return p, nil
}

func Text(p M) ([]byte, error) {
	out, err := yaml.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("pipeline yaml: %w", err)
	}

	return out, nil
}

// Concourse pins a resource type's image by tag only, so the settings may
// name a mirrored image to pin it by content.
func (g *gen) pullRequestsType() M {
	src := maps.Clone(defaultPullRequests)
	if len(g.ci.PullRequests) > 0 {
		src = maps.Clone(g.ci.PullRequests)
	}

	return M{"name": "github-pr", "type": "registry-image", "source": src}
}

func (g *gen) token() string { return "((" + g.spec.GitHub.TokenVar + "))" }

const triggerContinuous = plugin.TriggerContinuous

const capContinuous = plugin.CapContinuous

// branchResource writes each byte Concourse refuses in a name, "_" too, as
// _xx, so two branches never share a name.
func branchResource(branch string) string { return "branch-" + safeName(branch) }

func safeName(s string) string {
	var b strings.Builder

	for i := range len(s) {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.' {
			b.WriteByte(c)

			continue
		}

		fmt.Fprintf(&b, "_%02x", c)
	}

	return b.String()
}

func triggerResource(run string) string { return "trigger-" + run }

func planJob(run string) string { return "plan-" + run }

func plannedResource(run string) string { return "planned-" + run }

func sweepJob(run string) string { return "sweep-" + run }

func sweepTimer(run string) string { return "sweep-" + run + "-timer" }

const sweepEvery = "5m"

func SuiteJob(run, suite string) string { return run + "-" + suite }

func (g *gen) resources() []any {
	// Concourse refuses a pipeline with a resource no job uses.
	branches := map[string]bool{}

	out := []any{M{
		"name":        "planner",
		"type":        "registry-image",
		"source":      g.ci.Planner.Image,
		"version":     M{"digest": g.ci.Planner.Digest},
		"check_every": "24h",
	}}

	for _, r := range g.spec.Runs {
		name, t := r.Name, r.Trigger

		switch t.Kind {
		case "pull_requests":
			every := sweepEvery
			if t.SweepEvery != "" {
				every = t.SweepEvery
			}

			if t.Connector != "" {
				out = append(out, g.plannedMock(name))
			} else {
				out = append(out, g.pullRequests(name, t), g.planned(name, t))
			}

			out = append(out, M{"name": sweepTimer(name), "type": "time", "source": M{"interval": every}})
			// A proposed change's prepare command runs in its base's tree.
			branches[t.Base] = true
		case "timer":
			src := M{"start": t.Timer.Start, "stop": t.Timer.Stop}
			if t.Timer.Location != "" {
				src["location"] = t.Timer.Location
			}

			if len(t.Timer.Days) > 0 {
				src["days"] = t.Timer.Days
			}

			out = append(out, M{"name": triggerResource(name), "type": "time", "source": src})
			branches[t.Branch] = true
		case triggerContinuous:
			// Put only when the build planned, so an exited build starts no suite.
			out = append(out, g.plannedMock(name))
			branches[t.Branch] = true
		default:
			branches[t.Branch] = true
		}
	}

	for _, b := range slices.Sorted(maps.Keys(branches)) {
		src := maps.Clone(g.ci.Repository)
		src["branch"] = b
		out = append(out, M{"name": branchResource(b), "type": "git", "source": src})
	}

	src := maps.Clone(g.ci.Repository)
	src["branch"] = g.spec.Branch
	src["paths"] = g.configPaths()

	return append(out, M{"name": "config", "type": "git", "source": src})
}

func (g *gen) pullRequests(run string, pr plugin.SpecTrigger) M {
	src := M{
		"repository":    g.spec.GitHub.Repository,
		"access_token":  "((" + g.tokenVar(plugin.PurposeChanges) + "))",
		"base_branch":   pr.Base,
		"disable_forks": true,
	}

	if pr.Draft != nil && !*pr.Draft {
		src["ignore_drafts"] = true
	}

	if len(pr.Labels) > 0 {
		src["labels"] = pr.Labels
	}

	return M{"name": triggerResource(run), "type": "github-pr", "source": src, "check_every": "1m"}
}

// planned is a pull request run's hand-off, never checked: the pull request
// check reorders versions it has seen, so suite jobs take the planning
// job's puts in the order they land instead. Its ignore_paths, which only a
// check reads, keeps its source apart from every other's, since Concourse
// shares versions between equal sources.
func (g *gen) planned(run string, pr plugin.SpecTrigger) M {
	src := M{
		"repository":   g.spec.GitHub.Repository,
		"access_token": "((" + g.tokenVar(plugin.PurposeChanges) + "))",
		"base_branch":  pr.Base,
		"ignore_paths": []string{"pipelinegen hand-off of " + g.spec.Pipeline + " " + run},
	}

	return M{"name": plannedResource(run), "type": "github-pr", "source": src, "check_every": "never"}
}

// byConnector runs have no pull request resource or planning job: the
// sweep plans each change and hands it on.
func (g *gen) byConnector(run string) bool { return g.runs[run].Trigger.Connector != "" }

const mockType = "mock"

// plannedMock is a connector run's hand-off. mock refuses unknown source
// keys, so the log line is what keeps two hand-offs' versions apart.
func (g *gen) plannedMock(run string) M {
	return M{"name": plannedResource(run), "type": mockType, "check_every": "never", "source": M{
		"no_initial_version": true,
		"log":                "pipelinegen hand-off: " + g.spec.Pipeline + " " + run + " planned",
	}}
}

// trigger is what starts a run's jobs; exactly one of a job's gets triggers.
func (g *gen) trigger(run string, tags []string, passed []string) []any {
	t := g.runs[run].Trigger

	get := func(name, resource string, trigger bool) M {
		step := M{"get": name, "trigger": trigger}
		if name != resource {
			step["resource"] = resource
		}

		if len(passed) > 0 {
			step["passed"] = passed
		}

		return withTags(step, tags)
	}

	if t.Kind == "pull_requests" && t.Connector != "" {
		return []any{withTags(M{"get": "handoff", "resource": plannedResource(run), "trigger": true, "version": "every"}, tags)}
	}

	switch t.Kind {
	case "pull_requests":
		step := get("trigger", triggerResource(run), true)
		step["version"] = "every"
		// pipelinegen merges the head into the target's tip itself.
		step["params"] = M{"integration_tool": "checkout"}

		// Not through passed, which would order hand-offs by the planning
		// builds' start.
		if len(passed) > 0 {
			delete(step, "passed")
			step["resource"] = plannedResource(run)
		}

		return []any{step}
	case "timer":
		// Follow the planning job, not the timer: a round that plans nothing
		// passes no commit, so no suite runs. The suites take the newest
		// commit it passed, never every one: the planning job takes only the
		// newest, so a commit between two rounds never passes, and a suite
		// waiting for each in turn would wait for it for good.
		if len(passed) > 0 {
			return []any{get("trigger", branchResource(t.Branch), true)}
		}

		return []any{get(triggerResource(run), triggerResource(run), true), get("trigger", branchResource(t.Branch), false)}
	case "manual":
		return []any{get("trigger", branchResource(t.Branch), len(passed) > 0)}
	case triggerContinuous:
		if len(passed) == 0 {
			step := get("trigger", branchResource(t.Branch), true)
			step["version"] = "every"

			return []any{step}
		}

		// Only the hand-off triggers. Inputs passed through one job come from
		// one of its builds, so the branch get takes that build's commit.
		handoff := get("handoff", plannedResource(run), true)
		handoff["version"] = "every"

		return []any{handoff, get("trigger", branchResource(t.Branch), false)}
	default:
		// Every commit, in order, in the planning job and in the suites: a
		// suite takes each planned commit in turn, so one the planning job
		// skipped would hold every later commit back for good.
		step := get("trigger", branchResource(t.Branch), true)
		step["version"] = "every"

		return []any{step}
	}
}

func (g *gen) jobs() ([]any, error) {
	jobs := []any{g.pipelineJob()}

	for _, r := range g.spec.Runs {
		// A connector's changes are planned by the sweep alone.
		if !g.byConnector(r.Name) {
			jobs = append(jobs, g.planJob(r.Name))
		}

		if r.Trigger.Kind == "pull_requests" {
			jobs = append(jobs, g.sweepJob(r.Name))
		}

		for _, rs := range r.Suites {
			job, err := g.suiteJob(r.Name, rs.Suite)
			if err != nil {
				return nil, err
			}

			jobs = append(jobs, job)
		}
	}

	return jobs, nil
}

func (g *gen) gets(tags []string, inputs []any, minutes int) M {
	return timeout(M{"in_parallel": append(slices.Clone(inputs), withTags(M{"get": "planner"}, tags))}, minutes)
}

func (g *gen) bootstrapEnv() M {
	pg, bz := tool(g.spec, plugin.ToolPipelinegen), tool(g.spec, plugin.ToolBazelisk)

	env := M{
		"PIPELINEGEN_TOOL_REPOSITORY":  pg.Repository,
		"PIPELINEGEN_TOOL_COMMIT":      pg.Commit,
		"PIPELINEGEN_TOOL_VERSION":     pg.Version,
		"PIPELINEGEN_TOOL_SHA256":      pg.SHA256,
		"PIPELINEGEN_BAZELISK_VERSION": bz.Version,
		"PIPELINEGEN_BAZELISK_SHA256":  bz.SHA256,
		"PIPELINEGEN_PLUGINS":          pluginLines(g.spec),
		"PIPELINEGEN_PRIVATE":          privateModules(g.spec),
	}

	if pg.Sums != "" {
		env["PIPELINEGEN_TOOL_SUMS"] = pg.Sums
	}

	maps.Copy(env, g.tokens(plugin.PurposeRead))
	maps.Copy(env, g.runnerEnv())

	return env
}

// defaultRunner names the runner when the spec pins none, as a spec written
// by hand may not; the bootstrap then says the pipeline pins none.
const defaultRunner = "concourse-runner"

// runnerEnv is the bootstrap's pin of the runner, which the config's
// plugins name, as the tool fetches it: the bootstrap reads GitHub's
// owner/name, and a runner in a directory its path, which its tags and its
// build start from.
func (g *gen) runnerEnv() M {
	r := g.runnerTool()

	name, program := r.Name, plugin.Program(r.Repository)
	if r.Path != "" {
		program = pin.Pin{Path: r.Path}.ProgramName()
	}

	if name == "" {
		name, program = defaultRunner, defaultRunner
	}

	env := M{
		"PIPELINEGEN_RUNNER": name, "PIPELINEGEN_RUNNER_PROGRAM": program, "PIPELINEGEN_RUNNER_REPOSITORY": gitHubProject(r.Repository),
		"PIPELINEGEN_RUNNER_VERSION": r.Version, "PIPELINEGEN_RUNNER_SHA256": r.SHA256, "PIPELINEGEN_RUNNER_COMMIT": r.Commit,
	}

	if r.Path != "" {
		env["PIPELINEGEN_RUNNER_PATH"] = r.Path
	}

	if r.Sums != "" {
		env["PIPELINEGEN_RUNNER_SUMS"] = r.Sums
	}

	return env
}

// gitHubProject is a repository on github.com as owner/name, whatever URL
// the config gives.
func gitHubProject(repo string) string {
	if r, err := pin.ParseRepository(repo, nil); err == nil && r.Forge == pin.GitHub {
		return r.Project
	}

	return repo
}

// ErrRunnerPin wraps a pin of the runner the bootstrap can't fetch.
var ErrRunnerPin = errors.New("runner pin")

// checkRunnerPin refuses a runner the bootstrap can't fetch: it fetches
// from github.com alone.
func checkRunnerPin(spec plugin.Spec) error {
	r := ownRunner(spec)
	if r.Repository == "" {
		return nil
	}

	repo, err := pin.ParseRepository(r.Repository, nil)
	if err != nil {
		return fmt.Errorf("%w: plugins.%s: %w", ErrRunnerPin, r.Name, err)
	}

	if repo.Forge != pin.GitHub || repo.Origin != "https://github.com" {
		return fmt.Errorf("%w: plugins.%s: the bootstrap fetches the runner from github.com, not %s", ErrRunnerPin, r.Name, r.Repository)
	}

	if path.IsAbs(r.Path) || slices.Contains(strings.Split(r.Path, "/"), "..") {
		return fmt.Errorf("%w: plugins.%s: path %s leaves the repository", ErrRunnerPin, r.Name, r.Path)
	}

	return nil
}

func (g *gen) runnerTool() plugin.SpecTool { return ownRunner(g.spec) }

// toolTask keeps a job's bookkeeping in one container, so it waits for a
// worker once. It holds the pipeline's token and runs no test.
func (g *gen) toolTask(name string, tags []string, inputs []M, outputs []string, cacheDirs []string, env M, step ...string) M {
	params := g.bootstrapEnv()
	maps.Copy(params, env)
	params["PIPELINEGEN_BAZELISK"] = "tools/bazelisk"
	g.forTool(params)

	if slices.ContainsFunc(inputs, func(in M) bool { return in["name"] == "trigger" }) {
		params["PIPELINEGEN_TRIGGER"] = "trigger"
	}

	cfg := M{
		"platform": "linux",
		"inputs":   inputs,
		"caches":   caches(append([]string{"cache"}, cacheDirs...)...),
		"params":   params,
		"run":      runStep(step...),
	}

	if !slices.Contains(outputs, "tools") {
		outputs = append([]string{"tools"}, outputs...)
	}

	cfg["outputs"] = names(outputs...)

	return withTags(M{"task": name, "image": "planner", "config": cfg}, tags)
}

// pluginLines is the bootstrap's input format: one plugin per line, "-" for
// an empty field, and a path and sums only where a tool has them. Another runner's
// pipeline holds the runs it owns, so this one fetches no other runner.
func pluginLines(s plugin.Spec) string {
	var b strings.Builder

	for _, t := range plugins(s) {
		fields := []string{t.Name, t.Repository, t.Version, t.SHA256, t.Commit}
		if t.Path != "" || t.Sums != "" {
			fields = append(fields, t.Path)
		}

		if t.Sums != "" {
			fields = append(fields, t.Sums)
		}

		for i, f := range fields {
			if f == "" {
				fields[i] = "-"
			}
		}

		b.WriteString(strings.Join(fields, " ") + "\n")
	}

	return b.String()
}

func privateModules(s plugin.Spec) string {
	var mods []string

	for _, t := range plugins(s) {
		if t.Commit != "" {
			mods = append(mods, runstep.ModulePath(t.Repository))
		}
	}

	return strings.Join(mods, ",")
}

// plugins are the tools the steps fetch besides pipelinegen and bazelisk:
// every plugin but another runner.
func plugins(s plugin.Spec) []plugin.SpecTool {
	own := ownRunner(s).Name

	var out []plugin.SpecTool

	for _, t := range s.Tools {
		switch {
		case t.Kind == plugin.ToolPipelinegen || t.Kind == plugin.ToolBazelisk:
		case t.Kind == plugin.KindRunner && t.Name != own:
		default:
			out = append(out, t)
		}
	}

	return out
}

// forTool marks every pipelinegen step as a pipeline's, so its Bazel output
// stays in the step's cache.
func (g *gen) forTool(env M) {
	env[plugin.EnvPipelineStep] = "1"
	g.withTrusted(env)
}

// withTrusted gives only pipelinegen's own steps the trusted keys: in a
// proposed run the repository's scripts are the change's and could rewrite
// them.
func (g *gen) withTrusted(env M) {
	if g.spec.Trusted != "" {
		env[plugin.EnvTrusted] = g.spec.Trusted
	}
}

// helper is told where bazelisk is: the planner image has none on its PATH.
func (g *gen) helper(name string, tags []string, inputs []M, outputs []string, env M, args ...string) M {
	env = maps.Clone(env)
	if env == nil {
		env = M{}
	}

	env["PIPELINEGEN_BAZELISK"] = "tools/bazelisk"
	g.forTool(env)

	if slices.ContainsFunc(inputs, func(in M) bool { return in["name"] == "trigger" }) {
		env["PIPELINEGEN_TRIGGER"] = "trigger"
	}

	cfg := M{
		"platform": "linux",
		"inputs":   append(names("tools"), inputs...),
		"params":   env,
		"run":      M{"path": "tools/pipelinegen", "args": args},
	}

	if len(outputs) > 0 {
		cfg["outputs"] = names(outputs...)
	}

	return withTags(M{"task": name, "image": "planner", "config": cfg}, tags)
}

// env is for pipelinegen's own steps: they hold the pipeline's tokens and
// none of the repository's params or secrets.
func (g *gen) env(run, suite string, extra M) M {
	env := M{"PIPELINEGEN_RUN": run}
	maps.Copy(env, g.tokens(allPurposes...))

	if suite != "" {
		env["PIPELINEGEN_SUITE"] = suite
		env["PIPELINEGEN_JOB_URL"] = g.jobURL(SuiteJob(run, suite))
	}

	maps.Copy(env, extra)

	return env
}

// concourseURL is for a config that keeps Concourse's address out of the
// repository's files. Only the pipeline job needs it.
func (g *gen) concourseURL() M {
	if g.ci.URL == "" {
		return nil
	}

	return M{"PIPELINEGEN_CONCOURSE_URL": g.ci.URL}
}

// taskEnv sets pipelinegen's variables last, so nothing overrides them. A
// task gets the pipeline's token only as a declared secret, since every
// command in it sees what it gets.
func (g *gen) taskEnv(run, suite string, secrets map[string]string, own M) M {
	env := M{}
	for k, v := range g.runSuite(run, suite).Params {
		env[k] = v
	}

	for k, v := range secrets {
		env[k] = "((" + v + "))"
	}

	env["PIPELINEGEN_RUN"] = run
	env["PIPELINEGEN_SUITE"] = suite
	maps.Copy(env, own)

	return env
}

// pipelineJob sets the pipeline with no variables: its ((name)) references
// are left for Concourse's credential manager, so no value is written into
// the pipeline or shown in a diff. Without self_set it only fails when the
// pipeline set is stale.
func (g *gen) pipelineJob() M {
	tags := g.place.tags(forPipeline, "", "")

	repo := withTags(M{"get": "repo", "resource": "config", "trigger": true}, tags)
	plan := []any{g.gets(tags, []any{repo}, g.stepMinutes())}

	// Builds run side by side: one whose config isn't the newest opens no
	// gate, so an older pipeline is never set over a newer one.
	if g.ci.SelfSet {
		plan = append(plan,
			limited(g.toolTask("pipeline", tags, names("repo"), []string{"pipeline"}, nil, g.concourseURL(), g.forRunner("pipeline")...), stepsKey, g.stepMinutes()),
			M{"load_var": "newest", "file": "pipeline/gate.json"},
			M{"across": []any{M{"var": "newest_config", "values": "((.:newest))"}}, "do": []any{
				timeout(M{"set_pipeline": "self", "file": "pipeline/pipeline.yml"}, g.stepMinutes()),
			}},
		)

		return M{"name": "pipeline", "plan": plan}
	}

	env := M{staleHash: "", "PIPELINEGEN_PIPELINE": g.spec.Pipeline}
	maps.Copy(env, g.concourseURL())

	stale := limited(g.toolTask("stale", tags, names("repo"), []string{"newest"}, nil, env, g.forRunner("stale")...), stepsKey, g.stepMinutes())
	g.staleParams = asParams(stale)
	plan = append(plan, stale)

	return M{"name": "pipeline", "plan": plan}
}

// forRunner is a step that writes the pipeline: with several runners, this
// runner's, which pipelinegen writes when told its name.
func (g *gen) forRunner(step string) []string {
	if g.spec.Runner == "" {
		return []string{step}
	}

	return []string{step, "--for", g.spec.Runner}
}

// sealStale hashes the pipeline's text with the param empty, as the check
// computes it.
func (g *gen) sealStale(p M) error {
	if g.staleParams == nil {
		return nil
	}

	text, err := Text(p)
	if err != nil {
		return err
	}

	sum := sha256.Sum256(text)
	g.staleParams[staleHash] = hex.EncodeToString(sum[:])

	return nil
}

func (g *gen) configPaths() []string {
	return slices.Clone(g.spec.ConfigFiles)
}

func (g *gen) planJob(run string) M {
	tags := g.place.tags(forPlanning, run, "")

	if g.runs[run].Trigger.Kind == "pull_requests" {
		return g.proposedPlanJob(run)
	}

	// One task, with the plan as its last command, so no command follows a
	// Bazel server it leaves.
	gets := g.gets(tags, g.trigger(run, tags, nil), g.planMinutes())
	env := g.plannerEnv()

	step, outputs := []string{"plan"}, []string(nil)

	continuous := g.runs[run].Trigger.Kind == triggerContinuous
	if continuous {
		step, outputs = []string{"continuous-plan", "--plan-minutes", strconv.Itoa(g.planMinutes())}, []string{"handoff"}
	}

	task := limited(g.toolTask("plan", tags, names("trigger"), outputs, []string{"plan-cache"}, g.env(run, "", env), step...),
		planKey, g.planMinutes()+g.runs[run].WaitMinutes)

	// The whole plan task runs in the prepare image, since a separate task
	// couldn't share the plan's cache.
	if img := g.ci.Planner.PrepareImage; img != "" {
		gets = timeout(M{"in_parallel": append(g.trigger(run, tags, nil), withTags(M{"get": img}, tags))}, g.planMinutes())
		task["image"] = img
	}

	job := M{"name": planJob(run), "plan": []any{gets, task}}

	if continuous {
		put := withTags(M{"put": plannedResource(run), "inputs": []string{"handoff"}, "no_get": true, "params": M{"file": "handoff/version"}}, tags)
		job["plan"] = []any{gets, task,
			M{"load_var": "planned", "file": "handoff/gate.json"},
			M{"across": []any{M{"var": "planned_round", "values": "((.:planned))"}}, "do": []any{timeout(put, g.planMinutes())}},
		}
	}

	return job
}

// With a token per purpose, pipelinegen's steps never read GITHUB_TOKEN,
// which a repository task may hold with less right.
func (g *gen) split() bool { return len(g.spec.GitHub.Tokens) > 0 }

func (g *gen) tokenVar(purpose string) string {
	if v := g.spec.GitHub.Tokens[purpose]; v != "" {
		return v
	}

	return g.spec.GitHub.TokenVar
}

func purposeEnv(purpose string) string {
	return "PIPELINEGEN_" + strings.ToUpper(purpose) + "_TOKEN"
}

// allPurposes may be held only by a step that runs none of a change's code.
var allPurposes = plugin.Purposes

func (g *gen) tokens(purposes ...string) M {
	env := M{}

	if !g.split() {
		env["GITHUB_TOKEN"] = g.token()
	} else {
		for _, p := range purposes {
			env[purposeEnv(p)] = "((" + g.tokenVar(p) + "))"
		}
	}

	for _, p := range purposes {
		if v := g.spec.SSHKeys[p]; v != "" {
			env[sshKeyEnv(p)] = "((" + v + "))"
		}
	}

	return env
}

func sshKeyEnv(purpose string) string {
	return "PIPELINEGEN_SSH_KEY_" + strings.ToUpper(purpose)
}

// untrustedPurposes are all a task that runs a proposed change's code holds.
var untrustedPurposes = []string{plugin.PurposeRead}

// untrusted is for a step that runs a proposed change's code: the planner's
// secrets serve the base's tree, and its tokens only read.
func (g *gen) untrusted(run string) M {
	env := M{"PIPELINEGEN_RUN": run}
	maps.Copy(env, g.plannerEnv())
	maps.Copy(env, g.tokens(untrustedPurposes...))

	return env
}

// trusted is for a step that writes and runs none of the change's code.
func (g *gen) trusted(run string) M { return g.env(run, "", nil) }

func (g *gen) baseGet(run string, tags []string) M {
	return withTags(M{"get": "base", "resource": branchResource(g.runs[run].Trigger.Base)}, tags)
}

// proposedPlanJob plans in two tasks so the change's code never runs where
// a credential that writes is: plan holds the read token alone, and write
// runs none of the change's code.
func (g *gen) proposedPlanJob(run string) M {
	tags := g.place.tags(forPlanning, run, "")
	inputs := append(g.trigger(run, tags, nil), g.baseGet(run, tags))

	// No token that reports.
	beginEnv := M{"PIPELINEGEN_RUN": run}
	maps.Copy(beginEnv, g.tokens(plugin.PurposeRead, plugin.PurposeStore))

	begin := limited(g.toolTask("begin", tags, names("trigger"), nil, []string{"write-cache"},
		beginEnv, "begin"), stepsKey, g.stepMinutes())
	plan := limited(g.toolTask("plan", tags, names("trigger", "base"), []string{"plan"}, []string{"plan-cache"},
		g.untrusted(run), "proposed-plan"), planKey, g.planMinutes())
	write := limited(g.toolTask("write", tags, names("trigger", "plan"), []string{"handoff"}, []string{"write-cache"},
		g.trusted(run), "proposed-write"), planKey, g.planMinutes())

	gets := g.gets(tags, inputs, g.planMinutes())

	if img := g.ci.Planner.PrepareImage; img != "" {
		gets = timeout(M{"in_parallel": append(inputs, withTags(M{"get": img}, tags))}, g.planMinutes())
		plan["image"] = img
	}

	return M{"name": planJob(run), "plan": []any{gets, begin, plan, write,
		M{"load_var": "planned", "file": "handoff/gate.json"},
		M{"across": []any{M{"var": "planned_round", "values": "((.:planned))"}}, "do": []any{timeout(g.putHandOff(run, tags, "handoff", "handoff"), g.planMinutes())}},
	}}
}

// putHandOff skips the put's own get, which would fetch the head again for
// nothing.
func (g *gen) putHandOff(run string, tags []string, input, dir string) M {
	// A connector's run.json is the mock version's string.
	params := M{"path": dir}
	if g.byConnector(run) {
		params = M{"file": dir + "/run.json"}
	}

	return withTags(M{
		"put": plannedResource(run), "inputs": []string{input}, "no_get": true,
		"params": params,
	}, tags)
}

func (g *gen) sweepJob(run string) M {
	tags := g.place.tags(forPlanning, run, "")

	inputs := []any{
		withTags(M{"get": sweepTimer(run), "trigger": true}, tags),
		// A hand-off means a push, so a missed head waits at most until the
		// next push.
		withTags(M{"get": "planned", "resource": plannedResource(run), "trigger": true, "params": M{"skip_download": true}}, tags),
		withTags(M{"get": "config"}, tags),
		g.baseGet(run, tags),
	}

	// A connector's own sweep puts the hand-offs, so only its timer triggers it.
	if g.byConnector(run) {
		inputs = slices.Delete(inputs, 1, 2)
	}

	gets := g.gets(tags, inputs, g.planMinutes())

	list := limited(g.toolTask("sweep", tags, names("config"), []string{"sweep", "gate"}, []string{"write-cache"},
		g.trusted(run), "sweep"), planKey, g.planMinutes())
	plan := limited(g.toolTask("plan", tags, names("sweep", "base"), []string{"plans"}, []string{"plan-cache"},
		g.untrusted(run), "sweep-plan"), planKey, g.planMinutes())
	write := limited(g.toolTask("write", tags, names("sweep", "plans"), []string{"handoffs"}, []string{"write-cache"},
		g.trusted(run), g.sweepWrite(run)...), planKey, g.planMinutes())

	if img := g.ci.Planner.PrepareImage; img != "" {
		gets = timeout(M{"in_parallel": append(inputs, withTags(M{"get": img}, tags))}, g.planMinutes())
		plan["image"] = img
	}

	put := timeout(g.putHandOff(run, tags, "handoffs", "handoffs/((.:handoff))"), g.planMinutes())

	work := []any{
		plan, write,
		M{"load_var": "handoffs", "file": "handoffs/list.json"},
		M{"across": []any{M{"var": "handoff", "values": "((.:handoffs))"}}, "do": []any{put}},
	}

	// Sweeps run side by side: of two that take one head up, the first mark
	// written stands and the other hands nothing on.
	return M{
		"name": sweepJob(run),
		"plan": []any{
			gets, list,
			M{"load_var": "gate", "file": "gate/gate.json"},
			M{"across": []any{M{"var": "taken", "values": "((.:gate))"}}, "do": []any{M{"do": work}}},
		},
	}
}

// plannerEnv lists the planner's own names in PIPELINEGEN_PLANNER_ENV so the
// script can keep them away from the plan itself.
func (g *gen) plannerEnv() M {
	pl := g.ci.Planner
	env := M{"PIPELINEGEN_PREPARE": pl.Prepare}

	for k, v := range pl.Params {
		env[k] = v
	}

	for k, v := range pl.Secrets {
		env[k] = "((" + v + "))"
	}

	own := slices.Sorted(maps.Keys(pl.Params))
	own = append(own, slices.Sorted(maps.Keys(pl.Secrets))...)

	if len(own) > 0 {
		env["PIPELINEGEN_PLANNER_ENV"] = strings.Join(own, " ")
	}

	if pl.GitToken != "" {
		env["PIPELINEGEN_GIT_TOKEN"] = pl.GitToken
	}

	return env
}

func (g *gen) suiteJob(run, suite string) (M, error) {
	tags := g.suiteTags(run, suite)

	// A cached image is fetched by a task, never a get: a get is the job's
	// input, and a job would wait for an image the registry hasn't got.
	work, err := g.suiteImages(run, suite)
	if err != nil {
		return nil, err
	}

	s := g.suites[suite]

	// Fetched inside the gated work, so a build that runs nothing fetches
	// nothing.
	if s.Image != "" && s.Image != plannerImage && g.cachedKey(suite, s.Image) == "" {
		work = append(work, timeout(withTags(M{"get": s.Image}, tags), g.stepMinutes()))
	}

	if st := s.Steps; st != nil && st.Prepare && st.PrepareImage != "" && st.PrepareImage != s.Image && g.cachedKey(suite, st.PrepareImage) == "" {
		work = append(work, timeout(withTags(M{"get": st.PrepareImage}, tags), g.stepMinutes()))
	}

	work = append(work, g.prepare(run, suite)...)
	work = append(work,
		M{
			// every batch at once: the batches step deals at most as many as
			// should run together, and across alone runs one at a time
			"across":    []any{M{"var": "batch", "values": "((.:batches))", "max_in_flight": "all"}},
			"fail_fast": false,
			"do":        []any{g.batchStep(run, suite)},
		},
	)

	for i, w := range s.WholeSuite {
		work = append(work, g.wholeSuiteSteps(run, suite, i, w)...)
	}

	body := M{"do": work}

	gets := g.gets(tags, append(g.trigger(run, tags, []string{planJob(run)}), g.bookkeepingGets(run, suite)...), g.stepMinutes())

	// A suite whose image is the planner's fetches it once, for its tasks and
	// pipelinegen's alike.
	if from, ok := g.plannerFrom[suite]; ok {
		if all, ok := gets["in_parallel"].([]any); ok {
			for _, get := range all {
				if m, ok := get.(M); ok && m["get"] == plannerImage {
					m["resource"] = from
				}
			}
		}
	}

	plan := []any{gets}

	if g.byConnector(run) {
		plan = append(plan, timeout(handOffTrigger(tags), g.stepMinutes()))
	}

	plan = append(plan,
		limited(g.toolTask("start", tags, names("trigger"), []string{"tools", "source", "gate", "batches"}, nil,
			g.env(run, suite, nil), "start"), stepsKey, g.stepMinutes()),
		M{"load_var": "gate", "file": "gate/gate.json"},
		M{"load_var": "batches", "file": "batches/batches.json"},
		// Without a plan for this suite the gate is empty and nothing runs.
		M{"across": []any{M{"var": "planned", "values": "((.:gate))"}}, "do": []any{body}},
	)

	// These hooks report a build that broke or was aborted, or post its
	// statuses as errored when it had no tools yet. The ensure runs after
	// them, on an abort too, and fetches missing tools to end the suite.
	fallback := timeout(g.fallback(run, suite, tags), g.stepMinutes())

	job := M{
		"name":     SuiteJob(run, suite),
		"plan":     plan,
		"on_error": fallback,
		"on_abort": fallback,
		"ensure":   g.endTask(run, suite),
	}

	if g.byConnector(run) {
		delete(job, "on_error")
		delete(job, "on_abort")
	}

	if s.SerialGroup != "" {
		job["serial_groups"] = []string{g.serialGroup(s.SerialGroup, run)}
	}

	if s.MaxInFlight > 0 {
		job["max_in_flight"] = s.MaxInFlight
	}

	return job, nil
}

const (
	planKey  = "limits.plan"
	stepsKey = "limits.steps"
)

func (g *gen) serialGroup(group, run string) string {
	if g.ci.SerialGroups[group].PerRun {
		return group + "-" + run
	}

	return group
}

// endTask holds the bootstrap's pins too: a build that broke before its
// start task had the tools fetches them here.
func (g *gen) endTask(run, suite string) M {
	env := g.bootstrapEnv()
	maps.Copy(env, g.env(run, suite, nil))
	env["PIPELINEGEN_BAZELISK"] = "tools/bazelisk"
	env["PIPELINEGEN_TRIGGER"] = "trigger"
	g.forTool(env)

	// Optional inputs instead of a try, which would let an end that could not
	// be recorded pass.
	inputs := []M{{"name": "tools", "optional": true}, {"name": "trigger", "optional": true}, {"name": "batches", "optional": true}}

	return g.onBookkeeping(limited(withTags(M{"task": "end", "image": "planner", "config": M{
		"platform": "linux",
		"inputs":   inputs,
		"params":   env,
		"run":      runStep("end"),
	}}, g.bookkeepingTags(run, suite)), stepsKey, g.stepMinutes()), run, suite)
}

const (
	triggerBookkeeping = "trigger-bookkeeping"
	plannerBookkeeping = "planner-bookkeeping"
)

// Bookkeeping on other workers fetches its own inputs: a volume from another
// worker may stream through the web node, which takes most of an hour for a
// gigabyte image.
func (g *gen) bookkeepingApart(run, suite string) ([]string, bool) {
	tags := g.bookkeepingTags(run, suite)

	return tags, !slices.Equal(tags, g.suiteTags(run, suite))
}

func (g *gen) bookkeepingGets(run, suite string) []any {
	tags, apart := g.bookkeepingApart(run, suite)
	if !apart {
		return nil
	}

	var out []any

	for _, step := range g.trigger(run, tags, []string{planJob(run)}) {
		get, ok := step.(M)
		if !ok || get["get"] != "trigger" {
			continue
		}

		get = maps.Clone(get)
		if _, ok := get["resource"]; !ok {
			get["resource"] = get["get"]
		}

		get["get"] = triggerBookkeeping
		get["trigger"] = false
		out = append(out, get)
	}

	return append(out, withTags(M{"get": plannerBookkeeping, "resource": "planner"}, tags))
}

func (g *gen) onBookkeeping(task M, run, suite string) M {
	if _, apart := g.bookkeepingApart(run, suite); apart {
		task["image"] = plannerBookkeeping
		task["input_mapping"] = M{"trigger": triggerBookkeeping}
	}

	return task
}

func (g *gen) bookkeepingTags(run, suite string) []string {
	return g.place.tags(forBookkeeping, run, suite)
}

func (g *gen) suiteTags(run, suite string) []string {
	return g.place.tags(forSuite, run, suite)
}

func asParams(task M) M {
	if cfg, ok := task["config"].(M); ok {
		if p, ok := cfg["params"].(M); ok {
			return p
		}
	}

	return M{}
}

// fallback reports through pipelinegen when the build got its tools and
// trigger, so the run's round can end; otherwise the runner posts the
// statuses as errored. It holds no more than the end task, and the
// runner's pin, as it may run before any task fetched the runner.
func (g *gen) fallback(run, suite string, tags []string) M {
	env := g.env(run, suite, M{
		"PIPELINEGEN_BAZELISK": "tools/bazelisk",
		"PIPELINEGEN_TRIGGER":  "trigger",
		"REPOSITORY":           g.spec.GitHub.Repository,
		"SUITE_CONTEXT":        g.spec.GitHub.StatusPrefix + "/" + run + "/" + suite,
		"RUN_CONTEXT":          g.spec.GitHub.StatusPrefix + "/" + run,
		"TARGET_URL":           g.jobURL(SuiteJob(run, suite)),
	})
	maps.Copy(env, g.runnerEnv())
	g.forTool(env)

	return withTags(M{"task": "report-error", "config": M{
		"platform":       "linux",
		"image_resource": M{"type": "registry-image", "source": g.ci.Planner.Image, "version": M{"digest": g.ci.Planner.Digest}},
		"inputs":         []M{{"name": "tools", "optional": true}, {"name": "trigger", "optional": true}, {"name": "batches", "optional": true}},
		"params":         env,
		"run":            runStep("report-error"),
	}}, tags)
}

func (g *gen) prepare(run, suite string) []any {
	s := g.suites[suite]
	if s.Steps != nil {
		return g.prepareSteps(run, suite)
	}

	if s.Prepare == "" {
		return nil
	}

	// A prepare may build for coverage by it.
	rs := g.runSuite(run, suite)
	params := g.taskEnv(run, suite, rs.Secrets.Prepare, M{"PIPELINEGEN_RECORD": strconv.FormatBool(rs.Records)})

	return []any{timeout(withImage(privileged(withTags(M{
		"task":   "prepare",
		"file":   "source/" + s.Prepare,
		"params": params,
	}, g.suiteTags(run, suite)), s.Privileged), g.taskImage(suite, s.Image)), g.suiteMinutes(run, suite))}
}

func (g *gen) batchStep(run, suite string) M {
	s := g.suites[suite]
	if s.Steps != nil {
		return g.drivenBatch(run, suite)
	}

	rs := g.runSuite(run, suite)
	// No token: the slice commands only journal, and the ensure writes it.
	params := g.taskEnv(run, suite, rs.Secrets.Batch, M{
		"PIPELINEGEN_BATCH":    "((.:batch))",
		"PIPELINEGEN_RECORD":   strconv.FormatBool(rs.Records),
		"PIPELINEGEN_RUN_FILE": "batches/run.json",
		"PIPELINEGEN_JOB_URL":  g.jobURL(SuiteJob(run, suite)),
	})

	task := timeout(withImage(privileged(withTags(M{
		"task":   "batch",
		"file":   "source/" + s.RunSlice,
		"params": params,
	}, g.suiteTags(run, suite)), s.Privileged), g.taskImage(suite, s.Image)), g.suiteMinutes(run, suite))

	return g.withBatchEnd(run, suite, task)
}

func (g *gen) withBatchEnd(run, suite string, task M) M {
	rs := g.runSuite(run, suite)

	// The task may have died before writing result or coverage.
	inputs := append(names("source", "trigger", "batches"), M{"name": "result", "optional": true})
	if rs.Records {
		inputs = append(inputs, M{"name": "coverage", "optional": true})
	}

	args := []string{"report", "batch-end", "--batch", "((.:batch))", "--result", "result", "--root", "source"}

	task["ensure"] = g.onBookkeeping(limited(g.helper("batch-end", g.bookkeepingTags(run, suite), inputs, nil,
		g.env(run, suite, M{"PIPELINEGEN_BATCH": "((.:batch))"}), args...), stepsKey, g.stepMinutes()), run, suite)

	return task
}

// prepareSteps gets the trusted keys and the trigger, to read the suite's
// steps in a proposed run, but no token.
func (g *gen) prepareSteps(run, suite string) []any {
	s := g.suites[suite]
	if !s.Steps.Prepare {
		return nil
	}

	rs := g.runSuite(run, suite)
	params := g.taskEnv(run, suite, rs.Secrets.Prepare, M{
		"PIPELINEGEN_RECORD":  strconv.FormatBool(rs.Records),
		"PIPELINEGEN_TRIGGER": "trigger",
	})
	g.forTool(params)

	image := s.Steps.PrepareImage
	if image == "" {
		image = s.Image
	}

	cfg := M{
		"platform": "linux",
		"inputs":   names("source", "trigger", "tools"),
		"outputs":  names("prepared", "work"),
		"caches":   caches("cache"),
		"params":   params,
		"run":      M{"path": "tools/pipelinegen", "args": []string{"slice", "prepare", "--root", "source", "--tools", "tools"}},
	}

	return []any{timeout(withImage(privileged(withTags(M{"task": "prepare", "config": cfg}, g.suiteTags(run, suite)), s.Privileged), g.taskImage(suite, image)), g.suiteMinutes(run, suite))}
}

// drivenBatch runs the tests, so it gets no token; it gets the trusted keys
// to read the suite's steps from, and passes them to no driver.
func (g *gen) drivenBatch(run, suite string) M {
	s := g.suites[suite]
	rs := g.runSuite(run, suite)

	params := g.taskEnv(run, suite, rs.Secrets.Batch, M{
		"PIPELINEGEN_BATCH":    "((.:batch))",
		"PIPELINEGEN_RECORD":   strconv.FormatBool(rs.Records),
		"PIPELINEGEN_RUN_FILE": "batches/run.json",
		"PIPELINEGEN_JOB_URL":  g.jobURL(SuiteJob(run, suite)),
	})
	g.forTool(params)

	// the bootstrap's tools come first on the drivers' PATH
	inputs := names("source", "batches", "tools")
	args := []string{"slice", "run", "--root", "source", "--tools", "tools"}

	if s.Steps.Prepare {
		inputs = append(inputs, M{"name": "prepared"})
		args = append(args, "--prepared", "prepared")
	}

	cfg := M{
		"platform": "linux",
		"inputs":   inputs,
		"outputs":  names("result", "coverage", "work"),
		"caches":   caches("cache"),
		"params":   params,
		"run":      M{"path": "tools/pipelinegen", "args": args},
	}

	task := timeout(withImage(privileged(withTags(M{"task": "batch", "config": cfg}, g.suiteTags(run, suite)), s.Privileged), g.taskImage(suite, s.Image)), g.suiteMinutes(run, suite))

	return g.withBatchEnd(run, suite, task)
}

func (g *gen) wholeSuiteSteps(run, suite string, i int, w plugin.SpecWholeSuite) []any {
	s := g.suites[suite]
	name := "whole." + w.Name
	gate := fmt.Sprintf("whole_%d", i)

	rs := g.runSuite(run, suite)
	params := g.taskEnv(run, suite, rs.Secrets.Whole, M{
		"PIPELINEGEN_JOB_URL": g.jobURL(SuiteJob(run, suite)),
		"PIPELINEGEN_RECORD":  strconv.FormatBool(rs.Records),
	})

	task := timeout(withImage(privileged(withTags(M{
		"task":           name,
		"file":           "source/" + w.Task,
		"params":         params,
		"output_mapping": M{"result": "result-" + name},
	}, g.suiteTags(run, suite)), w.Privileged || s.Privileged), g.taskImage(suite, s.Image)), g.suiteMinutes(run, suite))

	task["ensure"] = limited(g.helper(name+"-ends", g.suiteTags(run, suite), append(names("source", "trigger"), M{"name": "result-" + name, "optional": true}), nil,
		g.env(run, suite, nil), "report", "whole", "--name", w.Name, "--result", "result-"+name, "--root", "source"), stepsKey, g.stepMinutes())

	collect := limited(g.helper("collect-"+w.Name, g.suiteTags(run, suite), names("source", "trigger"), []string{"slices"}, g.env(run, suite, nil),
		"report", "collect", "--suite", suite, "--out", "slices", "--root", "source"), stepsKey, g.stepMinutes())

	return []any{
		M{"load_var": gate, "file": "gate/whole-" + w.Name + ".json"},
		M{"across": []any{M{"var": gate + "_on", "values": "((.:" + gate + "))"}}, "do": []any{collect, task}},
	}
}

func (g *gen) runSuite(run, suite string) plugin.SpecRunSuite {
	for _, rs := range g.runs[run].Suites {
		if rs.Suite == suite {
			return rs
		}
	}

	return plugin.SpecRunSuite{}
}

// Defaults in minutes, for a spec that leaves the limits out.
const (
	defaultPlanMinutes  = 60
	defaultStepMinutes  = 20
	defaultSuiteMinutes = 120
)

func (g *gen) planMinutes() int {
	if m := g.spec.Limits.Plan; m > 0 {
		return m
	}

	return defaultPlanMinutes
}

func (g *gen) stepMinutes() int {
	if m := g.spec.Limits.Steps; m > 0 {
		return m
	}

	return defaultStepMinutes
}

func (g *gen) suiteMinutes(run, suite string) int {
	if m := g.runSuite(run, suite).Minutes; m > 0 {
		return m
	}

	return defaultSuiteMinutes
}

// Every step has a timeout, so a hung step ends and its ensure reports it.
// Steps in hooks need their own, as Concourse runs a hook outside the limit
// of the step it hangs on.
func timeout(step M, minutes int) M {
	step["timeout"] = fmt.Sprintf("%dm", minutes)

	return step
}

// limited also tells the tool its limit, so it ends a little before the
// step would be killed. The deadline is stamped at the script's start, so
// the bootstrap and a prepare command spend the same budget.
func limited(step M, key string, minutes int) M {
	timeout(step, minutes)

	cfg, ok := step["config"].(M)
	if !ok {
		return step
	}

	if params, ok := cfg["params"].(M); ok {
		params[plugin.EnvLimit] = fmt.Sprintf("%s=%d", key, minutes)
	}

	if run, ok := cfg["run"].(M); ok && run["path"] == "sh" {
		if args, ok := run["args"].([]any); ok && len(args) >= 2 {
			if script, ok := args[1].(string); ok {
				args[1] = fmt.Sprintf("export %s=$(( $(date +%%s) + %d ))\n", plugin.EnvDeadline, minutes*60) + script
			}
		}
	}

	return step
}

// jobURL links the job, since tasks don't learn their build's id.
func (g *gen) jobURL(job string) string {
	return fmt.Sprintf("%s/teams/%s/pipelines/%s/jobs/%s",
		strings.TrimRight(g.ci.URL, "/"), url.PathEscape(g.ci.Team),
		url.PathEscape(g.spec.Pipeline), url.PathEscape(job))
}

func (g *gen) groups(p M) []any {
	inRun := map[string]bool{}

	var out []any

	for _, r := range g.spec.Runs {
		jobs := []string{planJob(r.Name)}
		if r.Trigger.Kind == "pull_requests" {
			jobs = append(jobs, sweepJob(r.Name))
		}

		for _, rs := range r.Suites {
			jobs = append(jobs, SuiteJob(r.Name, rs.Suite))
		}

		for _, j := range jobs {
			inRun[j] = true
		}

		out = append(out, M{"name": r.Name, "jobs": jobs})
	}

	var rest []string

	jobs, _ := p["jobs"].([]any) //nolint:errcheck // the generator's own list

	for _, j := range jobs {
		if m, ok := j.(M); ok {
			if name, ok := m["name"].(string); ok && !inRun[name] {
				rest = append(rest, name)
			}
		}
	}

	return append(out, M{"name": "pipeline", "jobs": rest})
}

func groupsOf(p M) []any {
	groups, ok := p["groups"].([]any)
	if !ok {
		return nil
	}

	delete(p, "groups")

	return groups
}

const plannerImage = "planner"

// sharePlannerImage lets a suite whose image is the planner's fetch it once,
// under the planner's name.
func (g *gen) sharePlannerImage(file string) error {
	data, err := os.ReadFile(file) //nolint:gosec // the include the config names
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInclude, err)
	}

	var inc struct {
		Resources []M `yaml:"resources"`
	}

	if err := yaml.Unmarshal(data, &inc); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInclude, file, err)
	}

	for name, s := range g.suites {
		i := slices.IndexFunc(inc.Resources, func(r M) bool { return r["name"] == s.Image })
		if s.Image == "" || i < 0 || !g.isPlannerImage(inc.Resources[i]) {
			continue
		}

		if g.plannerFrom == nil {
			g.plannerFrom = map[string]string{}
		}

		g.plannerFrom[name] = s.Image
		s.Image = plannerImage
		g.suites[name] = s
	}

	return nil
}

func (g *gen) isPlannerImage(r M) bool {
	src, ok := r["source"].(M)
	want := g.ci.Planner.Image

	if !ok || r["type"] != "registry-image" || fmt.Sprint(src["repository"]) != fmt.Sprint(want["repository"]) {
		return false
	}

	version, pinned := r["version"].(M)
	digest := fmt.Sprint(version["digest"])

	switch {
	case pinned:
		return digest == g.ci.Planner.Digest
	case src["digest"] != nil:
		return fmt.Sprint(src["digest"]) == g.ci.Planner.Digest
	default:
		return want["tag"] != nil && fmt.Sprint(src["tag"]) == fmt.Sprint(want["tag"])
	}
}

func include(p M, file string) error {
	data, err := os.ReadFile(file) //nolint:gosec // the include the config names
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInclude, err)
	}

	var inc M
	if err := yaml.Unmarshal(data, &inc); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInclude, file, err)
	}

	for key := range inc {
		switch key {
		case "resource_types", "resources", "groups", "jobs":
		default:
			return fmt.Errorf("%w: %s: unknown key %s", ErrInclude, file, key)
		}
	}

	for _, key := range []string{"resource_types", "resources", "groups", "jobs"} {
		items, ok := inc[key].([]any)
		if !ok {
			continue
		}

		have, ok := p[key].([]any)
		if !ok {
			have = nil
		}

		seen := map[string]bool{}

		for _, it := range have {
			if m, ok := it.(M); ok {
				seen[fmt.Sprint(m["name"])] = true
			}
		}

		for _, it := range items {
			if m, ok := it.(M); ok && seen[fmt.Sprint(m["name"])] {
				return fmt.Errorf("%w: %s: %s %v is the generated pipeline's", ErrInclude, file, key, m["name"])
			}
		}

		p[key] = append(have, items...)
	}

	return nil
}

func names(items ...string) []M {
	out := make([]M, 0, len(items))
	for _, it := range items {
		out = append(out, M{"name": it})
	}

	return out
}

func caches(paths ...string) []M {
	out := make([]M, 0, len(paths))
	for _, p := range paths {
		out = append(out, M{"path": p})
	}

	return out
}

func withTags(step M, tags []string) M {
	if len(tags) > 0 {
		step["tags"] = slices.Clone(tags)
	}

	return step
}

func withImage(step M, image string) M {
	if image != "" {
		step["image"] = image
	}

	return step
}

func privileged(step M, on bool) M {
	if on {
		step["privileged"] = true
	}

	return step
}

// handOffTrigger runs none of the change's code and holds no credential.
func handOffTrigger(tags []string) M {
	return withTags(M{"task": "trigger", "image": "planner", "config": M{
		"platform": "linux",
		"inputs":   names("handoff"),
		"outputs":  names("trigger"),
		"run":      M{"path": "sh", "args": []any{"-ec", "cp handoff/version trigger/run.json"}},
	}}, tags)
}

// sweepWrite hands on a connector's change as its run.json, the mock
// resource's version.
func (g *gen) sweepWrite(run string) []string {
	if g.byConnector(run) {
		return []string{"sweep-write", "--connector"}
	}

	return []string{"sweep-write"}
}
