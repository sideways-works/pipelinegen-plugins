// Package driver is go-driver: it runs a suite's Go test files with go test,
// each _test.go file a slice. A bundle never puts two packages in one
// process. A file holding a line "//ci:alone" runs on its own, and one
// holding "//ci:race" on its own under the race detector.
package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

// Version is the driver's version, set by its release build.
var Version = "dev"

// Settings are what a step's with block may hold.
type Settings struct {
	// Timeout is each test file's limit (default go test's own); a go test of
	// several files gets it once per file, so bundling never tightens it.
	Timeout string `json:"timeout"`
	// CacheMB caps Go's build cache in megabytes, trimming entries unused for a
	// day first, then the least lately used (default no cap).
	CacheMB int `json:"cache_mb"`
	// ModcacheMB caps Go's module cache in megabytes; a cache over it is
	// removed whole, since a partly removed one is corrupt (default no cap).
	ModcacheMB int `json:"modcache_mb"`
	// Coverpkg is go test's -coverpkg for a recording (default ./...).
	Coverpkg []string `json:"coverpkg"`
	Tags     []string `json:"tags"`
	// CoverTags are build tags a recording adds to Tags.
	CoverTags []string `json:"cover_tags"`
	// Flags go to every go test as they are.
	Flags []string `json:"flags"`
	// Race runs every test under the race detector (default off: a file that
	// needs it says so with "//ci:race").
	Race bool `json:"race"`
	// Module is the Go module's directory in the tree (default its root).
	Module string `json:"module"`
	// MustRun maps a test file to a pattern of its top-level tests that must
	// run: a matching test that skips fails the file, and so does a file with
	// no matching test.
	MustRun map[string]string `json:"must_run"`
	// Uncached are patterns of test files that never take a result from go
	// test's cache: tests that read git history, the network or a clock.
	Uncached []string `json:"uncached"`

	held holds
}

// Apart says a recording bundle keeps each file's recording apart: it runs
// the built binary per file as go test runs it, so a child with a cut-down
// environment is recorded too.
const Apart = true

// Handlers are the driver's verbs.
func Handlers(log io.Writer) plugin.DriverHandlers {
	return plugin.DriverHandlers{
		Describe: describe,
		Run: func(ctx context.Context, req plugin.DriverRequest, emit func(plugin.DriverEvent) error) error {
			return run(ctx, req, emit, log)
		},
	}
}

func describe() (plugin.Description, error) {
	return plugin.Description{
		Protocol: plugin.Protocol, Kind: plugin.KindDriver, Name: "go-driver", Version: Version,
		Needs:          []string{"go"},
		Verbs:          []string{plugin.CallRun},
		Routes:         &plugin.Routes{Files: []string{"**/*_test.go"}},
		Apart:          Apart,
		RecordingForms: []string{"go-coverprofile"},
		Settings: map[string]string{
			"timeout": plugin.SettingDuration, "coverpkg": plugin.SettingList, "tags": plugin.SettingList,
			"flags": plugin.SettingList, "race": plugin.SettingBool, "module": plugin.SettingString,
			"cover_tags": plugin.SettingList, "cache_mb": plugin.SettingInt, "must_run": plugin.SettingMap,
			"modcache_mb": plugin.SettingInt, "uncached": plugin.SettingList,
		},
	}, nil
}

var errOutside = errors.New("outside the module")

var errSettings = errors.New("settings")

func settingsOf(req plugin.DriverRequest) (Settings, error) {
	return settingsFor(req.Config)
}

