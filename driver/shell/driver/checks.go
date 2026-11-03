package driver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

// Check is a named check of a run step, with its own time limit and result.
// A slice passes when every check passes.
type Check struct {
	// Name is the test's name in the slice's result.
	Name string `json:"name"`
	Run  string `json:"run"`
	// Timeout is the check's own limit (default none).
	Timeout string `json:"timeout"`
	// Need names earlier checks that must pass for this one to run.
	Need []string `json:"need"`

	limit time.Duration
}

func (*Check) settingNoun() string { return "a check" }

// Needs name only earlier checks, so no need goes round.
func checkSettings(checks []Check) error {
	seen := map[string]bool{}

	for i := range checks {
		c := &checks[i]

		switch {
		case strings.TrimSpace(c.Name) == "":
			return fmt.Errorf("%w: checks[%d] has no name", errSettings, i)
		case seen[c.Name]:
			return fmt.Errorf("%w: checks[%d]: two checks are named %q", errSettings, i, c.Name)
		case strings.TrimSpace(c.Run) == "":
			return fmt.Errorf("%w: checks[%d] (%s) has nothing to run", errSettings, i, c.Name)
		}

		if c.Timeout != "" {
			limit, err := time.ParseDuration(c.Timeout)
			if err != nil || limit <= 0 {
				return fmt.Errorf("%w: checks[%d].timeout (%s): %q is no positive duration", errSettings, i, c.Name, c.Timeout)
			}

			c.limit = limit
		}

		for _, n := range c.Need {
			if !seen[n] {
				return fmt.Errorf("%w: checks[%d].need (%s): %q is no check before it", errSettings, i, c.Name, n)
			}
		}

		seen[c.Name] = true
	}

	return nil
}

var (
	errCheckLimit = errors.New("the check's time limit ran out")
	errSliceLimit = errors.New("the slice's time limit ran out")
)

type cut int

const (
	notCut cut = iota
	cutByCheck
	// cutBySlice is the step's timeout.
	cutBySlice
	// cutByStep is the step's time, or the driver being stopped.
	cutByStep
)

func cutOf(ctx context.Context) cut {
	cause := context.Cause(ctx)

	switch {
	case cause == nil:
		return notCut
	case errors.Is(cause, errCheckLimit):
		return cutByCheck
	case errors.Is(cause, errSliceLimit):
		return cutBySlice
	default:
		return cutByStep
	}
}

type slicesChecks struct {
	ev       *plugin.DriverEvent
	passed   map[string]bool
	ran      int
	sliceOut string
}

func (r *slicesChecks) notRun(checks []Check, why string) {
	for _, c := range checks {
		r.ev.Failures = append(r.ev.Failures, plugin.Failure{Test: c.Name, Tail: "not run: " + why + "\n"})
	}
}

// checks: a check past its own limit fails and the rest go on; past the
// slice's limit the rest fail as not run. When the step's time runs out
// before any check failed, the slice gets no end (nil).
func (j *job) checks(ctx context.Context, sl plugin.DriverSlice) *plugin.DriverEvent {
	start := time.Now()

	if j.req.Record && sl.Recording != "" {
		if err := os.MkdirAll(sl.Recording, 0o750); err != nil {
			return failed(sl.Name, sl.Name, fmt.Sprintf("recording: %v\n", err))
		}
	}

	sctx := ctx

	if j.set.limit > 0 {
		var cancel context.CancelFunc

		sctx, cancel = context.WithTimeoutCause(ctx, j.set.limit, errSliceLimit)
		defer cancel()
	}

	env := environment(j.req, j.set, sliceVars(j.req, sl))
	r := &slicesChecks{
		ev: &plugin.DriverEvent{Slice: sl.Name, Event: plugin.EventEnd}, passed: map[string]bool{},
		sliceOut: fmt.Sprintf("the slice's time limit of %s ran out", j.set.limit),
	}

	for i := range j.set.Checks {
		if !j.nextCheck(sctx, sl, r, i, env) {
			break
		}
	}

	if r.ev == nil {
		return nil
	}

	r.ev.Tests = &r.ran
	r.ev.Passed = len(r.ev.Failures) == 0
	r.ev.Seconds = time.Since(start).Seconds()
	r.ev.Recorded = recorded(j.req, sl)

	return r.ev
}

