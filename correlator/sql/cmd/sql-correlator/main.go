package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"

	"github.com/sideways-works/pipelinegen-correlator-sql/collect"
	"github.com/sideways-works/pipelinegen-correlator-sql/observe"
)

var Version = "dev"

type config struct {
	Log            string           `json:"log"`
	SetupDatabases []string         `json:"setup_databases"`
	Databases      []string         `json:"databases"`
	Quiet          collect.Duration `json:"quiet"`
	SearchPath     []string         `json:"search_path"`
}

func (c config) collect() collect.Config {
	return collect.Config{Log: c.Log, SetupDatabases: c.SetupDatabases, Databases: c.Databases, Quiet: c.Quiet}
}

func (c config) observe() observe.Config {
	return observe.Config{SearchPath: c.SearchPath, Collect: c.collect()}
}

var errUnknownAction = errors.New("unknown collect action")

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return plugin.Serve(args, stdin, stdout, stderr, map[string]plugin.Handler{
		plugin.CallDescribe: func([]byte) (any, error) { return describe(), nil },
		plugin.CallCollect:  runCollect,
		plugin.CallObserve:  runObserve,
	})
}

func runCollect(in []byte) (any, error) {
	var req plugin.CollectRequest
	if err := json.Unmarshal(in, &req); err != nil {
		return nil, fmt.Errorf("reading the request: %w", err)
	}

	cfg, err := readConfig(req.Config)
	if err != nil {
		return nil, err
	}

	switch req.Action {
	case plugin.CollectSetupEnd:
		err = collect.Setup(cfg.collect(), req.Dir, time.Now)
	case plugin.CollectSliceStart:
		err = collect.Start(cfg.collect(), req.Dir, time.Now)
	case plugin.CollectSliceEnd:
		err = collect.End(cfg.collect(), req.Dir, req.Slice, time.Now)
	default:
		err = fmt.Errorf("%w %q", errUnknownAction, req.Action)
	}

	if err != nil {
		return nil, err
	}

	return struct{}{}, nil
}

func runObserve(in []byte) (any, error) {
	var req plugin.ObserveRequest
	if err := json.Unmarshal(in, &req); err != nil {
		return nil, fmt.Errorf("reading the request: %w", err)
	}

	cfg, err := readConfig(req.Config)
	if err != nil {
		return nil, err
	}

	obs, err := observe.Touched(cfg.observe(), req.Dirs, req.Cache)
	if errors.Is(err, observe.ErrCannotTell) {
		return nil, fmt.Errorf("%w: %w", plugin.ErrCantTell, err)
	}

	if err != nil {
		return nil, err
	}

	return plugin.ObserveAnswer{Touched: nonNil(obs.Touched), SetupLike: obs.SetupLike}, nil
}

// nonNil keeps an empty answer a list: touched is always present.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}

	return s
}

// readConfig reads the plugin's block strictly: a misspelt key would
// otherwise switch a setting off without a word.
func readConfig(raw map[string]any) (config, error) {
	var cfg config

	b, err := json.Marshal(raw)
	if err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}

	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}

	return cfg, nil
}

func describe() plugin.Description {
	return plugin.Description{
		Protocol:     plugin.Protocol,
		Kind:         plugin.KindCorrelator,
		Name:         "sql-correlator",
		Version:      Version,
		Capabilities: []string{plugin.CallObserve, plugin.CallCollect},
	}
}
