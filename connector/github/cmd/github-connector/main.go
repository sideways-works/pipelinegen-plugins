// Command github-connector is a pipelinegen connector for repositories on
// GitHub: it lists their pull requests, sets commit statuses and keeps a
// comment on each pull request. It sends nothing itself: each call answers
// with the requests it wants sent, which pipelinegen checks, sends with the
// token of the call's purpose and hands back, round by round.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sideways-works/pipelinegen-connector-github/connector"
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

	for _, call := range plugin.ConnectorCalls {
		h[call] = func(in []byte) (any, error) { return round(call, in) }
	}

	return h
}

func round(call string, in []byte) (any, error) {
	var req plugin.ExchangeRequest
	if err := json.Unmarshal(in, &req); err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}

	read := func(file string) ([]byte, error) {
		if !filepath.IsLocal(file) {
			return nil, fmt.Errorf("%w: body file %q is outside the work directory", connector.ErrGitHub, file)
		}

		return os.ReadFile(filepath.Join(req.Work, file)) //nolint:gosec // a file inside the round's work directory
	}

	r, err := connector.Round(call, req, read)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", call, err)
	}

	return r, nil
}
