// Package driver runs a batch's slices with Bazel. Each slice's result comes
// from the invocation's build event file alone; a slice it gave no result
// gets none, and the caller runs it again alone.
package driver

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen-driver-bazel/internal/bazel"
	"github.com/sideways-works/pipelinegen-driver-bazel/internal/bep"
	"github.com/sideways-works/pipelinegen-driver-bazel/internal/boundary"
	"github.com/sideways-works/pipelinegen-driver-bazel/internal/junit"
)

// Slice is one slice to run: a test target, or some tests of one.
type Slice struct {
	Name  string
	Label string
	// Kind is the target's rule kind (go_test), which says how its tests are
	// filtered.
	Kind  string
	Tests []string
	// Extras receives test.log and test.xml; Recording, the coverage.
	Extras, Recording string
}

// Settings are the step's with block and what the request adds to it.
type Settings struct {
	Batch   bool
	Startup []string
	Flags   []string
	// Instrument defaults to every package of the workspace.
	Instrument string
	// TestEnv names the set-up variables that reach the tests.
	TestEnv []string
	// Timeout is a test's limit in seconds, Bazel's own by default.
	Timeout int
}

type With struct {
	Startup    []string `json:"startup"`
	Flags      []string `json:"flags"`
	Instrument string   `json:"instrument"`
	Timeout    int      `json:"timeout"`
}

func (*With) settingNoun() string { return "bazel-driver's settings" }

// moved names where each refused key's setting lives.
var moved = map[string]string{
	"batch":    "a step's batch sits beside with, not in it: {run: bazel, batch: true, with: {...}}",
	"bazel":    "the driver runs the bazelisk pipelinegen hands it with the batch's tools, or else the bazel on PATH; with names no binary",
	"test_env": "the variables set-up steps export reach the tests by themselves; pass any other with flags: [--test_env=NAME]",
}

var errSettings = errors.New("settings")

// SettingsOf reads a step's with block strictly: a setting the driver
// ignored would leave the step running without it, unseen.
func SettingsOf(conf map[string]any) (Settings, error) {
	var w With

	for _, k := range []string{"batch", "bazel", "test_env"} {
		if _, ok := conf[k]; ok {
			return Settings{}, fmt.Errorf("%w: %s: %s", errSettings, k, moved[k])
		}
	}

	if err := strictly(conf, &w); err != nil {
		return Settings{}, err
	}

	_, timeout := conf["timeout"]
	_, instrument := conf["instrument"]

	switch {
	case timeout && w.Timeout < 1:
		return Settings{}, fmt.Errorf("%w: timeout: %d is less than 1 second; leave timeout out for Bazel's own", errSettings, w.Timeout)
	case instrument && w.Instrument == "":
		return Settings{}, fmt.Errorf("%w: instrument is empty; leave it out for every package of the workspace", errSettings)
	}

	return Settings{Startup: w.Startup, Flags: w.Flags, Instrument: w.Instrument, Timeout: w.Timeout}, nil
}

type Request struct {
	Workspace string
	// Root is Bazel's output root, kept between a worker's batches so its
	// server and outputs are reused.
	Root   string
	Work   string
	Record bool
	// Stack is set for a suite whose tests run against servers a set-up step
	// started.
	Stack bool
	// Marks, in a recording run of such a suite, is the marks directory; Self
	// is this executable, the tests' --run_under prefix.
	Marks, Self string
	Slices      []Slice
	Settings    Settings
	Env         []string
}

type Failure struct {
	Test string `json:"test"`
	Tail string `json:"tail"`
}

type Result struct {
	Slice    string    `json:"slice"`
	Passed   bool      `json:"passed"`
	Tests    int       `json:"tests"`
	Failures []Failure `json:"failures,omitempty"`
	Seconds  float64   `json:"seconds"`
	// Status is Bazel's word for the target, FAILED_TO_BUILD, or UNFILTERED
	// for tests the target's rule can't be told to filter.
	Status   string `json:"status"`
	Replayed bool   `json:"replayed,omitempty"`
	Recorded bool   `json:"recorded,omitempty"`
}

const tailLines = 30

var ErrRun = errors.New("bazel driver")

// Run hands each result to emit as its invocation ends. A slice without a
// result gets none here: the caller runs it again on its own.
func Run(ctx context.Context, bz bazel.Runner, req Request, emit func(Result)) error {
	if err := os.MkdirAll(req.Work, 0o750); err != nil {
		return fmt.Errorf("%w: %w", ErrRun, err)
	}

	r := &run{bz: bz, req: req, emit: emit}

	var runnable []Slice

	for _, sl := range req.Slices {
		if _, ok := testFilter(sl); !ok {
			emit(Result{Slice: sl.Name, Status: "UNFILTERED", Failures: []Failure{{
				Test: sl.Label, Tail: "the slice names some tests of a " + sl.Kind + " target, and the driver can't tell such a rule which: it runs no test rather than all of them",
			}}})

			continue
		}

		runnable = append(runnable, sl)
	}

	if req.Marks != "" && remote(req.Settings.Flags) {
		// Remote tests can't reach the marks directory, so no recording could be
		// told apart.
		for _, sl := range runnable {
			emit(Result{Slice: sl.Name, Status: "UNMARKED", Failures: []Failure{{
				Test: sl.Label, Tail: "a recording run of a stack suite marks each test's start and stop from inside its action, which remote execution runs elsewhere: run it locally",
			}}})
		}

		return nil
	}

	for _, group := range groups(runnable, req.Settings.Batch || req.Marks != "") {
		// Past the deadline, what didn't run gets no result.
		if ctx.Err() != nil {
			return nil //nolint:nilerr // a deadline is no failure of the driver
		}

		if err := r.invoke(ctx, group); err != nil {
			return err
		}
	}

	return nil
}

