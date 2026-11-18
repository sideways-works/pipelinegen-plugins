// Package driver runs each Playwright spec file as a slice and reads its result from
// Playwright's JSON report.
package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

var Version = "dev"

type Settings struct {
	// Dir is the Playwright project's directory in the tree (default the
	// tree's root).
	Dir string `json:"dir"`
	// Config is relative to Dir.
	Config  string `json:"config"`
	Project string `json:"project"`
	// Workers is ignored in a recorded stack bundle, which runs with one.
	Workers int `json:"workers"`
	// Install runs npm ci when node_modules is older than the lockfile
	// (default true).
	Install *bool    `json:"install"`
	Flags   []string `json:"flags"`
}

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
		Protocol: plugin.Protocol, Kind: plugin.KindDriver, Name: "playwright-driver", Version: Version,
		Needs:  []string{"node", "npm", "npx"},
		Verbs:  []string{plugin.CallRun},
		Routes: &plugin.Routes{Files: []string{"**/*.ts", "**/*.mts", "**/*.js", "**/*.mjs", "**/*.tsx", "**/*.jsx"}},
		// The coverage fixture writes each spec file's recording as the file ends.
		Apart: true,
		Settings: map[string]string{
			"dir": plugin.SettingString, "config": plugin.SettingString, "project": plugin.SettingString,
			"workers": plugin.SettingInt, "install": plugin.SettingBool, "flags": plugin.SettingList,
		},
	}, nil
}

var errSettings = errors.New("settings")

func settingsOf(req plugin.DriverRequest) (Settings, error) {
	return settingsFor(req.Config)
}

func (*Settings) settingNoun() string { return "playwright-driver's settings" }

// settingsFor checks presence as well as value: a key given empty would
// otherwise read as left out.
func settingsFor(conf map[string]any) (Settings, error) {
	var s Settings

	if err := strictly(conf, &s); err != nil {
		return s, err
	}

	_, workers := conf["workers"]
	_, project := conf["project"]
	_, config := conf["config"]
	clean := filepath.ToSlash(filepath.Clean(s.Dir))

	switch {
	case workers && s.Workers < 1:
		return s, fmt.Errorf("%w: workers: %d is less than 1; leave workers out for the config's", errSettings, s.Workers)
	case project && s.Project == "":
		return s, fmt.Errorf("%w: project is empty, which runs every project; leave it out for that", errSettings)
	case config && s.Config == "":
		return s, fmt.Errorf("%w: config is empty; leave it out for Playwright's own lookup", errSettings)
	case filepath.IsAbs(s.Dir) || clean == ".." || strings.HasPrefix(clean, "../"):
		return s, fmt.Errorf("%w: dir: %q is no directory inside the tree", errSettings, s.Dir)
	}

	return s, nil
}

type job struct {
	req plugin.DriverRequest
	set Settings
	dir string
	env []string
	log io.Writer
}

func run(ctx context.Context, req plugin.DriverRequest, emit func(plugin.DriverEvent) error, log io.Writer) error {
	set, err := settingsOf(req)
	if err != nil {
		return err
	}

	for _, need := range []string{"node", "npm", "npx"} {
		if _, err := exec.LookPath(need); err != nil {
			return fmt.Errorf("playwright-driver needs %s on PATH: %w", need, err)
		}
	}

	j := &job{req: req, set: set, dir: filepath.Join(req.Tree, set.Dir), log: log}
	j.env = j.environment()

	if err := j.install(ctx); err != nil {
		return err
	}

	if len(req.Slices) == 0 {
		return nil
	}

	r, err := j.playwright(ctx)
	if err != nil {
		return err
	}

	files := r.byFile(req.Tree)

	// A run Playwright stopped early reports the tests it never reached as
	// skipped, so no file of it passes on a skip.
	stopped := len(r.Errors) > 0
	for _, e := range r.Errors {
		sayf(j.log, "playwright-driver: the run says: %s\n", firstLine(e.Message))
	}

	for _, sl := range req.Slices {
		ev, ok := end(sl.Name, files[sl.Name], stopped)
		if !ok {
			continue
		}

		if req.Record {
			ev.Recorded = leftRecording(sl.Recording)
		}

		if err := emit(ev); err != nil {
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

	if os.Getenv("npm_config_cache") == "" && j.req.Cache != "" {
		env = append(env, "npm_config_cache="+filepath.Join(j.req.Cache, "npm"))
	}

	if j.req.Marks != nil {
		env = append(env, plugin.EnvMarks+"="+j.req.Marks.Dir)
	}

	if j.req.Record {
		if out := coverageRoot(j.req.Slices); out != "" {
			env = append(env, "PIPELINEGEN_COVERAGE="+out)
		}

		env = append(env, "PIPELINEGEN_TREE="+j.req.Tree)
	}

	return env
}

func coverageRoot(todo []plugin.DriverSlice) string {
	for _, sl := range todo {
		if sl.Recording == "" {
			continue
		}

		if root, ok := strings.CutSuffix(filepath.ToSlash(sl.Recording), "/"+sl.Name); ok {
			return filepath.FromSlash(root)
		}
	}

	return ""
}

func leftRecording(dir string) bool {
	found := false

	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error { //nolint:errcheck // nothing found is the answer
		if err == nil && !d.IsDir() {
			found = true

			return filepath.SkipAll
		}

		return nil
	})

	return found
}

func (j *job) install(ctx context.Context) error {
	if j.set.Install != nil && !*j.set.Install {
		return nil
	}

	lock := filepath.Join(j.dir, "package-lock.json")
	if !exists(lock) {
		return nil
	}

	if fresh(lock, filepath.Join(j.dir, "node_modules", ".package-lock.json")) {
		return nil
	}

	sayf(j.log, "playwright-driver: npm ci in %s\n", j.where())

	cmd := exec.CommandContext(ctx, "npm", "ci", "--no-audit", "--no-fund")
	cmd.Dir = j.dir
	cmd.Env = j.env
	cmd.Stdout, cmd.Stderr = j.log, j.log

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("npm ci in %s: %w", j.where(), err)
	}

	return nil
}

