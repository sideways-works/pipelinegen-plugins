package concourse

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"
)

// ErrPlacement wraps a placement the runner can't follow.
var ErrPlacement = errors.New("placement")

const capPlacement = plugin.CapPlacement

// What a placement rule places, by what a step is.
const (
	forPipeline = "pipeline"
	// forPlanning includes a proposed change's sweep.
	forPlanning = "planning"
	forChecks   = "checks"
	// forSuite is every step of a suite's job but its bookkeeping.
	forSuite = "suite"
	// forBookkeeping are each batch's end and the suite's end.
	forBookkeeping = "bookkeeping"
)

// placeable says whether a rule for each kind may name a run and a suite.
var placeable = map[string]struct{ run, suite bool }{
	forPipeline:    {},
	forPlanning:    {run: true},
	forChecks:      {},
	forSuite:       {run: true, suite: true},
	forBookkeeping: {run: true, suite: true},
}

type runnerConfig struct {
	Placement *placement `json:"placement"`
}

// The first matching rule places a step, else the default. An empty tag
// list is a value: untagged workers.
type placement struct {
	Default *[]string `json:"default"`
	Rules   []rule    `json:"rules"`
}

type rule struct {
	For   string    `json:"for"`
	Run   string    `json:"run,omitempty"`
	Suite string    `json:"suite,omitempty"`
	Tags  *[]string `json:"tags"`
}

func (r rule) matches(what, run, suite string) bool {
	return r.For == what && (r.Run == "" || r.Run == run) && (r.Suite == "" || r.Suite == suite)
}

// String is the rule as the config writes it.
func (r rule) String() string {
	parts := []string{"for: " + r.For}
	if r.Run != "" {
		parts = append(parts, "run: "+r.Run)
	}

	if r.Suite != "" {
		parts = append(parts, "suite: "+r.Suite)
	}

	return "{" + strings.Join(parts, ", ") + "}"
}

// readConfig refuses unknown keys, so a typo never leaves a step where it
// was.
func readConfig(block map[string]any) (runnerConfig, error) {
	var c runnerConfig

	if len(block) == 0 {
		return c, nil
	}

	data, err := json.Marshal(block)
	if err != nil {
		return c, fmt.Errorf("%w: the runner's config: %w", ErrPlacement, err)
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("%w: the runner's config: %w", ErrPlacement, err)
	}

	return c, nil
}

type placer struct {
	own  *placement
	name string
}

func newPlacer(spec plugin.Spec, conf runnerConfig) placer {
	p := placer{own: conf.Placement, name: tool(spec, plugin.KindRunner).Name}
	if p.name == "" {
		p.name = "<runner>"
	}

	return p
}

// Bookkeeping no rule places goes with its suite, so a job's gets stay on
// one worker.
func (p placer) tags(what, run, suite string) []string {
	if r, ok := p.rule(what, run, suite); ok {
		return *r.Tags
	}

	if what == forBookkeeping {
		return p.tags(forSuite, run, suite)
	}

	if p.own != nil && p.own.Default != nil {
		return *p.own.Default
	}

	return nil
}

func (p placer) rule(what, run, suite string) (rule, bool) {
	if p.own == nil {
		return rule{}, false
	}

	for _, r := range p.own.Rules {
		if r.matches(what, run, suite) {
			return r, true
		}
	}

	return rule{}, false
}

type step struct{ what, run, suite string }

func steps(spec plugin.Spec) []step {
	out := []step{{what: forPipeline}, {what: forChecks}}

	for _, r := range spec.Runs {
		out = append(out, step{what: forPlanning, run: r.Name})

		for _, rs := range r.Suites {
			out = append(out, step{forSuite, r.Name, rs.Suite}, step{forBookkeeping, r.Name, rs.Suite})
		}
	}

	return out
}

// notices name the suite jobs that rules without a default leave on
// untagged workers, which the rules suggest was not meant.
func (p placer) notices(spec plugin.Spec) []string {
	if p.own == nil || len(p.own.Rules) == 0 || p.own.Default != nil {
		return nil
	}

	var out []string

	for _, s := range steps(spec) {
		if s.what != forSuite {
			continue
		}

		if _, ok := p.rule(s.what, s.run, s.suite); !ok {
			out = append(out, fmt.Sprintf("placement: %s is placed by no rule and there is no default: its steps run on untagged workers", SuiteJob(s.run, s.suite)))
		}
	}

	return out
}

// Notices are warnings about the pipeline short of refusing it.
func Notices(spec plugin.Spec, config map[string]any) []string {
	out := lockNotices(spec)

	conf, err := readConfig(config)
	if err != nil {
		return out
	}

	return append(out, newPlacer(spec, conf).notices(spec)...)
}

