// Command git-connector is a pipelinegen connector for a plain git server
// with no host API: its proposed changes are branches whose names match
// the config's patterns. pipelinegen lists the branches with git and hands
// them in; the connector sends nothing and reports nowhere.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/sideways-works/pipelinegen-connector-git/connector"
	"github.com/sideways-works/pipelinegen/plugin"
)

// Version is set by the release build.
var Version = "dev"

func main() {
	os.Exit(plugin.Serve(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, handlers()))
}

func handlers() map[string]plugin.Handler {
	h := map[string]plugin.Handler{
		plugin.CallDescribe: func([]byte) (any, error) { return connector.Describe(Version), nil },
	}

	for _, call := range connector.Calls {
		h[call] = func(in []byte) (any, error) { return round(call, in) }
	}

	return h
}

func round(call string, in []byte) (any, error) {
	var req plugin.ExchangeRequest
	if err := json.Unmarshal(in, &req); err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}

	r, err := connector.Round(call, req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", call, err)
	}

	return r, nil
}
