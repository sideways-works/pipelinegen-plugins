// Package connector never sends a request or holds a token: each call answers with the
// requests it needs, which the core sends with the token of their purpose.
package connector

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

const DefaultAPI = "https://api.github.com"

const (
	accept     = "application/vnd.github+json"
	apiVersion = "2022-11-28"
)

const perPage = 100

const maxPages = 10

var ErrGitHub = errors.New("github")

// Config's Remote is filled by the core in every call.
type Config struct {
	// GitHub Enterprise Server: https://<host>/api/v3.
	API    string
	Remote string
}

func ConfigFrom(m map[string]any) Config {
	c := Config{API: DefaultAPI}

	if s, ok := m["api"].(string); ok && s != "" {
		c.API = strings.TrimRight(s, "/")
	}

	if s, ok := m["remote"].(string); ok {
		c.Remote = s
	}

	return c
}

func Describe(version string) plugin.Description {
	return plugin.Description{
		Protocol: plugin.Protocol, Kind: plugin.KindConnector, Name: "github", Version: version,
		Capabilities: []string{
			plugin.CapChanges, plugin.CapStatuses, plugin.CapComment, plugin.CapDraft, plugin.CapLabels,
			plugin.CapReadySince, plugin.CapForks, plugin.CapHeadRef,
		},
		APIHosts:        []string{DefaultAPI},
		RequestHeaders:  []string{"Accept", "X-Github-Api-Version", "Content-Type"},
		ResponseHeaders: []string{"Link"},
		Auth:            &plugin.ConnectorAuth{Header: "Authorization", Scheme: "Bearer"},
		Purposes:        []string{plugin.PurposeRead, plugin.PurposeChanges, plugin.PurposeReport},
		GitUser:         "x-access-token",
		Limits:          &plugin.ConnectorLimits{StatusTextMax: 140, CommentMax: 65536, Markdown: true, Mermaid: true},
	}
}

func Repository(remote string) (string, bool) {
	var rest string

	if after, ok := strings.CutPrefix(remote, "git@"); ok {
		_, rest, _ = strings.Cut(after, ":")
	} else {
		u, err := url.Parse(remote)
		if err != nil || u.Host == "" {
			return "", false
		}

		rest = u.Path
	}

	rest = strings.TrimSuffix(strings.Trim(rest, "/"), ".git")

	owner, name, ok := strings.Cut(rest, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", false
	}

	return owner + "/" + name, true
}

func webHost(remote string) string {
	if after, ok := strings.CutPrefix(remote, "git@"); ok {
		host, _, _ := strings.Cut(after, ":")

		return host
	}

	if u, err := url.Parse(remote); err == nil {
		return u.Hostname()
	}

	return ""
}

// Identify claims a remote on github.com, or on the host of a configured API.
func Identify(c Config, remote string) plugin.Identity {
	repo, ok := Repository(remote)
	host := webHost(remote)

	apiHost := ""
	if u, err := url.Parse(c.API); err == nil {
		apiHost = u.Hostname()
	}

	mine := ok && (host == "github.com" || (c.API != DefaultAPI && apiHost == host))
	if !mine {
		return plugin.Identity{}
	}

	return plugin.Identity{
		Mine: true, Repository: repo, Web: "https://" + host + "/" + repo,
		GitRemote: "https://" + host + "/" + repo + ".git", HeadRef: "refs/pull/{id}/head",
	}
}

func (c Config) repoPath() (string, error) {
	repo, ok := Repository(c.Remote)
	if !ok {
		return "", fmt.Errorf("%w: remote %q names no repository", ErrGitHub, c.Remote)
	}

	owner, name, _ := strings.Cut(repo, "/")

	return c.API + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name), nil
}

func request(id, method, u, purpose string, body any) (plugin.HTTPRequest, error) {
	r := plugin.HTTPRequest{
		ID: id, Method: method, URL: u,
		Headers: map[string]string{"Accept": accept, "X-Github-Api-Version": apiVersion},
		Auth:    &plugin.HTTPAuth{Purpose: purpose},
	}

	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return r, fmt.Errorf("%w: %w", ErrGitHub, err)
		}

		r.Body = string(data)
		r.Headers["Content-Type"] = "application/json"
	}

	return r, nil
}