func settingsFor(conf map[string]any) (Settings, error) {
	var s Settings

	if err := strictly(conf, &s); err != nil {
		return s, err
	}

	if s.Timeout != "" {
		// go test reads 0 as no limit, and the driver passes no limit for less.
		if limit, err := time.ParseDuration(s.Timeout); err != nil || limit <= 0 {
			return s, fmt.Errorf("%w: timeout %q is no positive duration", errSettings, s.Timeout)
		}
	}

	switch {
	case s.CacheMB < 0:
		return s, fmt.Errorf("%w: cache_mb: %d is less than 0", errSettings, s.CacheMB)
	case s.ModcacheMB < 0:
		return s, fmt.Errorf("%w: modcache_mb: %d is less than 0", errSettings, s.ModcacheMB)
	case !inTree(s.Module):
		return s, fmt.Errorf("%w: module: %q is no directory inside the tree", errSettings, s.Module)
	}

	for i, p := range s.Uncached {
		if !testFilePattern(p) {
			return s, fmt.Errorf("%w: uncached[%d]: %q is no pattern of test files of the tree, as the slices name them (dir/name_test.go, api/*_test.go, **/git_test.go)", errSettings, i, p)
		}
	}

	var err error
	if s.held, err = holdsOf(s.MustRun); err != nil {
		return s, err
	}

	if len(s.Coverpkg) == 0 {
		s.Coverpkg = []string{"./..."}
	}

	if s.Module == "" {
		s.Module = "."
	}

	return s, nil
}

func (*Settings) settingNoun() string { return "go-driver's settings" }

func inTree(p string) bool {
	clean := filepath.ToSlash(filepath.Clean(p))

	return !filepath.IsAbs(p) && clean != ".." && !strings.HasPrefix(clean, "../")
}

type slice struct {
	plugin.DriverSlice

	pkgs        []string
	alone, race bool
	// uncached slices never take a result from go test's cache.
	uncached bool
}

type job struct {
	req    plugin.DriverRequest
	set    Settings
	module string
	env    []string
	log    io.Writer
	emit   func(plugin.DriverEvent) error
	// limit is a test file's limit; until is the step's deadline less the time
	// to report in (zero: none).
	limit time.Duration
	until time.Time
}

func run(ctx context.Context, req plugin.DriverRequest, emit func(plugin.DriverEvent) error, log io.Writer) error {
	set, err := settingsOf(req)
	if err != nil {
		return err
	}

	if _, err := exec.LookPath("go"); err != nil {
		return fmt.Errorf("go-driver needs go on PATH: %w", err)
	}

	j := &job{req: req, set: set, module: filepath.Join(req.Tree, set.Module), log: log, emit: emit}
	j.env = j.environment()

	ctx, cancel, err := j.deadline(ctx)
	if err != nil {
		return err
	}

	defer cancel()

	j.trimCaches()
	j.sayMissing()

	var todo []*slice

	for _, sl := range req.Slices {
		s, err := j.slice(sl)
		if err != nil {
			return err
		}

		todo = append(todo, s)
	}

	if req.Marks != nil && len(todo) > 1 {
		sayf(log, "go-driver: a recording of a suite with a stack: the bundle's %d files run one at a time, marked\n", len(todo))
	}

	for _, group := range j.groups(todo) {
		if err := j.runGroup(ctx, group); err != nil {
			return err
		}
	}

	return nil
}

func (j *job) environment() []string {
	env := os.Environ()

	for k, v := range j.req.Env {
		env = append(env, k+"="+v)
	}

	for name, dir := range map[string]string{"GOCACHE": "gocache", "GOMODCACHE": "gomodcache"} {
		if os.Getenv(name) == "" && j.req.Env[name] == "" && j.req.Cache != "" {
			env = append(env, name+"="+filepath.Join(j.req.Cache, dir))
		}
	}

	return env
}

func (j *job) deadline(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if j.set.Timeout != "" {
		j.limit, _ = time.ParseDuration(j.set.Timeout) //nolint:errcheck // checked in settingsOf
	}

	if j.req.Deadline == "" {
		ctx, cancel := context.WithCancel(ctx)

		return ctx, cancel, nil
	}

	at, err := time.Parse(time.RFC3339, j.req.Deadline)
	if err != nil {
		return ctx, func() {}, fmt.Errorf("deadline: %w", err)
	}

	at = at.Add(-time.Minute)
	j.until = at

	ctx, cancel := context.WithDeadline(ctx, at)

	return ctx, cancel, nil
}

