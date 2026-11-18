package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sideways-works/pipelinegen/plugin"

	"github.com/sideways-works/pipelinegen-sbom-locale/catalog"

	"github.com/sideways-works/pipelinegen-correlator-locale/collect"
	"github.com/sideways-works/pipelinegen-correlator-locale/observe"
	"github.com/sideways-works/pipelinegen-correlator-locale/recorder"
)

var Version = "dev"

const callRecorder = "recorder"

const roleCatalog = "catalog"

type config struct {
	// Log is the file a batch's stack shares (PIPELINEGEN_LOCALE_KEYS_LOG),
	// relative to the batch's directory; collect cuts it per slice.
	Log string `json:"log"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == callRecorder {
		return recorderCmd(args[1:], stdout, stderr)
	}

	return plugin.Serve(args, stdin, stdout, stderr, map[string]plugin.Handler{
		plugin.CallDescribe: func([]byte) (any, error) { return describe(), nil },
		plugin.CallDetect:   detect,
		plugin.CallObserve:  observeCall,
		plugin.CallCollect:  func(in []byte) (any, error) { return collectCall(in, stderr) },
	})
}

func describe() plugin.Description {
	return plugin.Description{
		Protocol:     plugin.Protocol,
		Kind:         plugin.KindCorrelator,
		Name:         "locale-correlator",
		Version:      Version,
		Capabilities: []string{plugin.CallDetect, plugin.CallObserve, plugin.CallCollect},
		Candidates:   catalog.Candidates,
		Inputs:       map[string]string{plugin.CallDetect: plugin.PerFile},
	}
}

// detect claims the catalog files locale-sbom claims, so observe can check
// that a recording names catalogs that exist.
func detect(in []byte) (any, error) {
	var req plugin.DetectRequest
	if err := json.Unmarshal(in, &req); err != nil {
		return nil, fmt.Errorf("reading the request: %w", err)
	}

	claims := []plugin.Claim{}

	for _, p := range req.Paths {
		ok, err := catalog.Claimable(req.Tree, p)
		if err != nil {
			return nil, fmt.Errorf("detect: %w", err)
		}

		if ok {
			claims = append(claims, plugin.Claim{Path: p, Role: roleCatalog})
		}
	}

	return plugin.DetectAnswer{Claims: claims}, nil
}

func observeCall(in []byte) (any, error) {
	var req plugin.ObserveRequest
	if err := json.Unmarshal(in, &req); err != nil {
		return nil, fmt.Errorf("reading the request: %w", err)
	}

	paths := make([]string, 0, len(req.Claimed))
	for _, c := range req.Claimed {
		paths = append(paths, c.Path)
	}

	ans, err := observe.Observe(req.Dirs, observe.NewCatalogs(paths))
	if errors.Is(err, observe.ErrCantTell) {
		return nil, fmt.Errorf("%w: %w", plugin.ErrCantTell, err)
	}

	if err != nil {
		return nil, err
	}

	return plugin.ObserveAnswer{Touched: ans.Touched, SetupLike: ans.SetupLike}, nil
}

func collectCall(in []byte, stderr io.Writer) (any, error) {
	var req plugin.CollectRequest
	if err := json.Unmarshal(in, &req); err != nil {
		return nil, fmt.Errorf("reading the request: %w", err)
	}

	var cfg config

	if b, err := json.Marshal(req.Config); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	} else if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	if req.Dir == "" {
		return nil, fmt.Errorf("%w: no dir", collect.ErrCollect)
	}

	var err error

	switch req.Action {
	case plugin.CollectSetupEnd, plugin.CollectSliceEnd:
		// what the slice's processes wrote into the slice's directory; a
		// later part's own directory lies elsewhere
		err = collect.Take(collect.SliceDir(req.Dir, req.Slice), req.Dir)
		// a stack's shared log, in a suite that runs one
		if err == nil && cfg.Log != "" && req.Stack {
			err = collect.Cut(cfg.Log, req.Dir, req.Action == plugin.CollectSetupEnd)
		}
	case plugin.CollectSliceStart:
		if cfg.Log != "" && req.Stack {
			err = collect.Mark(cfg.Log, req.Dir)
		}
	default:
		err = fmt.Errorf("%w: action %q", collect.ErrCollect, req.Action)
	}

	// a stack that doesn't record narrows nothing and is no failure: said
	// once, at the setup's end
	if errors.Is(err, collect.ErrNoRecorder) {
		say(stderr, "locale-correlator collect: "+err.Error())

		err = nil
	}

	if err != nil {
		return nil, err
	}

	return struct{}{}, nil
}

func recorderCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		say(stderr, "usage: locale-correlator recorder <dir>")

		return 2
	}

	if err := recorder.Write(args[0]); err != nil {
		say(stderr, "locale-correlator recorder: "+err.Error())

		return 1
	}

	for _, n := range recorder.Names() {
		say(stdout, filepath.Join(args[0], n))
	}

	return 0
}

func say(w io.Writer, msg string) {
	if _, err := fmt.Fprintln(w, msg); err != nil {
		return
	}
}
