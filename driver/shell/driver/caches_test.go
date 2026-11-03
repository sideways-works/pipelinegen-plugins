package driver

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

func file(t *testing.T, path string, mb int64, age time.Duration) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Truncate(path, mb<<20); err != nil {
		t.Fatal(err)
	}

	at := time.Now().Add(-age)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}

func cachesConf(script string, caches ...map[string]any) map[string]any {
	list := make([]any, 0, len(caches))
	for _, c := range caches {
		list = append(list, c)
	}

	return map[string]any{"script": script, "caches": list}
}

func TestACacheOfFilesLosesItsOldestFirst(t *testing.T) {
	conf := cachesConf(`test ! -e "$BUILD/old" && test ! -e "$BUILD/older" && test -e "$BUILD/new" && echo "cache at $BUILD"`,
		map[string]any{"dir": "build", "env": "BUILD", "mb": 1, "age": "7d"})
	req := request(t, conf, "a")
	dir := filepath.Join(req.Cache, "build")

	file(t, filepath.Join(dir, "x", "older"), 1, 10*day)
	file(t, filepath.Join(dir, "y", "old"), 1, 3*day)
	file(t, filepath.Join(dir, "new"), 1, time.Minute)

	got, log := runOf(t, req)

	if !got["a"].Passed {
		t.Errorf("the script saw the cache before its trim: %+v\n%s", got["a"], log)
	}

	if !strings.Contains(log, "cache at "+dir) || !strings.Contains(log, "shell-driver: cache build: 3 MB (cap 1 MB, unused for 7d goes); 2 MB in 2 files removed") {
		t.Errorf("the log:\n%s", log)
	}
}

func TestACacheOfEntriesGoesByEachEntrysNewestUse(t *testing.T) {
	conf := cachesConf("true", map[string]any{"dir": "tools", "age": "7d", "unit": "entries"})
	req := request(t, conf, "a")
	dir := filepath.Join(req.Cache, "tools")

	file(t, filepath.Join(dir, "v1", "bin", "lint"), 1, 30*day)
	file(t, filepath.Join(dir, "v2", "bin", "lint"), 1, 30*day)
	file(t, filepath.Join(dir, "v3", "bin", "lint"), 1, 30*day)

	for _, d := range []string{"v1", "v1/bin", "v3", "v3/bin"} {
		at := time.Now().Add(-30 * day)
		if err := os.Chtimes(filepath.Join(dir, d), at, at); err != nil {
			t.Fatal(err)
		}
	}

	// v2 was reused yesterday: its directory touched.
	at := time.Now().Add(-day)
	if err := os.Chtimes(filepath.Join(dir, "v2"), at, at); err != nil {
		t.Fatal(err)
	}

	runOf(t, req)

	if exists(filepath.Join(dir, "v1")) || !exists(filepath.Join(dir, "v2", "bin", "lint")) || exists(filepath.Join(dir, "v3")) {
		t.Errorf("v1 %v, v2 %v, v3 %v: want only v2", exists(filepath.Join(dir, "v1")), exists(filepath.Join(dir, "v2")), exists(filepath.Join(dir, "v3")))
	}
}

func TestAWholeCacheGoesByItsStamp(t *testing.T) {
	conf := cachesConf("true", map[string]any{"dir": "mod", "env": "GOMODCACHE", "mb": 3, "age": "7d", "unit": "whole"})
	req := request(t, conf, "a")
	dir := filepath.Join(req.Cache, "mod")

	file(t, filepath.Join(dir, "example.com", "m@v1", "a.go"), 1, 300*day)
	file(t, filepath.Join(dir, stampName), 0, day)

	runOf(t, req)

	if !exists(filepath.Join(dir, "example.com", "m@v1", "a.go")) {
		t.Fatal("a cache used yesterday went for files a year old")
	}

	if info, err := os.Stat(filepath.Join(dir, stampName)); err != nil || time.Since(info.ModTime()) > time.Minute {
		t.Fatalf("the step didn't note its use: %v", err)
	}

	// Read-only, as Go's module cache is, and over its cap.
	file(t, filepath.Join(dir, "example.com", "m@v2", "a.go"), 4, 0)

	for _, d := range []string{"example.com/m@v1", "example.com/m@v2", "example.com"} {
		if err := os.Chmod(filepath.Join(dir, d), 0o500); err != nil { //nolint:gosec // read-only, as Go's module cache is
			t.Fatal(err)
		}
	}

	req.Work = t.TempDir() // the next batch

	got, log := runOf(t, req)
	if !got["a"].Passed || exists(filepath.Join(dir, "example.com")) || !strings.Contains(log, "5 MB in 1 whole cache removed") {
		t.Errorf("a whole cache over its cap: %+v\n%s", got["a"], log)
	}

	file(t, filepath.Join(dir, "example.com", "m@v1", "a.go"), 1, 0)
	file(t, filepath.Join(dir, stampName), 0, 8*day)

	req.Work = t.TempDir() // the next batch

	runOf(t, req)

	if exists(filepath.Join(dir, "example.com")) {
		t.Error("a cache unused for 8 days, by its stamp, was kept")
	}
}

func TestATrimThatFailsFailsNoStep(t *testing.T) {
	was := removeAll
	removeAll = func(string) error { return errors.New("operation not permitted") } //nolint:err113 // the test's own

	t.Cleanup(func() { removeAll = was })

	req := request(t, cachesConf("true", map[string]any{"dir": "build", "age": "1d"}), "a")
	file(t, filepath.Join(req.Cache, "build", "old"), 1, 2*day)

	got, log := runOf(t, req)
	if !got["a"].Passed || !strings.Contains(log, "shell-driver: cache build: 1 not removed; first: operation not permitted") {
		t.Errorf("%+v\n%s", got["a"], log)
	}
}