// lockNotices name the suites an older pipelinegen's spec gives a lock,
// which no step takes.
func lockNotices(spec plugin.Spec) []string {
	var out []string

	for _, s := range spec.Suites {
		if s.Lock != nil {
			out = append(out, fmt.Sprintf("suite %s: the spec holds a lock, which this runner no longer takes: a suite's batches start when their node has room; update pipelinegen", s.Name))
		}
	}

	return out
}

func (p placer) check(spec plugin.Spec) error {
	if err := olderKeys(spec.CI, nil); err != nil {
		return err
	}

	if p.own == nil {
		return nil
	}

	var errs []error

	bad := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%w: plugins.%s.config.placement: %s", ErrPlacement, p.name, fmt.Sprintf(format, args...)))
	}

	if d := p.own.Default; d != nil && slices.Contains(*d, "") {
		bad("default has an empty tag")
	}

	p.checkRules(spec, bad)

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	p.checkSteps(spec, bad)

	return errors.Join(errs...)
}

func (p placer) checkRules(spec plugin.Spec, bad func(string, ...any)) {
	runs, suites := map[string]bool{}, map[string]bool{}
	for _, r := range spec.Runs {
		runs[r.Name] = true
	}

	for _, s := range spec.Suites {
		suites[s.Name] = true
	}

	for i, r := range p.own.Rules {
		where := fmt.Sprintf("rule %d %s", i+1, r)
		can, known := placeable[r.For]

		switch {
		case !known:
			bad("%s: for is one of pipeline, planning, checks, suite or bookkeeping", where)
		case r.Tags == nil:
			bad("%s: tags is missing ([] for untagged workers)", where)
		case slices.Contains(*r.Tags, ""):
			bad("%s: an empty tag", where)
		}

		if known && r.Run != "" && !can.run {
			bad("%s: a rule for %s names no run", where, r.For)
		}

		if known && r.Suite != "" && !can.suite {
			bad("%s: a rule for %s names no suite", where, r.For)
		}

		if r.Run != "" && !runs[r.Run] {
			bad("%s: no run %s", where, r.Run)
		}

		if r.Suite != "" && !suites[r.Suite] {
			bad("%s: no suite %s", where, r.Suite)
		}
	}
}

func (r rule) coveredBy(e rule) bool {
	return e.For == r.For && (e.Run == "" || e.Run == r.Run) && (e.Suite == "" || e.Suite == r.Suite)
}

func (p placer) checkSteps(spec plugin.Spec, bad func(string, ...any)) {
	all := steps(spec)

	for i, r := range p.own.Rules {
		where := fmt.Sprintf("rule %d %s", i+1, r)

		if j := slices.IndexFunc(p.own.Rules[:i], r.coveredBy); j >= 0 {
			bad("%s places nothing: rule %d %s comes first and places all it would", where, j+1, p.own.Rules[j])

			continue
		}

		if !slices.ContainsFunc(all, func(s step) bool { return r.matches(s.what, s.run, s.suite) }) {
			bad("%s places nothing: the pipeline has no such step", where)
		}
	}
}

// olderKeys refuses worker tags in the spec's own keys, which the runner
// ignores: otherwise every step would silently land on untagged workers.
// Empty lists place nothing and pass.
func olderKeys(ci map[string]any, suiteTags map[string]bool) error {
	var keys []string

	planner, _ := ci["planner"].(map[string]any) //nolint:errcheck // a planner that isn't a mapping has no tags

	for key, v := range map[string]any{"concourse.planner.tags": planner["tags"], "concourse.resource_tags": ci["resource_tags"]} {
		if list, _ := v.([]any); len(list) > 0 { //nolint:errcheck // anything but a list places nothing
			keys = append(keys, key)
		}
	}

	slices.Sort(keys)

	if ci["bookkeeping"] != nil {
		keys = append(keys, "concourse.bookkeeping")
	}

	for _, s := range slices.Sorted(maps.Keys(suiteTags)) {
		keys = append(keys, "suites."+s+".run_on.tags")
	}

	if len(keys) == 0 {
		return nil
	}

	return fmt.Errorf("%w: %s place steps on workers, which this runner reads only from its own config's placement: "+
		"update pipelinegen, and write them as rules of plugins.<runner>.config.placement (see the runner's README)",
		ErrPlacement, strings.Join(keys, ", "))
}

// requestTags reads suite tags from the raw request, since the spec's type
// has no field for them.
func requestTags(in []byte) map[string]bool {
	var req struct {
		Spec struct {
			Suites []struct {
				Name string   `json:"name"`
				Tags []string `json:"tags"`
			} `json:"suites"`
		} `json:"spec"`
	}

	if json.Unmarshal(in, &req) != nil {
		return nil
	}

	out := map[string]bool{}

	for _, s := range req.Spec.Suites {
		if len(s.Tags) > 0 {
			out[s.Name] = true
		}
	}

	return out
}