func remote(flags []string) bool {
	return slices.ContainsFunc(flags, func(f string) bool { return strings.HasPrefix(f, "--remote_executor") })
}

func testFilter(sl Slice) (string, bool) {
	if len(sl.Tests) == 0 {
		return "", true
	}

	// rules_go hands the filter to -test.run; other rules vary.
	if sl.Kind != "go_test" {
		return "", false
	}

	quoted := make([]string, 0, len(sl.Tests))
	for _, t := range sl.Tests {
		quoted = append(quoted, regexp.QuoteMeta(t))
	}

	return "^(" + strings.Join(quoted, "|") + ")$", true
}

func groups(sls []Slice, batch bool) [][]Slice {
	if !batch {
		out := make([][]Slice, 0, len(sls))
		for _, sl := range sls {
			out = append(out, []Slice{sl})
		}

		return out
	}

	// Bazel's test filter holds for the whole invocation and a target runs once
	// in it, so slices with different filters can't share one.
	by := map[string][]Slice{}

	var order []string

	for _, sl := range sls {
		f, _ := testFilter(sl)
		if _, ok := by[f]; !ok {
			order = append(order, f)
		}

		by[f] = append(by[f], sl)
	}

	out := make([][]Slice, 0, len(order))
	for _, f := range order {
		out = append(out, by[f])
	}

	return out
}

type run struct {
	bz   bazel.Runner
	req  Request
	emit func(Result)
	n    int
}

func (r *run) invoke(ctx context.Context, sls []Slice) error {
	r.n++

	filter, _ := testFilter(sls[0])

	inv := bazel.Invocation{
		Dir: r.req.Workspace, Record: r.req.Record, Filter: r.req.Settings.Instrument,
		TestFilter: filter, Events: filepath.Join(r.req.Work, fmt.Sprintf("events-%d.json", r.n)),
		Startup: slices.Concat([]string{"--output_user_root=" + r.req.Root}, r.req.Settings.Startup),
		Flags:   r.req.Settings.Flags, TestEnv: r.req.Settings.TestEnv, Timeout: r.req.Settings.Timeout,
		Log: filepath.Join(r.req.Work, fmt.Sprintf("bazel-%d.log", r.n)), Env: r.req.Env,
	}

	for _, sl := range sls {
		if !slices.Contains(inv.Targets, sl.Label) {
			inv.Targets = append(inv.Targets, sl.Label)
		}
	}

	if r.req.Stack {
		// The stack's code is no input of the test's action, so a replayed result
		// could be a pass from before the server changed.
		inv.Flags = append(slices.Clone(inv.Flags), "--nocache_test_results")
	}

	if r.req.Marks != "" {
		if err := r.marked(&inv, sls); err != nil {
			return err
		}
	}

	// A file an earlier invocation left must not be read as this one's.
	os.Remove(inv.Events) //nolint:errcheck // absent is fine

	if err := r.bz.Run(ctx, inv); err != nil {
		return fmt.Errorf("%w: %w", ErrRun, err)
	}

	events, err := bep.ReadFile(inv.Events)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %w", ErrRun, err)
	}

	for _, sl := range sls {
		label := normal(sl.Label)

		if res, ok := find(events.Results, label); ok {
			out, err := r.result(sl, res)
			if err != nil {
				return err
			}

			r.emit(out)

			continue
		}

		if why, ok := find(events.Failed, label); ok {
			r.emit(Result{Slice: sl.Name, Status: "FAILED_TO_BUILD", Failures: []Failure{{Test: sl.Label, Tail: why}}})
		}
	}

	return nil
}

// marked runs tests one at a time, each wrapped by the boundary verb.
func (r *run) marked(inv *bazel.Invocation, sls []Slice) error {
	byLabel := map[string]string{}
	for _, sl := range sls {
		byLabel[normal(sl.Label)] = sl.Name
	}

	names := filepath.Join(r.req.Work, fmt.Sprintf("mark-slices-%d.json", r.n))
	if err := boundary.WriteSlices(names, byLabel); err != nil {
		return fmt.Errorf("%w: %w", ErrRun, err)
	}

	inv.Flags = append(inv.Flags, "--local_test_jobs=1",
		"--run_under="+r.req.Self+" boundary --",
		"--sandbox_writable_path="+r.req.Marks)
	inv.TestEnv = append(slices.Clone(inv.TestEnv), boundary.EnvDir, boundary.EnvSlices)
	inv.Env = append(slices.Clone(inv.Env), boundary.EnvDir+"="+r.req.Marks, boundary.EnvSlices+"="+names)

	return nil
}

