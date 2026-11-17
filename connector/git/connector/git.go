// Package connector is the plain git connector's logic, for a git server
// with no host API. Its proposed changes are the branches whose names match
// the config's patterns. pipelinegen lists the remote's branches with git
// and its read credential, and hands them in; the connector sends no
// request, holds no token, and reports nowhere: a run's state is in its
// records.
package connector

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"
)

// Name is the connector's plugin name in the config.
const Name = "git"

// IDPrefix starts every change id the connector gives: a branch, its name
// encoded as one ref part.
const IDPrefix = "branch-"

// ErrGit is a call the connector can't answer.
var ErrGit = errors.New("git connector")

// Config is the connector's block. The core puts the code's remote in
// every call, and the remote's branches, by name, in heads.
type Config struct {
	Remote   string
	Branches []string
	Heads    map[string]string
	Links    string
}

var commitID = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// ConfigFrom reads the connector's block.
func ConfigFrom(m map[string]any) (Config, error) {
	var c Config

	for key, dest := range map[string]*string{"remote": &c.Remote, "links": &c.Links} {
		if v, ok := m[key].(string); ok {
			*dest = v
		}
	}

	if raw, ok := m["branches"].([]any); ok {
		for _, b := range raw {
			s, ok := b.(string)
			if !ok {
				return c, fmt.Errorf("%w: branches: %v is no pattern", ErrGit, b)
			}

			c.Branches = append(c.Branches, s)
		}
	}

	if raw, ok := m["heads"].(map[string]any); ok {
		c.Heads = make(map[string]string, len(raw))

		for branch, v := range raw {
			sha, ok := v.(string)
			if !ok || !commitID.MatchString(sha) {
				return c, fmt.Errorf("%w: heads: %s: %v is no commit", ErrGit, branch, v)
			}

			c.Heads[branch] = sha
		}
	}

	return c, nil
}

// Describe is what the connector can tell: changes, each from the
// repository itself, since a plain git server has no forks, and nothing a
// host would keep (statuses, comments, drafts, labels).
func Describe(version string) plugin.Description {
	return plugin.Description{
		Protocol: plugin.Protocol, Kind: plugin.KindConnector, Name: Name, Version: version,
		Capabilities: []string{plugin.CapChanges, plugin.CapForks},
	}
}

// Identify claims the configured remote and no other.
func Identify(c Config, remote string) plugin.Identity {
	if c.Remote == "" || strings.TrimSuffix(remote, "/") != strings.TrimSuffix(c.Remote, "/") {
		return plugin.Identity{}
	}

	return plugin.Identity{Mine: true, Repository: repository(remote), GitRemote: remote}
}

// repository is a remote's path without its .git: owner/name on most
// servers.
func repository(remote string) string {
	p := remote

	if u, err := url.Parse(remote); err == nil && u.Scheme != "" {
		p = u.Path
	} else if _, after, ok := strings.Cut(remote, ":"); ok {
		p = after
	}

	return strings.TrimSuffix(strings.Trim(p, "/"), ".git")
}

// ChangeID is a branch's change id.
func ChangeID(branch string) string { return IDPrefix + RefPart(branch) }

// Changes are the heads whose names match one of the patterns (path.Match),
// but base, as proposed changes onto base, in branch order.
func Changes(c Config, base string) []plugin.Change {
	out := []plugin.Change{}

	for branch, sha := range c.Heads {
		if branch == base || !matches(branch, c.Branches) {
			continue
		}

		out = append(out, plugin.Change{ID: ChangeID(branch), Base: base, Head: sha, HeadBranch: branch, From: plugin.FromSame})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].HeadBranch < out[j].HeadBranch })

	return out
}

func matches(branch string, patterns []string) bool {
	for _, p := range patterns {
		if ok, err := path.Match(p, branch); err == nil && ok {
			return true
		}
	}

	return false
}

// Links fills the config's page template for what is asked: {commit},
// {branch} and {path}. Without a template there is no page.
func Links(c Config, a plugin.LinksArgs) plugin.WebLinks {
	if c.Links == "" {
		return plugin.WebLinks{}
	}

	branch := ""
	if part, ok := strings.CutPrefix(a.Change, IDPrefix); ok {
		branch, _ = url.PathUnescape(part) //nolint:errcheck // a branch that won't decode fills in empty
	}

	fill := func(commit, file string) string {
		return strings.NewReplacer("{commit}", commit, "{branch}", branch, "{path}", file).Replace(c.Links)
	}

	var w plugin.WebLinks

	if a.Commit != "" {
		w.Commit = fill(a.Commit, "")
	}

	if a.File != "" {
		w.File = fill(a.Commit, a.File)
	}

	if a.Change != "" && branch != "" {
		w.Change = fill(a.Commit, "")
	}

	return w
}

// Calls are the calls the connector answers besides describe.
var Calls = []string{plugin.CallIdentify, plugin.CallChanges, plugin.CallLinks}

// Round answers one round of a call. No call asks for a request: each
// answers at once.
func Round(call string, req plugin.ExchangeRequest) (plugin.Round, error) {
	c, err := ConfigFrom(req.Config)
	if err != nil {
		return plugin.Round{}, err
	}

	var answer any

	switch call {
	case plugin.CallIdentify:
		var a plugin.IdentifyArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			return plugin.Round{}, fmt.Errorf("%w: args: %w", ErrGit, err)
		}

		answer = Identify(c, a.Remote)
	case plugin.CallChanges:
		var a plugin.ChangesArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			return plugin.Round{}, fmt.Errorf("%w: args: %w", ErrGit, err)
		}

		answer = plugin.ChangesAnswer{Changes: Changes(c, a.Base)}
	case plugin.CallLinks:
		var a plugin.LinksArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			return plugin.Round{}, fmt.Errorf("%w: args: %w", ErrGit, err)
		}

		answer = Links(c, a)
	default:
		return plugin.Round{}, fmt.Errorf("%w: no call %q: a plain git server keeps no statuses or comments", ErrGit, call)
	}

	data, err := json.Marshal(answer)
	if err != nil {
		return plugin.Round{}, fmt.Errorf("%w: %w", ErrGit, err)
	}

	return plugin.Round{Done: data}, nil
}
