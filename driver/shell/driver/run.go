package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

const deadlineMargin = time.Minute

type job struct {
	req  plugin.DriverRequest
	set  Settings
	log  io.Writer
	seq  int
	emit func(plugin.DriverEvent) error
}

func run(ctx context.Context, req plugin.DriverRequest, emit func(plugin.DriverEvent) error, log io.Writer) error {
	set, err := settingsOf(req, plugin.CallRun)
	if err != nil {
		return err
	}

	ctx, cancel, err := withDeadline(ctx, req)
	if err != nil {
		return err
	}

	defer cancel()

	trimCaches(ctx, req, set, log)

	j := &job{req: req, set: set, log: log, emit: emit}

	if req.Marks != nil && len(req.Slices) > 1 {
		sayf(log, "shell-driver: a recording of a suite with a stack: the bundle's %d slices run one at a time, marked\n", len(req.Slices))
	}

	if set.Functions == "" && len(set.Checks) == 0 && req.Batch && req.Marks == nil && len(req.Slices) > 1 {
		return j.send(j.invoke(ctx, req.Slices))
	}

	for _, sl := range req.Slices {
		if ctx.Err() != nil {
			// The step's time ran out: what is left gets no end.
			return nil
		}

		if err := j.one(ctx, sl); err != nil {
			return err
		}
	}

	return nil
}

func withDeadline(ctx context.Context, req plugin.DriverRequest) (context.Context, context.CancelFunc, error) {
	if req.Deadline == "" {
		ctx, cancel := context.WithCancel(ctx)

		return ctx, cancel, nil
	}

	at, err := time.Parse(time.RFC3339, req.Deadline)
	if err != nil {
		return ctx, func() {}, fmt.Errorf("deadline: %w", err)
	}

	ctx, cancel := context.WithDeadline(ctx, at.Add(-deadlineMargin))

	return ctx, cancel, nil
}

func (j *job) one(ctx context.Context, sl plugin.DriverSlice) error {
	if j.req.Marks != nil {
		if err := j.mark(ctx, plugin.MarkStart, sl.Name); err != nil {
			return j.send([]*plugin.DriverEvent{failed(sl.Name, sl.Name, "no answer to its start mark: "+err.Error()+"\n")})
		}
	}

	var evs []*plugin.DriverEvent

	switch {
	case len(j.set.Checks) > 0:
		evs = []*plugin.DriverEvent{j.checks(ctx, sl)}
	case j.set.Functions != "":
		evs = []*plugin.DriverEvent{j.functions(ctx, sl)}
	default:
		evs = j.invoke(ctx, []plugin.DriverSlice{sl})
	}

	if j.req.Marks != nil {
		if err := j.mark(ctx, plugin.MarkStop, sl.Name); err != nil && evs[0] != nil {
			evs[0].Passed = false
			evs[0].Failures = append(evs[0].Failures, plugin.Failure{Test: sl.Name, Tail: "no answer to its stop mark: " + err.Error() + "\n"})
		}
	}

	return j.send(evs)
}

func (j *job) send(evs []*plugin.DriverEvent) error {
	for _, ev := range evs {
		if ev == nil {
			continue
		}

		if err := j.emit(*ev); err != nil {
			return err
		}
	}

	return nil
}

func (j *job) mark(ctx context.Context, which, slice string) error {
	m := j.req.Marks
	if len(m.Command) == 0 {
		return errors.New("the request's marks name no command") //nolint:err113 // said once, to the slice
	}

	argv := append(append([]string{}, m.Command...), which, slice)
	cmd := command(ctx, argv, j.req.Tree, environment(j.req, j.set, nil), newTail(j.log))

	if err := runCmd(cmd, "", nil); err != nil {
		return fmt.Errorf("%s %s: %w", which, slice, err)
	}

	return nil
}

func failed(slice, test, tail string) *plugin.DriverEvent {
	return &plugin.DriverEvent{Slice: slice, Event: plugin.EventEnd, Failures: []plugin.Failure{{Test: test, Tail: tail}}}
}

type outcome struct {
	runErr error
	out    *tail
	// step: the step's time ran out; limit: the invocation's did.
	step, limit, bundle bool
}

// decide prefers the result the script wrote over the exit status. Nil is
// no end.
func (j *job) decide(results string, sl plugin.DriverSlice, o outcome) *plugin.DriverEvent {
	if ev, ok := j.written(results, sl, o.out); ok {
		return ev
	}

	status, exited := exitStatus(o.runErr)

	switch {
	case o.step:
		// The step's time ran out: no end.
		return nil
	case o.bundle && o.runErr != nil:
		return nil
	case o.bundle:
		return &plugin.DriverEvent{Slice: sl.Name, Event: plugin.EventEnd, Passed: true}
	case o.limit:
		return failed(sl.Name, sl.Name, o.out.Tail()+fmt.Sprintf("the slice's time limit of %s ran out\n", j.set.limit))
	case !exited:
		return failed(sl.Name, sl.Name, o.out.Tail()+fmt.Sprintf("it did not run: %v\n", o.runErr))
	case status == 0:
		return &plugin.DriverEvent{Slice: sl.Name, Event: plugin.EventEnd, Passed: true}
	default:
		return failed(sl.Name, sl.Name, o.out.Tail())
	}
}

func (j *job) limitFor(ctx context.Context, n int) (context.Context, context.CancelFunc) {
	if j.set.limit == 0 {
		return context.WithCancel(ctx)
	}

	return context.WithTimeout(ctx, j.set.limit*time.Duration(n))
}