// result copies the files out at once: the next invocation may replace them
// in Bazel's tree. One part without coverage leaves the slice unrecorded.
func (r *run) result(sl Slice, res bep.Result) (Result, error) {
	out := Result{Slice: sl.Name, Passed: res.Passed(), Status: res.Status, Replayed: res.Cached, Seconds: res.Seconds}

	if err := r.keepFiles(sl, res); err != nil {
		return out, err
	}

	if r.req.Record && sl.Recording != "" {
		recorded, err := keepCoverage(sl.Recording, res.Parts)
		if err != nil {
			return out, err
		}

		out.Recorded = recorded
	}

	var failedLogs []string

	for _, p := range res.Parts {
		if p.XML != "" {
			cases, _ := junit.ReadFile(p.XML) //nolint:errcheck // an unreadable test.xml leaves the log's tail
			for _, c := range cases {
				out.Tests++

				if c.Failed {
					out.Failures = append(out.Failures, Failure{Test: c.Name, Tail: lastLines(c.Message, tailLines)})
				}
			}
		}

		if p.Status != "PASSED" && p.Log != "" {
			failedLogs = append(failedLogs, p.Log)
		}
	}

	if !out.Passed && len(out.Failures) == 0 {
		tail := "Bazel says " + res.Status

		for _, log := range failedLogs {
			if t, err := fileTail(log, tailLines); err == nil && t != "" {
				tail = t

				break
			}
		}

		out.Failures = []Failure{{Test: sl.Label, Tail: tail}}
	}

	return out, nil
}

func (r *run) keepFiles(sl Slice, res bep.Result) error {
	if sl.Extras == "" {
		return nil
	}

	if err := os.MkdirAll(sl.Extras, 0o750); err != nil {
		return fmt.Errorf("%w: %w", ErrRun, err)
	}

	for _, p := range res.Parts {
		suffix := ""
		if len(res.Parts) > 1 {
			suffix = fmt.Sprintf(".%d.%d", p.Shard, p.Run)
		}

		for ext, from := range map[string]string{".log": p.Log, ".xml": p.XML} {
			if from != "" {
				if err := copyFile(from, filepath.Join(sl.Extras, "test"+suffix+ext)); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func keepCoverage(dir string, parts []bep.Part) (bool, error) {
	if len(parts) == 0 || slices.ContainsFunc(parts, func(p bep.Part) bool { return p.LCOV == "" }) {
		return false, nil
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return false, fmt.Errorf("%w: %w", ErrRun, err)
	}

	out, err := os.Create(filepath.Join(dir, "coverage.lcov")) //nolint:gosec // the slice's own directory
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrRun, err)
	}

	for _, p := range parts {
		if err := appendFile(out, p.LCOV); err != nil {
			out.Close() //nolint:errcheck // failing anyway

			return false, err
		}
	}

	if err := out.Close(); err != nil {
		return false, fmt.Errorf("%w: %w", ErrRun, err)
	}

	return true, nil
}

func appendFile(out io.Writer, from string) error {
	in, err := os.Open(from) //nolint:gosec // a file Bazel's event names
	if err != nil {
		return fmt.Errorf("%w: %w", ErrRun, err)
	}
	defer in.Close() //nolint:errcheck // read only

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("%w: %w", ErrRun, err)
	}

	return nil
}

func normal(label string) string {
	for _, p := range []string{"@@//", "@//"} {
		if rest, ok := strings.CutPrefix(label, p); ok {
			return "//" + rest
		}
	}

	return label
}

func find[V any](m map[string]V, label string) (V, bool) {
	for k, v := range m {
		if normal(k) == label {
			return v, true
		}
	}

	var zero V

	return zero, false
}

func copyFile(from, to string) error {
	in, err := os.Open(from) //nolint:gosec // a file Bazel's event names
	if err != nil {
		return fmt.Errorf("%w: %w", ErrRun, err)
	}
	defer in.Close() //nolint:errcheck // read only

	out, err := os.Create(to) //nolint:gosec // the slice's own directory
	if err != nil {
		return fmt.Errorf("%w: %w", ErrRun, err)
	}

	if _, err := io.Copy(out, in); err != nil {
		out.Close() //nolint:errcheck // failing anyway

		return fmt.Errorf("%w: %w", ErrRun, err)
	}

	if err := out.Close(); err != nil {
		return fmt.Errorf("%w: %w", ErrRun, err)
	}

	return nil
}

func fileTail(path string, n int) (string, error) {
	f, err := os.Open(path) //nolint:gosec // a test's log
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrRun, err)
	}
	defer f.Close() //nolint:errcheck // read only

	var lines []string

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<24)

	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}

	return strings.Join(lines, "\n"), nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	return strings.Join(lines, "\n")
}
