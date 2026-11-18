package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/sideways-works/pipelinegen-plugins/driver/bazel/internal/bazel"
	"github.com/sideways-works/pipelinegen-plugins/driver/bazel/internal/boundary"
	"github.com/sideways-works/pipelinegen-plugins/driver/bazel/internal/driver"
)

var errVerb = errors.New("unknown verb")

var Version = "dev"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "bazel-driver: describe | run")
		os.Exit(2)
	}

	var err error

	switch os.Args[1] {
	case "describe":
		err = json.NewEncoder(os.Stdout).Encode(describe())
	case "run":
		err = run(context.Background(), os.Stdin, os.Stdout)
	case "boundary":
		os.Exit(boundaryVerb(os.Args[2:]))
	default:
		err = fmt.Errorf("%w %q", errVerb, os.Args[1])
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "bazel-driver:", err)
		os.Exit(1)
	}
}

// boundaryVerb is the tests' --run_under prefix in a recording run of a
// stack suite: boundary [--wait 5m] -- test command...
func boundaryVerb(args []string) int {
	wait := markWait

	for len(args) > 0 && args[0] != "--" {
		if args[0] != "--wait" || len(args) < 2 {
			fmt.Fprintln(os.Stderr, "boundary: unknown argument", args[0])

			return 1
		}

		d, err := time.ParseDuration(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "boundary:", err)

			return 1
		}

		wait, args = d, args[2:]
	}

	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "boundary [--wait 5m] -- test command")

		return 1
	}

	return boundary.Wrap(wait, args[1:])
}

const markWait = 5 * time.Minute

type description struct {
	Protocol       int               `json:"protocol"`
	Kind           string            `json:"kind"`
	Name           string            `json:"name"`
	Version        string            `json:"version"`
	Verbs          []string          `json:"verbs"`
	Routes         map[string]any    `json:"routes"`
	Apart          bool              `json:"apart"`
	RecordingForms []string          `json:"recording_forms"`
	Needs          []string          `json:"needs"`
	Caches         []string          `json:"caches"`
	Settings       map[string]string `json:"settings"`
}

func describe() description {
	return description{
		Protocol: 1, Kind: "driver", Name: "bazel-driver", Version: Version, Verbs: []string{"run"},
		// Any test rule: Bazel runs each target as an action of its own.
		Routes: map[string]any{"kinds": []string{"*_test", "test_suite"}},
		// Each target's coverage is its own file, bundled or not.
		Apart: true, RecordingForms: []string{"lcov"},
		Needs: []string{"bazel"}, Caches: []string{"output_user_root", "repository_cache"},
		Settings: map[string]string{
			"startup": "list", "flags": "list", "instrument": "string", "timeout": "int",
		},
	}
}

type request struct {
	Work      string            `json:"work"`
	Cache     string            `json:"cache"`
	Tree      string            `json:"tree"`
	Workspace string            `json:"workspace"`
	Tools     map[string]string `json:"tools"`
	Config    map[string]any    `json:"config"`
	Batch     bool              `json:"batch"`
	Record    bool              `json:"record"`
	Stack     bool              `json:"stack"`
	Marks     *struct {
		Dir string `json:"dir"`
	} `json:"marks"`
	Env      map[string]string `json:"env"`
	Deadline string            `json:"deadline"`
	Slices   []struct {
		Name      string   `json:"name"`
		Label     string   `json:"label"`
		Target    string   `json:"target"`
		Kind      string   `json:"kind"`
		Tests     []string `json:"tests"`
		Extras    string   `json:"extras"`
		Recording string   `json:"recording"`
	} `json:"slices"`
}

type event struct {
	Slice    string           `json:"slice"`
	Event    string           `json:"event"`
	Passed   *bool            `json:"passed,omitempty"`
	Tests    *int             `json:"tests,omitempty"`
	Failures []driver.Failure `json:"failures,omitempty"`
	Seconds  *float64         `json:"seconds,omitempty"`
	Recorded bool             `json:"recorded,omitempty"`
	Replayed bool             `json:"replayed,omitempty"`
	Status   string           `json:"status,omitempty"`
}

// deadlineMargin leaves Bazel time to close its event file after the
// interrupt.
const deadlineMargin = 2 * time.Minute

func driverRequest(req request) (driver.Request, error) {
	set, err := driver.SettingsOf(req.Config)
	if err != nil {
		return driver.Request{}, fmt.Errorf("%w", err)
	}

	dr := driver.Request{
		Workspace: filepath.Join(req.Tree, req.Workspace), Root: filepath.Join(req.Cache, "output_user_root"),
		Work: req.Work, Record: req.Record, Stack: req.Stack, Settings: set, Env: os.Environ(),
	}

	if req.Marks != nil {
		self, err := os.Executable()
		if err != nil {
			return dr, fmt.Errorf("marks: %w", err)
		}

		dr.Marks, dr.Self, dr.Stack = req.Marks.Dir, self, true
	}

	dr.Settings.Batch = req.Batch
	dr.Settings.Startup = append([]string{"--max_idle_secs=10800"}, dr.Settings.Startup...)
	dr.Settings.Flags = append([]string{"--repository_cache=" + filepath.Join(req.Cache, "repository_cache")}, dr.Settings.Flags...)

	for name, value := range req.Env {
		dr.Env = append(dr.Env, name+"="+value)
		dr.Settings.TestEnv = append(dr.Settings.TestEnv, name)
	}

	return dr, nil
}

func run(ctx context.Context, in io.Reader, out io.Writer) error {
	var req request
	if err := json.NewDecoder(in).Decode(&req); err != nil {
		return fmt.Errorf("request: %w", err)
	}

	if req.Deadline != "" {
		at, err := time.Parse(time.RFC3339, req.Deadline)
		if err != nil {
			return fmt.Errorf("deadline: %w", err)
		}

		var cancel context.CancelFunc

		ctx, cancel = context.WithDeadline(ctx, at.Add(-deadlineMargin))
		defer cancel()
	}

	dr, err := driverRequest(req)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(out)

	for _, s := range req.Slices {
		label := s.Target
		if label == "" {
			label = s.Label
		}

		dr.Slices = append(dr.Slices, driver.Slice{Name: s.Name, Label: label, Kind: s.Kind, Tests: s.Tests, Extras: s.Extras, Recording: s.Recording})

		if err := enc.Encode(event{Slice: s.Name, Event: "start"}); err != nil {
			return fmt.Errorf("answer: %w", err)
		}
	}

	bin := req.Tools["bazelisk"]

	var encErr error

	err = driver.Run(ctx, bazel.Exec{Bazel: bin}, dr, func(r driver.Result) {
		e := event{Slice: r.Slice, Event: "end", Passed: &r.Passed, Tests: &r.Tests, Failures: r.Failures,
			Seconds: &r.Seconds, Recorded: r.Recorded, Replayed: r.Replayed, Status: r.Status}
		if err := enc.Encode(e); err != nil && encErr == nil {
			encErr = err
		}
	})
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}

	if encErr != nil {
		return fmt.Errorf("answer: %w", encErr)
	}

	return nil
}
