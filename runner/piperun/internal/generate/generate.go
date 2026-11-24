// Package generate writes piperun's piperun.yaml for a pipelinegen config: one pipeline per run,
// whose planning jobs emit each suite's prepare, the pool of its selected slices, its whole-suite
// checks and its end, from templates this file declares.
package generate

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"
)

// ErrSpec is a config this runner can't run on piperun.
var ErrSpec = errors.New("piperun-runner")

// The tool, where piperun mounts it, the planner's cache and the directories the steps share.
const (
	toolName   = "pipelinegen"
	tool       = "/piperun/tools/" + toolName
	planCache  = "planner"
	cachePath  = "/cache"
	dirPlan    = "plan"
	dirPools   = "pools"
	dirPrep    = "prepared"
	emitDir    = "$PIPERUN_EMIT"
	planOutDir = "out/plan"
)

// The jobs of a run's pipeline that plan; the second runs none of the change's code and holds the
// writing tokens.
const (
	jobPlan  = "plan"
	jobWrite = "plan-write"
)

// The environment variables of pipelinegen's own steps.
const (
	envStep    = "PIPELINEGEN_PIPELINE_STEP"
	envTrusted = "PIPELINEGEN_TRUSTED"
	envRun     = "PIPELINEGEN_RUN"
	envSuite   = "PIPELINEGEN_SUITE"
	envRunFile = "PIPELINEGEN_RUN_FILE"
	envOneTok  = "GITHUB_TOKEN"
)

// rerunSafe marks a bookkeeping step a worker taking the job over may run again: each writes what
// it would write the first time.
const rerunSafe = "safe"

// Generate is the piperun.yaml of the spec with the runner's settings, and the notices of what a
// person should know of it.
func Generate(s plugin.Spec, set Settings) (File, []string, error) {
	g := &gen{s: s, set: set, secrets: map[string]Secret{}}

	tool, err := g.tool()
	if err != nil {
		return File{}, nil, err
	}

	f := File{
		Version:   1,
		Tools:     map[string]Tool{toolName: tool},
		Images:    map[string]Image{},
		Caches:    map[string]Cache{planCache: {}},
		Resources: map[string]Res{set.Resource.Name: g.resource()},
		Pipelines: map[string]Pipeline{},
	}

	suites := map[string]plugin.SpecSuite{}
	for _, su := range s.Suites {
		suites[su.Name] = su
	}

	var errs []error

	for _, r := range s.Runs {
		p, err := g.pipeline(r, suites)
		if err != nil {
			errs = append(errs, err)

			continue
		}

		f.Pipelines[r.Name] = p
	}

	if err := errors.Join(errs...); err != nil {
		return File{}, nil, err
	}

	for _, n := range g.images {
		f.Images[n] = set.Images[n]
	}

	for _, n := range g.caches {
		f.Caches[n] = Cache{}
	}

	if len(g.secrets) > 0 {
		f.Secrets = g.secrets
	}

	return f, g.notices, nil
}

type gen struct {
	s       plugin.Spec
	set     Settings
	secrets map[string]Secret
	images  []string
	caches  []string
	notices []string
}

// tool is pipelinegen's release, mounted into every step of its own: piperun mounts released tools
// only, so a pin by commit or zip can't run there.
func (g *gen) tool() (Tool, error) {
	if p := g.set.Pipelinegen; p != nil {
		repo := p.Repository
		if repo == "" {
			repo = "sideways-works/pipelinegen"
		}

		if p.Version == "" || (p.SHA256 == "") == (p.Sums == "") {
			return Tool{}, fmt.Errorf("%w: pipelinegen: its version and one of sha256 or sums", ErrSpec)
		}

		return Tool{Repository: repo, Release: Release{Version: p.Version, SHA256: p.SHA256, Sums: p.Sums}, Asset: toolName + "-linux-amd64"}, nil
	}

	for _, t := range g.s.Tools {
		if t.Name != plugin.ToolPipelinegen {
			continue
		}

		if t.Version == "" || t.Repository == "" || (t.SHA256 == "") == (t.Sums == "") {
			return Tool{}, fmt.Errorf("%w: piperun mounts released tools only: pin pipelinegen by a release in the runner config's pipelinegen, its version and one of sha256 or sums", ErrSpec)
		}

		return Tool{Repository: t.Repository, Release: Release{Version: t.Version, SHA256: t.SHA256, Sums: t.Sums}, Asset: toolName + "-linux-amd64"}, nil
	}

	return Tool{}, fmt.Errorf("%w: no pipelinegen to mount: pin its release in the runner config's pipelinegen", ErrSpec)
}

