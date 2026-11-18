// Package bep reads results from the build event file alone, never from the output tree,
// which keeps files from earlier invocations.
package bep

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

type Result struct {
	Label  string
	Status string
	// Cached is set when every part was replayed from a cache.
	Cached bool
	// Parts are the shards' and runs' last attempts, in order.
	Parts   []Part
	Seconds float64
}

type Part struct {
	Shard, Run, Attempt int
	Status              string
	Cached              bool
	// Log, XML and LCOV are local paths; empty when the event names none or
	// only a remote one (bytestream://).
	Log, XML, LCOV string
	Remote         []string
	Seconds        float64
}

func (r Result) Passed() bool { return r.Status == "PASSED" || r.Status == "FLAKY" }

type Invocation struct {
	// Results holds only targets whose test summary the file holds, so a
	// target cut short in one shard has none.
	Results map[string]Result
	// Failed are the targets that didn't build, by label, with why.
	Failed map[string]string
	// Complete is set when the file holds its last message: a server that died
	// leaves a file without it.
	Complete bool
}

// brokenTarget are the aborts that are the target's own fault. Any other
// abort says nothing of the target, which then has no result.
var brokenTarget = map[string]bool{"LOADING_FAILURE": true, "ANALYSIS_FAILURE": true}

var ErrRead = errors.New("build events")

type event struct {
	ID struct {
		TestResult *struct {
			Label   string `json:"label"`
			Run     int    `json:"run"`
			Shard   int    `json:"shard"`
			Attempt int    `json:"attempt"`
		} `json:"testResult"` //nolint:tagliatelle // the field name Bazel writes
		TestSummary *struct {
			Label string `json:"label"`
		} `json:"testSummary"` //nolint:tagliatelle // the field name Bazel writes
		TargetCompleted *struct {
			Label string `json:"label"`
		} `json:"targetCompleted"` //nolint:tagliatelle // the field name Bazel writes
	} `json:"id"`
	TestResult *struct {
		Status           string `json:"status"`
		CachedLocally    bool   `json:"cachedLocally"`             //nolint:tagliatelle // the field name Bazel writes
		TestAttemptDurMs string `json:"testAttemptDurationMillis"` //nolint:tagliatelle // the field name Bazel writes
		TestAttemptDur   string `json:"testAttemptDuration"`       //nolint:tagliatelle // the field name Bazel writes
		ExecutionInfo    *struct {
			CachedRemotely bool `json:"cachedRemotely"` //nolint:tagliatelle // the field name Bazel writes
		} `json:"executionInfo"` //nolint:tagliatelle // the field name Bazel writes
		TestActionOutput []struct {
			Name string `json:"name"`
			URI  string `json:"uri"`
		} `json:"testActionOutput"` //nolint:tagliatelle // the field name Bazel writes
	} `json:"testResult"` //nolint:tagliatelle // the field name Bazel writes
	TestSummary *struct {
		OverallStatus string `json:"overallStatus"` //nolint:tagliatelle // the field name Bazel writes
	} `json:"testSummary"` //nolint:tagliatelle // the field name Bazel writes
	Completed *struct {
		Success bool `json:"success"`
		Failure *struct {
			Message string `json:"message"`
		} `json:"failureDetail"` //nolint:tagliatelle // the field name Bazel writes
	} `json:"completed"`
	Aborted *struct {
		Reason      string `json:"reason"`
		Description string `json:"description"`
	} `json:"aborted"`
	LastMessage bool `json:"lastMessage"` //nolint:tagliatelle // the field name Bazel writes
}

// ReadFile reads as far as the file is whole: a server that died may cut
// it off mid-line.
func ReadFile(path string) (Invocation, error) {
	f, err := os.Open(path) //nolint:gosec // the invocation's own event file
	if err != nil {
		return Invocation{}, fmt.Errorf("%w: %w", ErrRead, err)
	}
	defer f.Close() //nolint:errcheck // read only

	return Read(f)
}

func Read(r io.Reader) (Invocation, error) {
	inv := Invocation{Results: map[string]Result{}, Failed: map[string]string{}}
	parts := map[string]map[[2]int]Part{}
	summaries := map[string]string{}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)

	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}

		var e event
		if err := json.Unmarshal(line, &e); err != nil {
			// A last line cut off: what came before stands.
			break
		}

		switch {
		case e.ID.TestResult != nil && e.TestResult != nil:
			addPart(parts, e)
		case e.ID.TestSummary != nil && e.TestSummary != nil:
			summaries[e.ID.TestSummary.Label] = e.TestSummary.OverallStatus
		case e.ID.TargetCompleted != nil && e.Completed != nil && !e.Completed.Success:
			why := "the target failed to build"
			if e.Completed.Failure != nil && e.Completed.Failure.Message != "" {
				why = e.Completed.Failure.Message
			}

			inv.Failed[e.ID.TargetCompleted.Label] = why
		case e.ID.TargetCompleted != nil && e.Aborted != nil && brokenTarget[e.Aborted.Reason]:
			inv.Failed[e.ID.TargetCompleted.Label] = strings.TrimSpace(e.Aborted.Reason + ": " + e.Aborted.Description)
		}

		if e.LastMessage {
			inv.Complete = true
		}
	}

	// A later shard that passed never hides an earlier one that failed.
	for label, status := range summaries {
		inv.Results[label] = result(label, status, parts[label])
	}

	if err := sc.Err(); err != nil {
		return inv, fmt.Errorf("%w: %w", ErrRead, err)
	}

	return inv, nil
}

func result(label, status string, parts map[[2]int]Part) Result {
	r := Result{Label: label, Status: status, Cached: len(parts) > 0}

	keys := make([][2]int, 0, len(parts))
	for k := range parts {
		keys = append(keys, k)
	}

	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}

		return keys[i][1] < keys[j][1]
	})

	for _, k := range keys {
		p := parts[k]
		r.Parts = append(r.Parts, p)
		r.Seconds += p.Seconds
		r.Cached = r.Cached && p.Cached
	}

	return r
}

func addPart(parts map[string]map[[2]int]Part, e event) {
	id, tr := e.ID.TestResult, e.TestResult

	p := Part{Shard: id.Shard, Run: id.Run, Attempt: id.Attempt, Status: tr.Status,
		Cached: tr.CachedLocally || tr.ExecutionInfo != nil && tr.ExecutionInfo.CachedRemotely}
	p.Seconds = seconds(tr.TestAttemptDur, tr.TestAttemptDurMs)

	for _, o := range tr.TestActionOutput {
		path, local := localPath(o.URI)
		if !local {
			p.Remote = append(p.Remote, o.Name)

			continue
		}

		switch o.Name {
		case "test.log":
			p.Log = path
		case "test.xml":
			p.XML = path
		case "test.lcov":
			p.LCOV = path
		}
	}

	if parts[id.Label] == nil {
		parts[id.Label] = map[[2]int]Part{}
	}

	key := [2]int{id.Shard, id.Run}
	if have, ok := parts[id.Label][key]; ok && have.Attempt > p.Attempt {
		return
	}

	parts[id.Label][key] = p
}

func localPath(uri string) (string, bool) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return "", false
	}

	return u.Path, true
}

func seconds(dur, ms string) float64 {
	if d, err := time.ParseDuration(dur); err == nil {
		return d.Seconds()
	}

	var n float64
	if _, err := fmt.Sscan(ms, &n); err == nil {
		return n / 1000
	}

	return 0
}