// withoutVersion: the pull request reads have always gone without the version header,
// and their answers are read as the default version gives them.
func withoutVersion(r *plugin.HTTPRequest) { delete(r.Headers, "X-Github-Api-Version") }

type Body func(file string) ([]byte, error)

func decode(r plugin.HTTPResponse, read Body, out any) error {
	data, err := read(r.BodyFile)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrGitHub, r.ID, err)
	}

	if r.Status < 200 || r.Status > 299 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 300 {
			msg = msg[:300]
		}

		return fmt.Errorf("%w: %s: %d: %s", ErrGitHub, r.ID, r.Status, msg)
	}

	if out == nil || len(data) == 0 {
		return nil
	}

	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrGitHub, r.ID, err)
	}

	return nil
}

func done(v any) (plugin.Round, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return plugin.Round{}, fmt.Errorf("%w: %w", ErrGitHub, err)
	}

	return plugin.Round{Done: data}, nil
}

func Round(call string, req plugin.ExchangeRequest, read Body) (plugin.Round, error) {
	c := ConfigFrom(req.Config)

	switch call {
	case plugin.CallIdentify:
		var a plugin.IdentifyArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			return plugin.Round{}, fmt.Errorf("%w: args: %w", ErrGitHub, err)
		}

		return done(Identify(c, a.Remote))
	case plugin.CallLinks:
		var a plugin.LinksArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			return plugin.Round{}, fmt.Errorf("%w: args: %w", ErrGitHub, err)
		}

		return done(Links(c, a))
	case plugin.CallChanges:
		return changes(c, req, read)
	case plugin.CallStatus:
		return status(c, req, read)
	case plugin.CallStatuses:
		return statuses(c, req, read)
	case plugin.CallSubject:
		return subject(c, req, read)
	case plugin.CallComment:
		return comment(c, req, read)
	}

	return plugin.Round{}, fmt.Errorf("%w: no call %q", ErrGitHub, call)
}

func Links(c Config, a plugin.LinksArgs) plugin.WebLinks {
	repo, ok := Repository(c.Remote)
	if !ok {
		return plugin.WebLinks{}
	}

	web := "https://" + webHost(c.Remote) + "/" + repo

	var w plugin.WebLinks

	if a.Commit != "" {
		w.Commit = web + "/commit/" + a.Commit

		if a.File != "" {
			w.File = web + "/blob/" + a.Commit + "/" + a.File
		}
	}

	if n, ok := strings.CutPrefix(a.Change, "pr-"); ok {
		w.Change = web + "/pull/" + n
	}

	return w
}

func pageURL(base string, n int) string {
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}

	return base + sep + "per_page=" + strconv.Itoa(perPage) + "&page=" + strconv.Itoa(n)
}

func paged[T any](prefix, purpose, base string, responses []plugin.HTTPResponse, read Body, pages int, version bool) ([]T, *plugin.HTTPRequest, error) {
	var all []T

	n := 0

	for _, r := range responses {
		if !strings.HasPrefix(r.ID, prefix+"-") {
			continue
		}

		var got []T
		if err := decode(r, read, &got); err != nil {
			return nil, nil, err
		}

		all = append(all, got...)
		n++

		if len(got) < perPage {
			return all, nil, nil
		}
	}

	if n >= pages {
		return all, nil, nil
	}

	r, err := request(fmt.Sprintf("%s-%d", prefix, n+1), "GET", pageURL(base, n+1), purpose, nil)
	if err != nil {
		return nil, nil, err
	}

	if !version {
		withoutVersion(&r)
	}

	return nil, &r, nil
}

type timeOf struct {
	time.Time
}

func (t *timeOf) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}

	if err := json.Unmarshal(b, &t.Time); err != nil {
		return fmt.Errorf("%w: time: %w", ErrGitHub, err)
	}

	return nil
}