func (g *gen) resource() Res {
	with := map[string]any{"remote": g.set.Resource.Remote}
	if c := g.set.Resource.Credential; c != "" {
		with["credential"] = c
		g.secret(c, false)
	}

	return Res{Uses: "git", With: with}
}

// secret declares a secret by its var's name; one that writes anywhere stays writing.
func (g *gen) secret(name string, writes bool) {
	s := g.secrets[name]
	g.secrets[name] = Secret{From: g.set.Secrets.From, Key: name, Writes: s.Writes || writes}
}

// tokens are the environment variables of the given purposes' tokens, each a secret's name; with one
// token for every purpose, GITHUB_TOKEN.
func (g *gen) tokens(purposes ...string) map[string]string {
	out := map[string]string{}

	for _, p := range purposes {
		writes := p != plugin.PurposeRead

		if len(g.s.GitHub.Tokens) == 0 {
			if v := g.s.GitHub.TokenVar; v != "" {
				out[envOneTok] = v
				g.secret(v, true)
			}

			continue
		}

		if v := g.s.GitHub.Tokens[p]; v != "" {
			out["PIPELINEGEN_"+strings.ToUpper(p)+"_TOKEN"] = v
			g.secret(v, writes)
		}
	}

	return out
}

func (g *gen) image(name string) error {
	if _, ok := g.set.Images[name]; !ok {
		return fmt.Errorf("%w: image %q is none of the runner config's images", ErrSpec, name)
	}

	if !slices.Contains(g.images, name) {
		g.images = append(g.images, name)
		slices.Sort(g.images)
	}

	return nil
}

func (g *gen) env(run, suite string) map[string]string {
	e := map[string]string{envStep: "1", envRun: run}
	if g.s.Trusted != "" {
		e[envTrusted] = g.s.Trusted
	}

	if suite != "" {
		e[envSuite] = suite
		e[envRunFile] = dirPools + "/run.json"
	}

	return e
}

// own is a step of pipelinegen's own: the tool, in the planner's image.
func (g *gen) own(run, suite string, args ...string) Step {
	return Step{Run: append([]string{tool}, args...), Image: g.set.Planner.Image, Tools: []string{toolName}, Env: g.env(run, suite)}
}

// bookkeeping is a step of pipelinegen's own that writes records and statuses: pinned, with the
// store and report tokens.
func (g *gen) bookkeeping(run, suite string, args ...string) Step {
	st := g.own(run, suite, args...)
	st.Secrets = g.tokens(plugin.PurposeStore, plugin.PurposeReport)
	st.Trust = "pinned"
	st.Rerun = rerunSafe

	return st
}

func (g *gen) pipeline(r plugin.SpecRun, suites map[string]plugin.SpecSuite) (Pipeline, error) {
	trig, err := g.trigger(r)
	if err != nil {
		return Pipeline{}, err
	}

	if err := g.image(g.set.Planner.Image); err != nil {
		return Pipeline{}, err
	}

	p := Pipeline{Triggers: []Trigger{trig}, Jobs: map[string]Job{}, Templates: map[string]Job{}}

	var errs []error

	for _, rs := range r.Suites {
		su, ok := suites[rs.Suite]
		if !ok {
			errs = append(errs, fmt.Errorf("%w: run %s: no suite %s in the spec", ErrSpec, r.Name, rs.Suite))

			continue
		}

		ts, err := g.templates(r, rs, su)
		if err != nil {
			errs = append(errs, err)

			continue
		}

		maps.Copy(p.Templates, ts)
	}

	if err := errors.Join(errs...); err != nil {
		return Pipeline{}, err
	}

	g.planJobs(r, &p)

	return p, nil
}

func (g *gen) planJobs(r plugin.SpecRun, p *Pipeline) {
	plan := g.own(r.Name, "", "plan", "--run", r.Name, "--trigger", ".", "--cache", cachePath, "--out", planOutDir)
	plan.Secrets = g.tokens(plugin.PurposeRead)
	plan.Caches = map[string]string{planCache: cachePath}
	plan.Outputs = map[string]string{dirPlan: planOutDir}

	write := g.bookkeeping(r.Name, "", "plan", "--run", r.Name, "--trigger", ".", "--from", dirPlan, "--emit", emitDir, "--pools", dirPools)
	write.Outputs = map[string]string{dirPools: dirPools}

	p.Jobs[jobPlan] = Job{
		Needs: g.set.Planner.Needs, Minutes: g.s.Limits.Plan,
		Steps: []Step{{Uses: "git"}, plan},
	}

	p.Jobs[jobWrite] = Job{
		After: []After{{Job: jobPlan}}, Needs: g.set.Planner.Needs, Minutes: g.s.Limits.Steps,
		Inputs: map[string]Input{dirPlan: {Artifact: jobPlan + "/" + dirPlan}},
		Steps:  []Step{{Uses: "git"}, write},
		Emits:  sortedKeys(p.Templates),
	}
}

