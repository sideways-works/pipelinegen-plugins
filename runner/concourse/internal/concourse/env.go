package concourse

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"
)

// ErrEnv wraps params or secrets the pipeline can't carry safely.
var ErrEnv = errors.New("params and secrets")

var (
	envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	varName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

// plannerOwn are the plan step's own: a planner param by one of these names
// would be taken from the plan along with the rest.
var plannerOwn = []string{"PATH", "HOME", "GITHUB_TOKEN", "GOMODCACHE", "GOCACHE", "npm_config_cache"}

// checkEnv repeats the tool's check, so a config that skipped it never
// reaches a pipeline.
func checkEnv(spec plugin.Spec, ci settings) error {
	for _, r := range spec.Runs {
		for _, rs := range r.Suites {
			where := "run " + r.Name + ", suite " + rs.Suite
			kinds := map[string]map[string]string{
				"prepare": rs.Secrets.Prepare, "batch": rs.Secrets.Batch, "whole": rs.Secrets.Whole,
			}

			if err := checkSet(where, rs.Params, kinds); err != nil {
				return err
			}

			// A proposed change's tasks run its code, so at most the read token.
			if r.Trigger.Kind == "pull_requests" {
				if err := atMostRead(spec, where, kinds); err != nil {
					return err
				}
			}
		}
	}

	pl := ci.Planner
	if err := checkSet("the planner", pl.Params, map[string]map[string]string{"planner": pl.Secrets}); err != nil {
		return err
	}

	for _, n := range slices.Concat(slices.Collect(maps.Keys(pl.Params)), slices.Collect(maps.Keys(pl.Secrets))) {
		// The prepare command may hold the read token as GITHUB_TOKEN.
		if n == "GITHUB_TOKEN" && pl.Secrets[n] == readVar(spec) {
			continue
		}

		if slices.Contains(plannerOwn, n) {
			return fmt.Errorf("%w: the planner: %s is the plan step's own", ErrEnv, n)
		}
	}

	// The planner's secrets share the task that runs a proposed change's
	// code, so none may be a token that writes.
	if slices.ContainsFunc(spec.Runs, func(r plugin.SpecRun) bool { return r.Trigger.Kind == "pull_requests" }) {
		if err := noWriting(spec, "the planner", map[string]map[string]string{"planner": pl.Secrets}); err != nil {
			return err
		}
	}

	if pl.GitToken != "" {
		if _, ok := pl.Secrets[pl.GitToken]; !ok {
			return fmt.Errorf("%w: the planner: git_token %s is none of its secrets", ErrEnv, pl.GitToken)
		}
	}

	return nil
}

func readVar(spec plugin.Spec) string {
	if v := spec.GitHub.Tokens[plugin.PurposeRead]; v != "" {
		return v
	}

	return spec.GitHub.TokenVar
}

func atMostRead(spec plugin.Spec, where string, secrets map[string]map[string]string) error {
	if len(spec.GitHub.Tokens) == 0 {
		return nil
	}

	for _, kind := range slices.Sorted(maps.Keys(secrets)) {
		for _, k := range slices.Sorted(maps.Keys(secrets[kind])) {
			v := secrets[kind][k]

			for _, p := range plugin.Purposes {
				if p != plugin.PurposeRead && spec.GitHub.Tokens[p] == v && v != readVar(spec) {
					return fmt.Errorf("%w: %s: %s secret %s is the %s token: a task that runs a proposed change's code gets at most the read token", ErrEnv, where, kind, k, p)
				}
			}
		}
	}

	return nil
}

func noWriting(spec plugin.Spec, where string, secrets map[string]map[string]string) error {
	if len(spec.GitHub.Tokens) == 0 {
		return nil
	}

	for _, kind := range slices.Sorted(maps.Keys(secrets)) {
		for _, k := range slices.Sorted(maps.Keys(secrets[kind])) {
			v := secrets[kind][k]

			for _, p := range []string{plugin.PurposeReport, plugin.PurposeStore} {
				if spec.GitHub.Tokens[p] == v && v != readVar(spec) {
					return fmt.Errorf("%w: %s: %s secret %s is the %s token, which writes: it would share the task that plans a proposed change's code", ErrEnv, where, kind, k, p)
				}
			}
		}
	}

	return nil
}

func checkSet(where string, params map[string]string, secrets map[string]map[string]string) error {
	for _, k := range slices.Sorted(maps.Keys(params)) {
		switch {
		case strings.HasPrefix(k, "PIPELINEGEN_"):
			return fmt.Errorf("%w: %s: param %s: PIPELINEGEN_ names are pipelinegen's", ErrEnv, where, k)
		case strings.Contains(params[k], "(("):
			return fmt.Errorf("%w: %s: param %s holds a var: declare it in secrets", ErrEnv, where, k)
		}
	}

	for _, kind := range slices.Sorted(maps.Keys(secrets)) {
		for _, k := range slices.Sorted(maps.Keys(secrets[kind])) {
			v := secrets[kind][k]

			switch {
			case !envName.MatchString(k) || strings.HasPrefix(k, "PIPELINEGEN_"):
				return fmt.Errorf("%w: %s: %s secret %q is no variable name of the repository's", ErrEnv, where, kind, k)
			case !varName.MatchString(v):
				return fmt.Errorf("%w: %s: %s secret %s: %q is no plain var name", ErrEnv, where, kind, k, v)
			}

			if _, ok := params[k]; ok {
				return fmt.Errorf("%w: %s: %s is both a param and a %s secret", ErrEnv, where, k, kind)
			}
		}
	}

	return nil
}
