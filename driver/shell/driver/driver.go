// Package driver is shell-driver: it runs a suite's steps written as shell,
// so a repository needs no task scripts of its own.
package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

// Version is the driver's version, set by its release build.
var Version = "dev"

// Settings are what a step's with block may hold.
type Settings struct {
	// Script runs by Shell from a file of its own.
	Script string `json:"script"`
	// Command runs without a shell.
	Command []string `json:"command"`
	// Functions makes a run step run each slice as a shell test file: every
	// function whose name starts with it is a test.
	Functions string `json:"functions"`
	// Shell defaults to sh -eu for a script and sh for test functions.
	Shell []string `json:"shell"`
	// Dir is relative to the tree. Test functions run in their file's own
	// directory.
	Dir string `json:"dir"`
	// Env is set over the step's environment.
	Env map[string]string `json:"env"`
	// Timeout is each slice's limit; a bundle gets it once per slice. On a
	// prepare or set-up step it bounds the script.
	Timeout string `json:"timeout"`
	// Stop is a set-up's script, run when the batch ends.
	Stop string `json:"stop"`
	// Checks run one after another for each slice, each a test.
	Checks []Check `json:"checks"`
	Caches []Cache `json:"caches"`

	limit time.Duration
}

func (*Settings) settingNoun() string { return "shell-driver's settings" }

// Handlers are the driver's verbs.
func Handlers(log io.Writer) plugin.DriverHandlers {
	return plugin.DriverHandlers{
		Describe: describe,
		Run: func(ctx context.Context, req plugin.DriverRequest, emit func(plugin.DriverEvent) error) error {
			return run(ctx, req, emit, log)
		},
		Setup: func(ctx context.Context, req plugin.DriverRequest, ready func(map[string]string) error, stop <-chan struct{}) error {
			return setup(ctx, req, ready, stop, log)
		},
		Prepare: func(ctx context.Context, req plugin.DriverRequest) error {
			return prepare(ctx, req, log)
		},
	}
}

// Describe is the driver's describe answer.
func Describe() plugin.Description {
	d, _ := describe() //nolint:errcheck // describe never fails

	return d
}

func describe() (plugin.Description, error) {
	return plugin.Description{
		Protocol: plugin.Protocol, Kind: plugin.KindDriver, Name: "shell-driver", Version: Version,
		Needs:   []string{"sh"},
		Verbs:   []string{plugin.CallPrepare, plugin.CallSetup, plugin.CallRun},
		Routes:  &plugin.Routes{Files: []string{"**"}, Kinds: []string{"*"}},
		Results: plugin.ResultsExit,
		Settings: map[string]string{
			"script": plugin.SettingString, "command": plugin.SettingList, "functions": plugin.SettingString,
			"shell": plugin.SettingList, "dir": plugin.SettingString, "env": plugin.SettingMap,
			"timeout": plugin.SettingDuration, "stop": plugin.SettingString, "checks": plugin.SettingList,
			"caches": plugin.SettingList,
		},
	}, nil
}

var errSettings = errors.New("settings")

var functionPrefix = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func settingsOf(req plugin.DriverRequest, verb string) (Settings, error) {
	s, err := settingsFor(req.Config, verb)
	if err != nil {
		return s, err
	}

	if len(s.Caches) > 0 && req.Cache == "" {
		return s, fmt.Errorf("%w: caches: the request names no cache directory to keep them in", errSettings)
	}

	if len(s.Shell) == 0 {
		s.Shell = []string{"sh", "-eu"}
		if s.Functions != "" {
			s.Shell = []string{"sh"}
		}
	}

	if s.Script != "" || s.Functions != "" || s.Stop != "" || len(s.Checks) > 0 {
		if _, err := exec.LookPath(s.Shell[0]); err != nil {
			return s, fmt.Errorf("shell-driver needs %s on PATH: %w", s.Shell[0], err)
		}
	}

	return s, nil
}

func settingsFor(conf map[string]any, verb string) (Settings, error) {
	var s Settings

	if err := strictly(conf, &s); err != nil {
		return s, err
	}

	if s.Timeout != "" {
		var err error
		if s.limit, err = time.ParseDuration(s.Timeout); err != nil || s.limit <= 0 {
			return s, fmt.Errorf("%w: timeout %q is no positive duration", errSettings, s.Timeout)
		}
	}

	if err := oneWay(s, verb); err != nil {
		return s, err
	}

	if err := checkSettings(s.Checks); err != nil {
		return s, err
	}

	if err := cacheSettings(s.Caches); err != nil {
		return s, err
	}

	if err := envSettings(s); err != nil {
		return s, err
	}

	return s, nil
}

func oneWay(s Settings, verb string) error {
	ways := 0

	for _, set := range []bool{s.Script != "", len(s.Command) > 0, s.Functions != "", len(s.Checks) > 0} {
		if set {
			ways++
		}
	}

	switch {
	case s.Script != "" && strings.TrimSpace(s.Script) == "":
		return fmt.Errorf("%w: script holds nothing to run", errSettings)
	case s.Stop != "" && strings.TrimSpace(s.Stop) == "":
		return fmt.Errorf("%w: stop holds nothing to run", errSettings)
	case ways > 1:
		return fmt.Errorf("%w: give one of script, command, functions and checks", errSettings)
	case s.Functions != "" && verb != plugin.CallRun:
		return fmt.Errorf("%w: functions is a run step's setting", errSettings)
	case len(s.Checks) > 0 && verb != plugin.CallRun:
		return fmt.Errorf("%w: checks is a run step's setting", errSettings)
	case s.Functions != "" && !functionPrefix.MatchString(s.Functions):
		return fmt.Errorf("%w: functions %q is no start of a shell function's name", errSettings, s.Functions)
	case s.Stop != "" && verb != plugin.CallSetup:
		return fmt.Errorf("%w: stop is a set-up's setting", errSettings)
	case ways == 0 && verb == plugin.CallRun:
		return fmt.Errorf("%w: a run step needs a script, a command, functions or checks", errSettings)
	case ways == 0 && verb == plugin.CallPrepare:
		return fmt.Errorf("%w: a prepare step needs a script or a command", errSettings)
	case ways == 0 && s.Stop == "":
		return fmt.Errorf("%w: a set-up needs a script, a command or a stop", errSettings)
	}

	return nil
}