// trigger is the run's trigger in piperun's words; a pull request's or a forge's run waits for
// pipelinegen to read piperun's change.
func (g *gen) trigger(r plugin.SpecRun) (Trigger, error) {
	t := r.Trigger
	res := g.set.Resource.Name

	switch t.Kind {
	case "branch":
		return Trigger{Uses: "git", With: map[string]any{"resource": res, "branch": t.Branch}}, nil
	case plugin.TriggerContinuous:
		return Trigger{Uses: "git", With: map[string]any{"resource": res, "branch": t.Branch, "mode": "continuous"}}, nil
	case "timer":
		return g.cron(r)
	case "manual":
		return Trigger{Uses: "manual"}, nil
	}

	return Trigger{}, fmt.Errorf("%w: run %s: a %s trigger can't run on piperun yet: plan --out reads a pull request only from Concourse's resource files", ErrSpec, r.Name, t.Kind)
}

// cron is a timer run's trigger: its start each day it names, late by up to its window; piperun
// builds a commit only once, as a timer run that skips an unchanged commit does.
func (g *gen) cron(r plugin.SpecRun) (Trigger, error) {
	tm := r.Trigger.Timer
	if tm == nil {
		return Trigger{}, fmt.Errorf("%w: run %s: a timer without its times", ErrSpec, r.Name)
	}

	start, err := clock(tm.Start)
	if err != nil {
		return Trigger{}, fmt.Errorf("%w: run %s: start: %w", ErrSpec, r.Name, err)
	}

	stop, err := clock(tm.Stop)
	if err != nil {
		return Trigger{}, fmt.Errorf("%w: run %s: stop: %w", ErrSpec, r.Name, err)
	}

	window := (stop - start + 24*60) % (24 * 60)
	if window == 0 {
		window = 24 * 60
	}

	days := "*"
	if len(tm.Days) > 0 {
		days = strings.ToLower(strings.Join(tm.Days, ","))
	}

	with := map[string]any{
		"schedule": fmt.Sprintf("%d %d * * %s", start%60, start/60, days),
		"window":   strconv.Itoa(window) + "m",
		"branch":   r.Trigger.Branch,
	}

	if tm.Location != "" {
		with["location"] = tm.Location
	}

	return Trigger{Uses: "cron", With: with}, nil
}

// clock reads "H:MM" as minutes since midnight.
func clock(s string) (int, error) {
	h, m, ok := strings.Cut(s, ":")

	hh, errH := strconv.Atoi(h)
	mm, errM := strconv.Atoi(m)

	if !ok || errH != nil || errM != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, fmt.Errorf("%w: %q is no H:MM", ErrSpec, s)
	}

	return hh*60 + mm, nil
}

// templates are a suite's templates in a run: its prepare, the pool of its slices and its end. A
// suite of task files (run_slice) is Concourse's; on piperun a suite runs by its drivers' steps.
func (g *gen) templates(r plugin.SpecRun, rs plugin.SpecRunSuite, su plugin.SpecSuite) (map[string]Job, error) {
	where := "run " + r.Name + ", suite " + su.Name

	switch {
	case su.Steps == nil:
		return nil, fmt.Errorf("%w: %s runs a Concourse task file (run_slice); on piperun give the suite steps, which its drivers run", ErrSpec, where)
	case len(su.WholeSuite) > 0:
		return nil, fmt.Errorf("%w: %s: its whole-suite checks are Concourse tasks, which piperun can't run yet", ErrSpec, where)
	case su.Image == "":
		return nil, fmt.Errorf("%w: %s has no image", ErrSpec, where)
	}

	if err := g.image(su.Image); err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}

	out := map[string]Job{}
	caches := g.suiteCaches(su.Name)
	pools := Input{Artifact: jobWrite + "/" + dirPools}
	inputs := map[string]Input{dirPools: pools}
	ends := []After{{Job: plugin.PiperunSlices(su.Name), Ended: true}}

	var poolAfter []After

	if su.Steps.Prepare {
		name, job, err := g.prepare(r.Name, rs, su, inputs, caches)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", where, err)
		}

		out[name] = job
		poolAfter = []After{{Job: name}}
		inputs = map[string]Input{dirPools: pools, dirPrep: {Artifact: name + "/" + dirPrep}}
		ends = append(ends, After{Job: name, Ended: true})
	}

	out[plugin.PiperunSlices(su.Name)] = g.pool(r.Name, rs, su, inputs, caches, poolAfter)

	report := g.bookkeeping(r.Name, su.Name, "report", "suite")
	out[plugin.PiperunEnd(su.Name)] = Job{
		After: ends, Needs: g.set.Planner.Needs, Minutes: g.s.Limits.Steps,
		Inputs: map[string]Input{dirPools: pools},
		Steps:  []Step{{Uses: "git"}, report},
		// exit 4: the store refused a write that may go through on a later attempt
		Retry: &Retry{On: []string{"crash", "exit:4"}, Attempts: 5},
	}

	return out, nil
}