// A bundle that didn't exit 0 gives no end to a slice it left no result
// for, so the tool runs that slice again on its own.
func (j *job) invoke(ctx context.Context, slices []plugin.DriverSlice) []*plugin.DriverEvent {
	evs := make([]*plugin.DriverEvent, len(slices))

	j.seq++
	base := filepath.Join(j.req.Work, fmt.Sprintf("call-%d", j.seq))
	results := filepath.Join(base, "results")

	names := make([]string, 0, len(slices))
	for _, sl := range slices {
		names = append(names, sl.Name)
	}

	own := map[string]string{
		EnvSlices: strings.Join(names, "\n"), EnvSlicesFile: filepath.Join(base, "slices"), EnvResults: results,
	}

	if len(slices) == 1 {
		maps.Copy(own, sliceVars(j.req, slices[0]))
	}

	if err := j.layout(base, results, names, slices); err != nil {
		return everyFailed(slices, err.Error()+"\n")
	}

	args, err := argv(j.set, j.set.Script, filepath.Join(base, "script"))
	if err != nil {
		return everyFailed(slices, err.Error()+"\n")
	}

	lctx, cancel := j.limitFor(ctx, len(slices))
	defer cancel()

	out := newTail(j.log)
	cmd := command(lctx, args, workDir(j.req.Tree, j.set), environment(j.req, j.set, own), out)

	what := "the script of " + names[0]
	if len(names) > 1 {
		what = fmt.Sprintf("the script of %d slices", len(names))
	}

	start := time.Now()
	runErr := runCmd(cmd, what, j.log)
	took := time.Since(start).Seconds()

	o := outcome{runErr: runErr, out: out, step: ctx.Err() != nil, limit: lctx.Err() != nil, bundle: len(slices) > 1}

	for i, sl := range slices {
		ev := j.decide(results, sl, o)
		if ev == nil {
			continue
		}

		if !o.bundle {
			ev.Seconds = took
		}

		ev.Recorded = recorded(j.req, sl)
		evs[i] = ev
	}

	return evs
}

func everyFailed(slices []plugin.DriverSlice, why string) []*plugin.DriverEvent {
	out := make([]*plugin.DriverEvent, 0, len(slices))
	for _, sl := range slices {
		out = append(out, failed(sl.Name, sl.Name, why))
	}

	return out
}

func (j *job) layout(base, results string, names []string, slices []plugin.DriverSlice) error {
	if err := os.RemoveAll(base); err != nil {
		return fmt.Errorf("work: %w", err)
	}

	for _, n := range names {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(results, filepath.FromSlash(n))), 0o750); err != nil {
			return fmt.Errorf("results: %w", err)
		}
	}

	if err := os.WriteFile(filepath.Join(base, "slices"), []byte(strings.Join(names, "\n")+"\n"), 0o600); err != nil {
		return fmt.Errorf("slices: %w", err)
	}

	for _, sl := range slices {
		if j.req.Record && sl.Recording != "" {
			if err := os.MkdirAll(sl.Recording, 0o750); err != nil {
				return fmt.Errorf("recording: %w", err)
			}
		}
	}

	return nil
}

func sliceVars(req plugin.DriverRequest, sl plugin.DriverSlice) map[string]string {
	source := sl.Name
	if len(sl.Sources) > 0 {
		source = sl.Sources[0]
	}

	v := map[string]string{
		EnvSlice: sl.Name, EnvSource: filepath.Join(req.Tree, filepath.FromSlash(source)),
		EnvTests: strings.Join(sl.Tests, ","), EnvLabel: sl.Label, EnvTarget: sl.Target, EnvExtras: sl.Extras,
	}

	if req.Record && sl.Recording != "" {
		v[EnvRecording] = sl.Recording
	}

	return v
}

// A result file that can't be read fails the slice, saying why.
func (j *job) written(results string, sl plugin.DriverSlice, out *tail) (*plugin.DriverEvent, bool) {
	base := filepath.Join(results, filepath.FromSlash(sl.Name))

	if data, err := os.ReadFile(base + ".json"); err == nil { //nolint:gosec // the script's own result
		var r struct {
			Passed   bool             `json:"passed"`
			Tests    *int             `json:"tests"`
			Failures []plugin.Failure `json:"failures"`
		}

		if err := json.Unmarshal(data, &r); err != nil {
			return failed(sl.Name, sl.Name, fmt.Sprintf("its result file %s.json is no result: %v\n", sl.Name, err)), true
		}

		return &plugin.DriverEvent{Slice: sl.Name, Event: plugin.EventEnd, Passed: r.Passed && len(r.Failures) == 0, Tests: r.Tests, Failures: r.Failures}, true
	}

	if _, err := os.Stat(base + ".status"); err == nil {
		status, ok := readStatus(base + ".status")

		switch {
		case !ok:
			return failed(sl.Name, sl.Name, fmt.Sprintf("its status file %s.status holds no exit status\n", sl.Name)), true
		case status == 0:
			return &plugin.DriverEvent{Slice: sl.Name, Event: plugin.EventEnd, Passed: true}, true
		default:
			return failed(sl.Name, sl.Name, out.Tail()+fmt.Sprintf("exit status %d\n", status)), true
		}
	}

	return nil, false
}

func recorded(req plugin.DriverRequest, sl plugin.DriverSlice) bool {
	if !req.Record || sl.Recording == "" {
		return false
	}

	entries, err := os.ReadDir(sl.Recording)

	return err == nil && len(entries) > 0
}