func (j *job) nextCheck(sctx context.Context, sl plugin.DriverSlice, r *slicesChecks, i int, env []string) bool {
	c := j.set.Checks[i]

	switch cutOf(sctx) {
	case cutByStep:
		if len(r.ev.Failures) == 0 {
			r.ev = nil

			j.sayNoEnd(sl, i, "")

			return false
		}

		r.notRun(j.set.Checks[i:], "the step's time ran out")

		return false
	case cutBySlice:
		r.notRun(j.set.Checks[i:], r.sliceOut)

		return false
	case notCut, cutByCheck:
	}

	if missing := slices.DeleteFunc(slices.Clone(c.Need), func(n string) bool { return r.passed[n] }); len(missing) > 0 {
		r.ev.Failures = append(r.ev.Failures, plugin.Failure{Test: c.Name, Tail: fmt.Sprintf("not run: it needs %s, which did not pass\n", strings.Join(missing, ", "))})
		sayf(j.log, "skip %s %s: it needs %s, which did not pass\n", sl.Name, c.Name, strings.Join(missing, ", "))

		return true
	}

	f, by := j.check(sctx, sl, c, env)

	switch by {
	case cutByStep:
		// The check's result is unknown, and so is every result after it.
		if len(r.ev.Failures) == 0 {
			r.ev = nil

			j.sayNoEnd(sl, i, c.Name)

			return false
		}

		r.ev.Failures = append(r.ev.Failures, plugin.Failure{Test: c.Name, Tail: f.Tail + "stopped: the step's time ran out\n"})
		r.notRun(j.set.Checks[i+1:], "the step's time ran out")

		return false
	case notCut, cutByCheck, cutBySlice:
	}

	r.ran++

	if f != nil {
		r.ev.Failures = append(r.ev.Failures, *f)
	} else {
		r.passed[c.Name] = true
	}

	if by == cutBySlice {
		r.notRun(j.set.Checks[i+1:], r.sliceOut)

		return false
	}

	return true
}

func (j *job) sayNoEnd(sl plugin.DriverSlice, from int, stopped string) {
	var left []string

	for _, c := range j.set.Checks[from:] {
		if c.Name != stopped {
			left = append(left, c.Name)
		}
	}

	if stopped != "" {
		sayf(j.log, "shell-driver: the step's time ran out during check %q of %s, which was stopped\n", stopped, sl.Name)
	}

	if len(left) > 0 {
		sayf(j.log, "shell-driver: never run in %s: %s\n", sl.Name, strings.Join(left, ", "))
	}

	sayf(j.log, "shell-driver: %s has no result: no check failed before the step's time ran out\n", sl.Name)
}

func (j *job) check(sctx context.Context, sl plugin.DriverSlice, c Check, env []string) (*plugin.Failure, cut) {
	cctx, cancel := context.WithCancel(sctx)
	defer cancel()

	if c.limit > 0 {
		var stop context.CancelFunc

		cctx, stop = context.WithTimeoutCause(cctx, c.limit, errCheckLimit)
		defer stop()
	}

	j.seq++

	args, err := argv(j.set, c.Run, filepath.Join(j.req.Work, fmt.Sprintf("check-%d", j.seq)))
	if err != nil {
		return &plugin.Failure{Test: c.Name, Tail: err.Error() + "\n"}, notCut
	}

	sayf(j.log, "== %s: %s\n", sl.Name, c.Name)

	out := newTail(j.log)
	start := time.Now()
	runErr := runCmd(command(cctx, args, workDir(j.req.Tree, j.set), env, out), fmt.Sprintf("check %q of %s", c.Name, sl.Name), j.log)
	took := time.Since(start).Round(100 * time.Millisecond)
	by := cutOf(cctx)

	if runErr == nil && by == notCut {
		sayf(j.log, "ok   %s %s (%s)\n", sl.Name, c.Name, took)

		return nil, notCut
	}

	tail := out.Tail()

	switch by {
	case cutByCheck:
		tail += fmt.Sprintf("the check's time limit of %s ran out\n", c.limit)
	case cutBySlice:
		tail += fmt.Sprintf("the slice's time limit of %s ran out\n", j.set.limit)
	case cutByStep:
	case notCut:
		if status, exited := exitStatus(runErr); exited {
			tail += fmt.Sprintf("exit status %d\n", status)
		} else {
			tail += fmt.Sprintf("it did not run: %v\n", runErr)
		}
	}

	sayf(j.log, "FAIL %s %s (%s)\n", sl.Name, c.Name, took)

	return &plugin.Failure{Test: c.Name, Tail: tail}, by
}
