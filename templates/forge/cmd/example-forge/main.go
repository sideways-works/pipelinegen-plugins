// Command example-forge is a minimal forge for a git host with an HTTP API: it claims the
// host's remotes, lists a repository's open changes with one request,
// GET <api>/repos/<repository>/changes?base=<branch>, which answers [{"id", "head", "branch"}],
// and names the host's web pages of a commit, a file and a change.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"
)

// Version is set to the release's tag when it is built.
var Version = "dev"

const requestChanges = "changes"

var errForge = errors.New("example-forge")

func main() {
	h := map[string]plugin.Handler{plugin.CallDescribe: func([]byte) (any, error) { return describe(), nil }}

	for _, call := range []string{plugin.CallIdentify, plugin.CallChanges, plugin.CallLinks} {
		h[call] = func(in []byte) (any, error) { return round(call, in) }
	}

	os.Exit(plugin.Serve(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, h))
}

func describe() plugin.Description {
	return plugin.Description{
		Protocol: plugin.Protocol, Kind: plugin.KindForge, Name: "example-forge", Version: Version,
		Capabilities: []string{plugin.CapChanges},
		// the core puts the token of the call's purpose in this header; the forge never sees it
		Auth:     &plugin.ForgeAuth{Header: "Authorization", Scheme: "Bearer"},
		Purposes: []string{plugin.PurposeChanges},
	}
}

// round answers one round of a call: the requests to send, or, once their responses are in, the
// call's answer. Each round carries the call's arguments and every response so far.
func round(call string, in []byte) (plugin.Round, error) {
	var req plugin.ExchangeRequest
	if err := json.Unmarshal(in, &req); err != nil {
		return plugin.Round{}, fmt.Errorf("%w: request: %w", errForge, err)
	}

	api, _ := req.Config["api"].(string) //nolint:errcheck // a missing api is checked below
	if api == "" {
		return plugin.Round{}, fmt.Errorf("%w: config: api, the host's API base URL, is missing", errForge)
	}

	var answer any

	switch call {
	case plugin.CallIdentify:
		var a plugin.IdentifyArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			return plugin.Round{}, fmt.Errorf("%w: args: %w", errForge, err)
		}

		answer = identify(api, a.Remote)
	case plugin.CallChanges:
		var a plugin.ChangesArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			return plugin.Round{}, fmt.Errorf("%w: args: %w", errForge, err)
		}

		remote, _ := req.Config["remote"].(string) //nolint:errcheck // an empty remote names no repository
		if len(req.Responses) == 0 {
			return plugin.Round{Requests: []plugin.HTTPRequest{{
				ID: requestChanges, Method: "GET",
				URL:  strings.TrimSuffix(api, "/") + "/repos/" + repository(remote) + "/changes?base=" + url.QueryEscape(a.Base),
				Auth: &plugin.HTTPAuth{Purpose: plugin.PurposeChanges},
			}}}, nil
		}

		changes, err := changesFrom(req.Work, req.Responses[0], a.Base)
		if err != nil {
			return plugin.Round{}, err
		}

		answer = plugin.ChangesAnswer{Changes: changes}
	case plugin.CallLinks:
		var a plugin.LinksArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			return plugin.Round{}, fmt.Errorf("%w: args: %w", errForge, err)
		}

		remote, _ := req.Config["remote"].(string) //nolint:errcheck // an empty remote has no pages
		answer = links(remote, a)
	default:
		return plugin.Round{}, fmt.Errorf("%w: no call %q", errForge, call)
	}

	data, err := json.Marshal(answer)
	if err != nil {
		return plugin.Round{}, fmt.Errorf("%w: %w", errForge, err)
	}

	return plugin.Round{Done: data}, nil
}

// identify claims a remote on the API's host.
func identify(api, remote string) plugin.Identity {
	a, errA := url.Parse(api)
	r, errR := url.Parse(remote)

	if errA != nil || errR != nil || a.Host == "" || r.Scheme != a.Scheme || r.Host != a.Host {
		return plugin.Identity{}
	}

	return plugin.Identity{Mine: true, Repository: repository(remote), GitRemote: remote}
}

// links are the host's pages, under the remote's address without .git; it sends no request.
func links(remote string, a plugin.LinksArgs) plugin.WebLinks {
	var w plugin.WebLinks

	page := strings.TrimSuffix(strings.TrimSuffix(remote, "/"), ".git")
	if page == "" {
		return w
	}

	if a.Commit != "" {
		w.Commit = page + "/commit/" + a.Commit
	}

	if a.Commit != "" && a.File != "" {
		w.File = page + "/blob/" + a.Commit + "/" + a.File
	}

	if a.Change != "" {
		w.Change = page + "/changes/" + url.PathEscape(a.Change)
	}

	return w
}

func repository(remote string) string {
	if u, err := url.Parse(remote); err == nil {
		return strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	}

	return ""
}

// changesFrom reads the host's list of changes from the response's body file, in the call's work
// directory.
func changesFrom(work string, resp plugin.HTTPResponse, base string) ([]plugin.Change, error) {
	if resp.Status != 200 { //nolint:mnd // HTTP's OK
		return nil, fmt.Errorf("%w: the host answered %d", errForge, resp.Status)
	}

	body, err := os.ReadFile(filepath.Join(work, filepath.Clean(resp.BodyFile))) //nolint:gosec // a file the core wrote into the call's work directory
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errForge, err)
	}

	var listed []struct {
		ID     string `json:"id"`
		Head   string `json:"head"`
		Branch string `json:"branch"`
	}

	if err := json.Unmarshal(body, &listed); err != nil {
		return nil, fmt.Errorf("%w: the host's list of changes: %w", errForge, err)
	}

	changes := []plugin.Change{}

	for _, c := range listed {
		// the host doesn't say whether a change comes from a fork, so none is trusted as the repository's own
		changes = append(changes, plugin.Change{ID: c.ID, Base: base, Head: c.Head, HeadBranch: c.Branch, From: plugin.FromUnknown})
	}

	return changes, nil
}