func (j *job) slice(sl plugin.DriverSlice) (*slice, error) {
	s := &slice{DriverSlice: sl}

	sources := sl.Sources
	if len(sources) == 0 {
		sources = []string{sl.Name}
	}

	for _, src := range sources {
		if slices.ContainsFunc(j.set.Uncached, func(p string) bool { return matchFile(p, src) }) {
			// A call of its own, so the rest of its package stays cached.
			s.alone, s.uncached = true, true
		}

		dir, err := filepath.Rel(j.module, filepath.Join(j.req.Tree, path.Dir(src)))
		if err != nil || strings.HasPrefix(dir, "..") {
			return nil, fmt.Errorf("%w: %s is outside the module %s", errOutside, src, j.set.Module)
		}

		if pkg := "./" + filepath.ToSlash(dir); !slices.Contains(s.pkgs, pkg) {
			s.pkgs = append(s.pkgs, pkg)
		}

		data, err := os.ReadFile(filepath.Join(j.req.Tree, src)) //nolint:gosec // the tree's own test file
		if err != nil {
			return nil, fmt.Errorf("%s: %w", src, err)
		}

		for line := range strings.SplitSeq(string(data), "\n") {
			switch strings.TrimSpace(line) {
			case "//ci:alone":
				s.alone = true
			case "//ci:race":
				s.alone, s.race = true, true
			}
		}
	}

	return s, nil
}

type group struct {
	pkg    string
	slices []*slice
}

func (j *job) groups(todo []*slice) []group {
	var (
		out   []group
		byPkg = map[string]int{}
	)

	for _, s := range todo {
		if !j.req.Batch || s.alone || len(s.pkgs) > 1 {
			out = append(out, group{slices: []*slice{s}})

			continue
		}

		i, ok := byPkg[s.pkgs[0]]
		if !ok {
			i = len(out)
			byPkg[s.pkgs[0]] = i
			out = append(out, group{pkg: s.pkgs[0]})
		}

		out[i].slices = append(out[i].slices, s)
	}

	return out
}

func (j *job) runGroup(ctx context.Context, g group) error {
	// A recording bundle of several files builds the binary once and runs it
	// per file.
	if j.req.Record && len(g.slices) > 1 {
		return j.recordBundle(ctx, g)
	}

	if len(g.slices) == 1 {
		return j.alone(ctx, g.slices[0])
	}

	return j.together(ctx, g)
}

func (j *job) alone(ctx context.Context, s *slice) error {
	if err := j.mark(ctx, plugin.MarkStart, s.Name); err != nil {
		return err
	}

	start := time.Now()

	var outs []*outcome

	for i, pkg := range s.pkgs {
		args := j.testArgs(s.race, 1)
		if s.uncached && !j.req.Stack {
			args = append(args, "-count=1")
		}

		if j.req.Record {
			if err := os.MkdirAll(s.Recording, 0o750); err != nil {
				return fmt.Errorf("recording: %w", err)
			}

			args = append(args, j.coverArgs()...)
			args = append(args, "-coverprofile="+filepath.Join(s.Recording, profileName(i)))
		}

		args = append(args, "-run", runPattern(s.Tests), pkg)

		o, err := j.goTest(ctx, args)
		if err != nil {
			return err
		}

		outs = append(outs, o)
	}

	if err := j.mark(ctx, plugin.MarkStop, s.Name); err != nil {
		return err
	}

	ev, ok := end(s.DriverSlice, outs, j.set.held)
	if !ok {
		return nil
	}

	if !j.req.Batch {
		ev.Seconds = time.Since(start).Seconds()
	}

	return j.done(s, ev)
}

func profileName(i int) string {
	if i == 0 {
		return "tests.coverprofile"
	}

	return fmt.Sprintf("tests-%d.coverprofile", i+1)
}

func (j *job) together(ctx context.Context, g group) error {
	var tests []string
	for _, s := range g.slices {
		tests = append(tests, s.Tests...)
	}

	args := append(j.testArgs(false, len(g.slices)), "-run", runPattern(tests), g.pkg)

	o, err := j.goTest(ctx, args)
	if err != nil {
		return err
	}

	for _, s := range g.slices {
		if ev, ok := end(s.DriverSlice, []*outcome{o}, j.set.held); ok {
			if err := j.done(s, ev); err != nil {
				return err
			}
		}
	}

	return nil
}

