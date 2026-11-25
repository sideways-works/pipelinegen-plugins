package concourse

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"
)

// defaultWorkerWaitMinutes is how long a step may wait for a worker when the config leaves it out.
const defaultWorkerWaitMinutes = 480

// own is what the runner's config block says: where steps run, how long each may wait for a worker, and
// which suites' tasks keep no job cache.
type own struct {
	place    placer
	wait     int
	uncached map[string]bool
}

// fromConfig reads the runner's config block.
func fromConfig(spec plugin.Spec, block map[string]any) (own, error) {
	conf, err := readConfig(block)
	if err != nil {
		return own{}, err
	}

	wait, err := workerWait(conf)
	if err != nil {
		return own{}, err
	}

	place := newPlacer(spec, conf)
	if err := place.check(spec); err != nil {
		return own{}, err
	}

	uncached, err := uncachedSuites(spec, conf)
	if err != nil {
		return own{}, err
	}

	return own{place: place, wait: wait, uncached: uncached}, nil
}

// uncachedSuites are caches.without, each a suite of the spec, so a typo never leaves a suite's cache in.
func uncachedSuites(spec plugin.Spec, conf runnerConfig) (map[string]bool, error) {
	out := map[string]bool{}
	if conf.Caches == nil {
		return out, nil
	}

	for _, name := range conf.Caches.Without {
		if !slices.ContainsFunc(spec.Suites, func(s plugin.SpecSuite) bool { return s.Name == name }) {
			return nil, fmt.Errorf("%w: the runner's config: caches.without names %q, which is no suite", ErrPlacement, name)
		}

		out[name] = true
	}

	return out, nil
}

// workerWait is the config's worker_wait_minutes, else the default; 0 adds nothing.
func workerWait(c runnerConfig) (int, error) {
	if c.WorkerWaitMinutes == nil {
		return defaultWorkerWaitMinutes, nil
	}

	if m := *c.WorkerWaitMinutes; m < 0 {
		return 0, fmt.Errorf("%w: the runner's config: worker_wait_minutes is %d; it takes 0 or more", ErrPlacement, m)
	}

	return *c.WorkerWaitMinutes, nil
}

// waitForWorkers adds minutes to every step's timeout. Concourse counts a step's wait for a worker,
// with every worker at its limit of active tasks, inside the step's timeout, so a step that waited
// longer than its own limit failed without having run. pipelinegen counts its own limits from its
// start, so a step that runs it still ends within its minutes once it runs. A task's config and
// params are left as they are: a key named timeout there is no step's.
func waitForWorkers(v any, minutes int) {
	if minutes == 0 {
		return
	}

	addWait(v, minutes, map[uintptr]bool{})
}

// addWait goes to each map once: the generator hands one step to several places, as a batch's
// retry, and its limit takes the wait once.
func addWait(v any, minutes int, seen map[uintptr]bool) {
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			addWait(e, minutes, seen)
		}
	case []M:
		for _, e := range x {
			addWait(e, minutes, seen)
		}
	case M:
		at := reflect.ValueOf(x).Pointer()
		if seen[at] {
			return
		}

		seen[at] = true

		for k, e := range x {
			switch k {
			case "timeout":
				x[k] = longer(e, minutes)
			case "config", "params", "vars", "source":
			default:
				addWait(e, minutes, seen)
			}
		}
	}
}

func longer(timeout any, minutes int) any {
	s, ok := timeout.(string)
	if !ok {
		return timeout
	}

	m, err := strconv.Atoi(strings.TrimSuffix(s, "m"))
	if err != nil || !strings.HasSuffix(s, "m") {
		return timeout
	}

	return fmt.Sprintf("%dm", m+minutes)
}
