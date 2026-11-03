package driver

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

// harness gets the directory, the file's name and the function as $1, $2
// and $3.
const harness = `fail() { echo "$*"; exit 1; }
cd "$1" && . "./$2" && "$3"`

const indent = "     "

// testFunctions keeps the order the file defines them in.
func testFunctions(data []byte, prefix string) []string {
	re := regexp.MustCompile(`^(` + regexp.QuoteMeta(prefix) + `[A-Za-z0-9_]*)\(\)`)

	var names []string

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)

	for sc.Scan() {
		if m := re.FindStringSubmatch(sc.Text()); m != nil {
			names = append(names, m[1])
		}
	}

	return names
}

func (j *job) functions(ctx context.Context, sl plugin.DriverSlice) *plugin.DriverEvent {
	source := sl.Name
	if len(sl.Sources) > 0 {
		source = sl.Sources[0]
	}

	path := filepath.Join(j.req.Tree, filepath.FromSlash(source))
	start := time.Now()

	data, err := os.ReadFile(path) //nolint:gosec // the slice's own file
	if err != nil {
		ev := failed(sl.Name, sl.Name, "no such file\n")
		ev.Tests = new(0)

		return ev
	}

	if j.req.Record && sl.Recording != "" {
		if err := os.MkdirAll(sl.Recording, 0o750); err != nil {
			return failed(sl.Name, sl.Name, fmt.Sprintf("recording: %v\n", err))
		}
	}

	names := testFunctions(data, j.set.Functions)
	env := environment(j.req, j.set, sliceVars(j.req, sl))

	fctx, cancel := j.limitFor(ctx, 1)
	defer cancel()

	ev := &plugin.DriverEvent{Slice: sl.Name, Event: plugin.EventEnd, Tests: new(len(names))}

	for _, fn := range names {
		f, ran := j.function(ctx, fctx, path, fn, sl.Name, env)

		switch {
		case !ran:
			// The step's time ran out: no end.
			return nil
		case f != nil:
			ev.Failures = append(ev.Failures, *f)
		}

		if fctx.Err() != nil {
			break
		}
	}

	ev.Passed = len(ev.Failures) == 0
	ev.Seconds = time.Since(start).Seconds()
	ev.Recorded = recorded(j.req, sl)

	return ev
}

func (j *job) function(ctx, fctx context.Context, path, fn, slice string, env []string) (*plugin.Failure, bool) {
	var out bytes.Buffer

	args := append(append([]string{}, j.set.Shell...), "-c", harness, "shell-driver", filepath.Dir(path), filepath.Base(path), fn)
	runErr := runCmd(command(fctx, args, filepath.Dir(path), env, &out), fn+" of "+slice, j.log)

	switch {
	case ctx.Err() != nil:
		return nil, false
	case runErr == nil:
		sayf(j.log, "ok   %s %s\n", slice, fn)

		return nil, true
	}

	text := strings.TrimRight(out.String(), "\n")
	if fctx.Err() != nil {
		text += fmt.Sprintf("\nthe slice's time limit of %s ran out", j.set.limit)
	}

	sayf(j.log, "FAIL %s %s\n", slice, fn)

	for line := range strings.SplitSeq(text, "\n") {
		sayf(j.log, "%s%s\n", indent, line)
	}

	return &plugin.Failure{Test: fn, Tail: lastLines(text)}, true
}

func lastLines(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) > tailLines {
		lines = lines[len(lines)-tailLines:]
	}

	return strings.Join(lines, "\n") + "\n"
}
