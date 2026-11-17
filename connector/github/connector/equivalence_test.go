package connector

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

const (
	headSHA  = "1111111111111111111111111111111111111111"
	otherSHA = "2222222222222222222222222222222222222222"
	token    = "a-token-for-the-fake-host"
)

type sent struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Query   url.Values        `json:"query"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

func (s sent) key() string {
	p := regexp.MustCompile(`[0-9a-f]{40}`).ReplaceAllString(s.Path, "{sha}")
	p = regexp.MustCompile(`/[0-9]+(/|$)`).ReplaceAllString(p, "/{n}$1")

	return s.Method + " " + p
}

type fakeGitHub struct {
	*httptest.Server

	mu   sync.Mutex
	seen []sent
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()

	f := &fakeGitHub{}
	f.Server = httptest.NewServer(f)
	t.Cleanup(f.Close)

	return f
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body) //nolint:errcheck // a body that can't be read is recorded empty

	s := sent{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Headers: map[string]string{}, Body: string(body)}
	for _, h := range []string{"Accept", "X-Github-Api-Version", "Content-Type", "Authorization"} {
		if v := r.Header.Get(h); v != "" {
			s.Headers[h] = v
		}
	}

	f.mu.Lock()
	f.seen = append(f.seen, s)
	f.mu.Unlock()

	repo := `{"id": 1, "full_name": "o/r"}`
	pull := `{"number": 7, "state": "open", "draft": false, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-02T00:00:00Z",
		"labels": [{"name": "ready"}], "head": {"sha": "` + headSHA + `", "ref": "feature", "repo": ` + repo + `}, "base": {"sha": "", "ref": "main", "repo": ` + repo + `}}`

	k := s.key()

	switch {
	case k == "GET /repos/o/r/pulls":
		write(w, "["+pull+"]")
	case k == "GET /repos/o/r/pulls/{n}":
		write(w, pull)
	case k == "GET /repos/o/r/pulls/{n}/commits":
		write(w, `[{"sha": "`+otherSHA+`"}, {"sha": "`+headSHA+`"}]`)
	case k == "GET /repos/o/r/issues/{n}/timeline":
		write(w, `[{"event": "ready_for_review", "created_at": "2026-01-03T00:00:00Z"}]`)
	case k == "GET /repos/o/r/commits/{sha}/statuses":
		write(w, `[]`)
	case k == "GET /repos/o/r/commits/{sha}/status":
		write(w, `{"statuses": [{"context": "pipelinegen/pr", "state": "success", "description": "passed"}]}`)
	case k == "GET /repos/o/r/commits/{sha}":
		write(w, `{"commit": {"message": "the subject\n\nand a body"}}`)
	case k == "GET /repos/o/r/issues/{n}/comments":
		write(w, `[{"id": 41, "body": "<!-- m -->\nold"}, {"id": 42, "body": "<!-- m -->\nnewer"}]`)
	case r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusCreated)
		write(w, "{}")
	}
}

func write(w http.ResponseWriter, s string) {
	if _, err := io.WriteString(w, s); err != nil {
		return
	}
}

func (f *fakeGitHub) take() []sent {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := f.seen
	f.seen = nil

	return out
}

// oldCalls are the requests pipelinegen's own GitHub clients sent before
// the connector replaced them, one per kind, recorded against this fake.
func oldCalls(t *testing.T) map[string]sent {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", "requests.json"))
	if err != nil {
		t.Fatal(err)
	}

	var old map[string]sent
	if err := json.Unmarshal(data, &old); err != nil {
		t.Fatal(err)
	}

	return old
}

func drive(t *testing.T, f *fakeGitHub, call string, args any) json.RawMessage {
	t.Helper()

	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}

	req := plugin.ExchangeRequest{Protocol: plugin.Protocol, Work: t.TempDir(), Config: map[string]any{"api": f.URL, "remote": "https://github.com/o/r.git"}, Args: raw}

	return driveWith(t, call, req, func(int) {})
}

// driveWith sends each round's requests as the core does, telling round
// how many a round asked for.
func driveWith(t *testing.T, call string, req plugin.ExchangeRequest, round func(n int)) json.RawMessage {
	t.Helper()

	work := req.Work
	read := func(file string) ([]byte, error) { return os.ReadFile(filepath.Join(work, file)) }

	for range 10 {
		r, err := Round(call, req, read)
		if err != nil {
			t.Fatalf("%s: %v", call, err)
		}

		if len(r.Done) > 0 {
			return r.Done
		}

		round(len(r.Requests))

		for _, q := range r.Requests {
			hr, err := http.NewRequestWithContext(t.Context(), q.Method, q.URL, strings.NewReader(q.Body))
			if err != nil {
				t.Fatal(err)
			}

			for k, v := range q.Headers {
				hr.Header.Set(k, v)
			}

			if q.Auth != nil {
				hr.Header.Set("Authorization", "Bearer "+token)
			}

			resp, err := http.DefaultClient.Do(hr)
			if err != nil {
				t.Fatal(err)
			}

			body, _ := io.ReadAll(resp.Body) //nolint:errcheck // a test server's own body
			_ = resp.Body.Close()

			name := fmt.Sprintf("r%d.body", len(req.Responses))
			if err := os.WriteFile(filepath.Join(work, name), body, 0o600); err != nil {
				t.Fatal(err)
			}

			req.Responses = append(req.Responses, plugin.HTTPResponse{ID: q.ID, Status: resp.StatusCode, BodyFile: name})
		}
	}

	t.Fatalf("%s: no answer in 10 rounds", call)

	return nil
}

type row struct {
	call       string
	args       any
	queryAdded url.Values
	replaced   string
}

var table = map[string]row{
	"POST /repos/o/r/statuses/{sha}": {call: plugin.CallStatus, args: plugin.StatusArgs{Commit: headSHA, Context: "pipelinegen/pr", State: "pending", Text: "planning", URL: "https://ci.example.test/b/1"}},
	"GET /repos/o/r/commits/{sha}/status": {
		call: plugin.CallStatuses, args: plugin.StatusesArgs{Commit: headSHA},
		// last_passed's check read one page; statuses names its page
		queryAdded: url.Values{"page": {"1"}},
	},
	"GET /repos/o/r/commits/{sha}":          {call: plugin.CallSubject, args: plugin.SubjectArgs{Commit: headSHA}},
	"GET /repos/o/r/issues/{n}/comments":    {call: plugin.CallComment, args: plugin.CommentArgs{Change: "pr-7", Marker: "<!-- m -->", Body: "<!-- m --> body"}},
	"POST /repos/o/r/issues/{n}/comments":   {call: plugin.CallComment, args: plugin.CommentArgs{Change: "pr-7", Marker: "<!-- x -->", Body: "<!-- m --> body"}},
	"PATCH /repos/o/r/issues/comments/{n}":  {call: plugin.CallComment, args: plugin.CommentArgs{Change: "pr-7", Marker: "<!-- m -->", Body: "<!-- m --> body"}},
	"DELETE /repos/o/r/issues/comments/{n}": {call: plugin.CallComment, args: plugin.CommentArgs{Change: "pr-7", Marker: "<!-- m -->", Body: "<!-- m --> body"}},
	"GET /repos/o/r/pulls":                  {call: plugin.CallChanges, args: plugin.ChangesArgs{Base: "main"}},
	"GET /repos/o/r/issues/{n}/timeline": {
		call: plugin.CallChanges, args: plugin.ChangesArgs{Base: "main", Filters: plugin.ChangesFilters{ReadySince: true}},
	},
	"GET /repos/o/r/pulls/{n}": {
		replaced: "admission reads the pull request's state, draft, labels and head from the changes listing",
	},
	"GET /repos/o/r/pulls/{n}/commits": {
		replaced: "once_per_ready finds an earlier head's run in the run's own records, not in a pull request's commits",
	},
	"GET /repos/o/r/commits/{sha}/statuses": {
		replaced: "once_per_ready finds an earlier head's run in the run's own records, not in its statuses",
	},
}

func TestTheConnectorSendsWhatTheClientsSent(t *testing.T) {
	f := newFakeGitHub(t)

	old := oldCalls(t)

	var missing []string

	for k := range old {
		if _, ok := table[k]; !ok {
			missing = append(missing, k)
		}
	}

	sort.Strings(missing)

	if len(missing) > 0 {
		t.Fatalf("requests of the old clients with no row: %v", missing)
	}

	for _, k := range sortedRows() {
		r := table[k]
		o, made := old[k]

		switch {
		case !made:
			t.Errorf("%s: a row for a request no old client made", k)

			continue
		case r.replaced != "":
			continue
		}

		drive(t, f, r.call, r.args)

		var got *sent

		for _, s := range f.take() {
			if s.key() == k && got == nil {
				got = &s
			}
		}

		if got == nil {
			t.Errorf("%s: the connector's %s didn't send it", k, r.call)

			continue
		}

		want := o.Query
		for q, v := range r.queryAdded {
			want = cloneValues(want)
			want[q] = v
		}

		if !equalValues(got.Query, want) || !maps(got.Headers, o.Headers) || !sameBody(got.Body, o.Body) {
			t.Errorf("%s:\n got %+v\nwant %+v (query %v)", k, *got, o, want)
		}
	}
}

func sortedRows() []string {
	out := make([]string, 0, len(table))
	for k := range table {
		out = append(out, k)
	}

	sort.Strings(out)

	return out
}

func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vs := range v {
		out[k] = slices.Clone(vs)
	}

	return out
}

func equalValues(a, b url.Values) bool {
	if len(a) != len(b) {
		return false
	}

	for k, v := range a {
		if !slices.Equal(v, b[k]) {
			return false
		}
	}

	return true
}

func maps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}

	for k, v := range a {
		if b[k] != v {
			return false
		}
	}

	return true
}

func sameBody(a, b string) bool {
	var x, y map[string]string
	if json.Unmarshal([]byte(a), &x) == nil && json.Unmarshal([]byte(b), &y) == nil {
		return maps(x, y)
	}

	return a == b
}