func exists(p string) bool {
	_, err := os.Stat(p)

	return err == nil
}

func (j *job) where() string {
	if j.set.Dir == "" || j.set.Dir == "." {
		return "the tree's root"
	}

	return j.set.Dir
}

func fresh(lock, installed string) bool {
	l, err := os.Stat(lock)
	if err != nil {
		return false
	}

	i, err := os.Stat(installed)

	return err == nil && !i.ModTime().Before(l.ModTime())
}

func (j *job) playwright(ctx context.Context) (*report, error) {
	out := filepath.Join(j.req.Work, "playwright-report.json")
	_ = os.Remove(out) //nolint:errcheck // a report left by an earlier call goes

	args := []string{"playwright", "test", "--reporter=json"}

	if j.set.Config != "" {
		args = append(args, "--config", j.set.Config)
	}

	if j.set.Project != "" {
		args = append(args, "--project", j.set.Project)
	}

	switch {
	case j.req.Marks != nil:
		sayf(j.log, "playwright-driver: a recording of a suite with a stack: the bundle's %d spec files run with one worker, marked\n", len(j.req.Slices))

		args = append(args, "--workers=1")
	case j.set.Workers > 0:
		args = append(args, "--workers="+strconv.Itoa(j.set.Workers))
	}

	if j.req.Deadline != "" {
		if at, err := time.Parse(time.RFC3339, j.req.Deadline); err == nil {
			// Leave a minute to write the report.
			if left := time.Until(at) - time.Minute; left > 0 {
				args = append(args, "--global-timeout="+strconv.FormatInt(left.Milliseconds(), 10))
			}
		}
	}

	args = append(args, j.set.Flags...)

	for _, sl := range j.req.Slices {
		args = append(args, j.specArg(sl))
	}

	cmd := exec.CommandContext(ctx, "npx", args...)
	cmd.Dir = j.dir
	cmd.Env = append(slices.Clone(j.env), "PLAYWRIGHT_JSON_OUTPUT_NAME="+out)
	cmd.Stdout, cmd.Stderr = j.log, j.log

	// Failing tests exit non-zero; the report says which.
	if err := cmd.Run(); err != nil {
		exit, ok := errors.AsType[*exec.ExitError](err)
		if !ok {
			return nil, fmt.Errorf("npx playwright test: %w", err)
		}

		sayf(j.log, "playwright-driver: playwright test exited %d\n", exit.ExitCode())
	}

	r, err := readReport(out)
	if err != nil {
		// Without a report no file has a result, and each runs again alone.
		sayf(j.log, "playwright-driver: %v\n", err)

		return &report{}, nil
	}

	return r, nil
}

func (j *job) specArg(sl plugin.DriverSlice) string {
	file := sl.Name
	if len(sl.Sources) > 0 {
		file = sl.Sources[0]
	}

	rel, err := filepath.Rel(j.dir, filepath.Join(j.req.Tree, file))
	if err != nil {
		rel = file
	}

	// playwright test takes a regular expression over paths.
	return "^" + regexpQuote(filepath.Join(j.dir, rel)) + "$"
}

func regexpQuote(s string) string {
	var b strings.Builder

	for _, r := range s {
		if strings.ContainsRune(`\.+*?()|[]{}^$`, r) {
			b.WriteRune('\\')
		}

		b.WriteRune(r)
	}

	return b.String()
}

func sayf(w io.Writer, format string, args ...any) {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		return
	}
}