func TestNoTrimWhenTheStepsTimeIsNearlyGone(t *testing.T) {
	req := request(t, cachesConf(`test -d "$BUILD"`, map[string]any{"dir": "build", "env": "BUILD", "age": "1d"}), "a")
	req.Deadline = time.Now().Add(deadlineMargin + 30*time.Second).UTC().Format(time.RFC3339)
	file(t, filepath.Join(req.Cache, "build", "old"), 1, 2*day)

	got, log := runOf(t, req)
	if !got["a"].Passed || !strings.Contains(log, "the step's time is nearly gone: its caches are not trimmed this time") || !exists(filepath.Join(req.Cache, "build", "old")) {
		t.Errorf("%+v\n%s", got["a"], log)
	}
}

func TestAPrepareAndASetupTrimFirst(t *testing.T) {
	conf := cachesConf(`test ! -e "$BUILD/old"`, map[string]any{"dir": "build", "env": "BUILD", "age": "1d"})

	req := request(t, conf)
	req.Prepared = t.TempDir()
	file(t, filepath.Join(req.Cache, "build", "old"), 1, 2*day)

	var log bytes.Buffer
	if err := prepare(t.Context(), req, &log); err != nil || !strings.Contains(log.String(), "shell-driver: cache build: 1 MB") {
		t.Errorf("prepare: %v\n%s", err, log.String())
	}

	req = request(t, conf)
	file(t, filepath.Join(req.Cache, "build", "old"), 1, 2*day)

	stop := make(chan struct{})
	close(stop)

	var setupLog lockedLog
	if err := setup(t.Context(), req, func(map[string]string) error { return nil }, stop, &setupLog); err != nil || !strings.Contains(setupLog.String(), "shell-driver: cache build: 1 MB") {
		t.Errorf("setup: %v\n%s", err, setupLog.String())
	}
}

func TestCachesRefused(t *testing.T) {
	cases := []struct {
		cache map[string]any
		want  string
	}{
		{map[string]any{"mb": 1}, "has no dir"},
		{map[string]any{"dir": "../up", "mb": 1}, "no directory inside"},
		{map[string]any{"dir": "/abs", "mb": 1}, "no directory inside"},
		{map[string]any{"dir": "a"}, "neither mb nor age"},
		{map[string]any{"dir": "a", "mb": -1}, "caches[0].mb (a): -1 is less than 0"},
		{map[string]any{"dir": "a", "age": "soon"}, "no positive duration"},
		{map[string]any{"dir": "a", "age": "-6h"}, "no positive duration"},
		{map[string]any{"dir": "a", "age": "0d"}, "no positive duration"},
		{map[string]any{"dir": "a", "days": 7}, "caches[0].days: unknown key; the keys of a cache are dir, env, mb, age, unit"},
		{map[string]any{"dir": "a", "mb": 1, "unit": "blocks"}, "none of files"},
		{map[string]any{"dir": "a", "mb": 1, "env": "NOT-A-NAME"}, "no variable's name"},
		{map[string]any{"dir": "a", "mb": 1, "cap": 2}, "caches[0].cap: unknown key; the keys of a cache are dir, env, mb, age, unit"},
	}

	for _, c := range cases {
		_, err := settingsOf(plugin.DriverRequest{Cache: t.TempDir(), Config: cachesConf("true", c.cache)}, plugin.CallRun)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.cache, err, c.want)
		}
	}

	twice := cachesConf("true", map[string]any{"dir": "a", "mb": 1}, map[string]any{"dir": "a/b", "mb": 1})
	if _, err := settingsOf(plugin.DriverRequest{Cache: t.TempDir(), Config: twice}, plugin.CallRun); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Errorf("caches inside each other: %v", err)
	}

	if _, err := settingsOf(plugin.DriverRequest{Config: cachesConf("true", map[string]any{"dir": "a", "mb": 1})}, plugin.CallRun); err == nil {
		t.Error("caches without a cache directory in the request")
	}
}

func TestACacheIsTrimmedOncePerBatch(t *testing.T) {
	req := request(t, cachesConf("true", map[string]any{"dir": "build", "age": "1d"}), "a")

	_, first := runOf(t, req)
	_, second := runOf(t, req)

	if !strings.Contains(first, "shell-driver: cache build:") || strings.Contains(second, "shell-driver: cache build:") {
		t.Errorf("first call:\n%s\nsecond call:\n%s", first, second)
	}

	req.Work = t.TempDir()

	if _, next := runOf(t, req); !strings.Contains(next, "shell-driver: cache build:") {
		t.Errorf("the next batch's call didn't trim:\n%s", next)
	}
}

func TestAnAgeIsADurationOrDays(t *testing.T) {
	for age, want := range map[string]time.Duration{"6h": 6 * time.Hour, "36h": 36 * time.Hour, "14d": 14 * day, "1.5d": 36 * time.Hour} {
		if got, err := parseAge(age); err != nil || got != want {
			t.Errorf("%s: %s %v, want %s", age, got, err, want)
		}
	}

	req := request(t, cachesConf("true", map[string]any{"dir": "build", "age": "6h"}), "a")
	dir := filepath.Join(req.Cache, "build")

	file(t, filepath.Join(dir, "seven-hours"), 1, 7*time.Hour)
	file(t, filepath.Join(dir, "five-hours"), 1, 5*time.Hour)

	_, log := runOf(t, req)

	if exists(filepath.Join(dir, "seven-hours")) || !exists(filepath.Join(dir, "five-hours")) ||
		!strings.Contains(log, "(unused for 6h goes); 1 MB in 1 file removed") {
		t.Errorf("an age of 6h:\n%s", log)
	}
}