func (j *job) done(s *slice, ev plugin.DriverEvent) error {
	if j.req.Record {
		cutRecording(s.Recording)

		_, err := os.Stat(filepath.Join(s.Recording, profileName(0)))
		ev.Recorded = err == nil
	}

	return j.emit(ev)
}

func (j *job) timeout(files int) time.Duration {
	limit := j.limit * time.Duration(files)

	if !j.until.IsZero() {
		if left := time.Until(j.until); limit == 0 || left < limit {
			limit = max(left, time.Second)
		}
	}

	return limit
}

func (j *job) testArgs(race bool, files int) []string {
	args := []string{"test", "-json"}

	// A stack's code is no input of go test's cache, so the test always runs.
	if j.req.Stack {
		args = append(args, "-count=1")
	}

	if race || j.set.Race {
		args = append(args, "-race")
	}

	if limit := j.timeout(files); limit > 0 {
		args = append(args, "-timeout", limit.String())
	}

	if tags := j.tags(); len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}

	return append(args, j.set.Flags...)
}

func (j *job) tags() []string {
	tags := slices.Clone(j.set.Tags)

	if j.req.Record {
		for _, t := range j.set.CoverTags {
			if !slices.Contains(tags, t) {
				tags = append(tags, t)
			}
		}
	}

	return tags
}

func (j *job) coverArgs() []string {
	return []string{"-cover", "-covermode=atomic", "-coverpkg=" + strings.Join(j.set.Coverpkg, ",")}
}

func runPattern(tests []string) string {
	if len(tests) == 0 {
		return "^$"
	}

	return "^(" + strings.Join(tests, "|") + ")$"
}

func (j *job) goTest(ctx context.Context, args []string) (*outcome, error) {
	cmd := exec.CommandContext(ctx, "go", args...) //nolint:gosec // go itself, on the tree under test
	cmd.Dir = j.module
	cmd.Env = j.env
	cmd.Stderr = j.log

	return j.events(cmd)
}

func (j *job) events(cmd *exec.Cmd) (*outcome, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", cmd.Args[0], err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s: %w", cmd.Args[0], err)
	}

	o := newOutcome()
	o.read(stdout, j.log)

	// A non-zero exit is the tests' to explain through their events; only a
	// command that couldn't run fails the driver.
	if err := cmd.Wait(); err != nil {
		exit, ok := errors.AsType[*exec.ExitError](err)
		if !ok {
			return nil, fmt.Errorf("%s: %w", strings.Join(cmd.Args[:2], " "), err)
		}

		sayf(j.log, "go-driver: %s exited %d\n", strings.Join(cmd.Args[:2], " "), exit.ExitCode())
	}

	return o, nil
}

func (j *job) mark(ctx context.Context, what, name string) error {
	m := j.req.Marks
	if m == nil || len(m.Command) == 0 {
		return nil
	}

	argv := append(slices.Clone(m.Command), what, name)

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // the tool's own command, from the request

	cmd.Env = append(slices.Clone(j.env), plugin.EnvMarks+"="+m.Dir)
	cmd.Stdout, cmd.Stderr = j.log, j.log

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mark %s %s: %w", what, name, err)
	}

	return nil
}

func sayf(w io.Writer, format string, args ...any) {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		return
	}
}

func testFilePattern(p string) bool {
	if !inTree(p) || p == "" || path.Clean(p) != p {
		return false
	}

	parts := strings.Split(p, "/")
	for _, part := range parts {
		if _, err := path.Match(part, ""); err != nil {
			return false
		}
	}

	return strings.HasSuffix(parts[len(parts)-1], "_test.go")
}

// matchFile matches path.Match parts, in which ** stands for any number of
// directories.
func matchFile(pattern, file string) bool {
	return matchParts(strings.Split(pattern, "/"), strings.Split(file, "/"))
}

func matchParts(pat, name []string) bool {
	switch {
	case len(pat) == 0:
		return len(name) == 0
	case pat[0] == "**":
		for i := 0; i <= len(name); i++ {
			if matchParts(pat[1:], name[i:]) {
				return true
			}
		}

		return false
	case len(name) == 0:
		return false
	}

	ok, err := path.Match(pat[0], name[0])

	return err == nil && ok && matchParts(pat[1:], name[1:])
}
