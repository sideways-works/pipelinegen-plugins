package driver

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"
)

type event struct {
	Action  string  `json:"Action"`  //nolint:tagliatelle // test2json's field names
	Package string  `json:"Package"` //nolint:tagliatelle // test2json's field names
	Test    string  `json:"Test"`    //nolint:tagliatelle // test2json's field names
	Output  string  `json:"Output"`  //nolint:tagliatelle // test2json's field names
	Elapsed float64 `json:"Elapsed"` //nolint:tagliatelle // test2json's field names
}

const actionFail = "fail"

const tailLines = 15

type testState struct {
	done, failed, skipped bool
	elapsed               float64
	out                   []string
}

type outcome struct {
	tests map[string]*testState
	// out is output outside any test: a panic before the first test,
	// TestMain's, the build's.
	out []string
	// failed says the package failed as a whole.
	buildFailed, cached, failed bool
}

func newOutcome() *outcome { return &outcome{tests: map[string]*testState{}} }

func keep(lines []string, line string) []string {
	lines = append(lines, strings.TrimRight(line, "\n"))
	// Trimmed in bulk, so an append stays cheap.
	if len(lines) > 4*tailLines {
		lines = lines[len(lines)-tailLines:]
	}

	return lines
}

func last(lines []string) string {
	if len(lines) > tailLines {
		lines = lines[len(lines)-tailLines:]
	}

	return strings.Join(lines, "\n")
}

func (o *outcome) read(r io.Reader, log io.Writer) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)

	for sc.Scan() {
		var ev event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil || ev.Action == "" {
			// Not an event: the build's or a toolchain's own line.
			o.out = keep(o.out, sc.Text())
			sayf(log, "%s\n", sc.Text())

			continue
		}

		o.fold(ev, log)
	}
}

func (o *outcome) fold(ev event, log io.Writer) {
	if ev.Output != "" {
		sayf(log, "%s", ev.Output)
	}

	top, _, _ := strings.Cut(ev.Test, "/")

	switch {
	case ev.Action == "build-output":
		o.out = keep(o.out, ev.Output)
	case ev.Action == "build-fail":
		o.buildFailed = true
	case top == "":
		if ev.Action == actionFail {
			o.failed = true
		}

		if ev.Action == "output" {
			o.out = keep(o.out, ev.Output)

			if strings.Contains(ev.Output, "(cached)") {
				o.cached = true
			}
		}
	default:
		st := o.tests[top]
		if st == nil {
			st = &testState{}
			o.tests[top] = st
		}

		switch ev.Action {
		case "output":
			st.out = keep(st.out, ev.Output)
		case "pass", "skip", actionFail:
			// A subtest's end is not its parent's.
			if ev.Test != top {
				if ev.Action == actionFail {
					st.failed = true
				}

				break
			}

			st.done, st.elapsed = true, ev.Elapsed
			st.failed = st.failed || ev.Action == actionFail
			st.skipped = ev.Action == "skip"
		}
	}
}

func (o *outcome) anyFailed() bool {
	for _, st := range o.tests {
		if st.failed {
			return true
		}
	}

	return false
}

func (o *outcome) allDone() bool {
	for _, st := range o.tests {
		if !st.done {
			return false
		}
	}

	return true
}

// end returns false when a test never reported and none failed: the
// process died first.
func end(sl plugin.DriverSlice, outs []*outcome, must holds) (plugin.DriverEvent, bool) {
	ev := plugin.DriverEvent{Slice: sl.Name, Event: plugin.EventEnd, Failures: []plugin.Failure{}}

	for _, o := range outs {
		if o.buildFailed {
			ev.Failures = append(ev.Failures, plugin.Failure{Test: "(build)", Tail: last(o.out)})
		}

		ev.Replayed = ev.Replayed || o.cached
	}

	if len(ev.Failures) > 0 {
		return ev, true
	}

	ran, complete := 0, true

	for _, name := range sl.Tests {
		var st *testState

		for _, o := range outs {
			if s := o.tests[name]; s != nil && s.done {
				st = s
			}
		}

		if st == nil {
			complete = false

			continue
		}

		ran++
		ev.Seconds += st.elapsed

		if st.failed {
			ev.Failures = append(ev.Failures, plugin.Failure{Test: name, Tail: last(st.out)})
		} else if e, ok := must.match(sl, name); ok && st.skipped {
			ev.Failures = append(ev.Failures, e.skipped(name, st.out))
		}
	}

	ev.Failures = append(ev.Failures, must.unheld(sl)...)

	ev.Tests = &ran

	// A package that failed though every test passed failed as a whole
	// (TestMain, an exit after the tests), unless a test never reported, which
	// explains the failure by the process's death.
	for _, o := range outs {
		if o.failed && len(ev.Failures) == 0 && complete && o.allDone() && !o.anyFailed() {
			ev.Failures = append(ev.Failures, plugin.Failure{Test: "(package)", Tail: last(o.out)})
		}
	}

	switch {
	case len(ev.Failures) > 0:
		return ev, true
	case !complete:
		return ev, false
	default:
		ev.Passed = true

		return ev, true
	}
}