// prepare is the suite's prepare template, in its prepare step's image or else the suite's.
func (g *gen) prepare(run string, rs plugin.SpecRunSuite, su plugin.SpecSuite, inputs map[string]Input, caches map[string]string) (string, Job, error) {
	img := su.Steps.PrepareImage
	if img == "" {
		img = su.Image
	}

	if err := g.image(img); err != nil {
		return "", Job{}, err
	}

	prep := g.suiteStep(run, rs, su, img, rs.Secrets.Prepare, "slice", "prepare", "--root", ".", "--prepared", dirPrep)
	prep.Caches = caches
	prep.Outputs = map[string]string{dirPrep: dirPrep}

	return plugin.PiperunPrepare(su.Name), Job{
		Needs: g.set.needs(su.Name), Minutes: rs.Minutes, Inputs: inputs,
		Steps: []Step{{Uses: "git"}, prep},
	}, nil
}

// pool is the template of the suite's pool: each session runs its slices as they are fed, and the
// session's end writes the confirmed slices' ends and closes what is left. The session step runs the
// change's code, so it never writes records itself: the slices show as each session ends.
func (g *gen) pool(run string, rs plugin.SpecRunSuite, su plugin.SpecSuite, inputs map[string]Input, caches map[string]string, after []After) Job {
	args := []string{"slice", "run", "--root", ".", "--feed", "$PIPERUN_FEED", "--pools", dirPools}
	if su.Steps.Prepare {
		args = append(args, "--prepared", dirPrep)
	}

	session := g.suiteStep(run, rs, su, su.Image, rs.Secrets.Batch, args...)
	session.Caches = caches

	end := g.bookkeeping(run, su.Name, "report", "batch-end", "--confirmed", "$PIPERUN_CONFIRMED", "--requeued", "$PIPERUN_REQUEUED", "--pools", dirPools)

	return Job{
		After: after, Needs: g.set.needs(su.Name), Inputs: inputs, Minutes: rs.Minutes,
		Steps:   []Step{{Uses: "git"}, session},
		Finally: []Step{end},
		Retry:   &Retry{On: []string{"crash", "setup"}, Attempts: 2},
	}
}

// suiteStep is a step that runs the suite's drivers, and so the change's code: the suite's params,
// its declared secrets of the kind, the read token at most, and pipelinegen's own variables last.
func (g *gen) suiteStep(run string, rs plugin.SpecRunSuite, su plugin.SpecSuite, img string, secrets map[string]string, args ...string) Step {
	st := g.own(run, su.Name, args...)
	st.Image = img
	st.Privileged = su.Privileged

	env := map[string]string{}
	maps.Copy(env, rs.Params)
	maps.Copy(env, st.Env)
	st.Env = env

	if len(secrets) > 0 {
		st.Secrets = map[string]string{}

		for k, v := range secrets {
			st.Secrets[k] = v
			g.secret(v, false)
		}
	}

	return st
}

// suiteCaches are the caches the runner config mounts for the suite, by name the path each mounts
// at, each declared once.
func (g *gen) suiteCaches(suite string) map[string]string {
	cs := g.set.Caches[suite]
	if len(cs) == 0 {
		return nil
	}

	out := map[string]string{}

	for name, path := range cs {
		out[name] = path

		if !slices.Contains(g.caches, name) {
			g.caches = append(g.caches, name)
			slices.Sort(g.caches)
		}
	}

	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := slices.Collect(maps.Keys(m))
	slices.Sort(out)

	return out
}
