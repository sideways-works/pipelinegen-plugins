package connector

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func pullsHost(t *testing.T, pulls string) *fakeGitHub {
	t.Helper()

	f := &fakeGitHub{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.seen = append(f.seen, sent{Method: r.Method, Path: r.URL.Path})
		f.mu.Unlock()

		switch {
		case r.URL.Path == "/repos/o/r/pulls":
			write(w, pulls)
		case strings.HasSuffix(r.URL.Path, "/timeline"):
			write(w, `[{"event": "ready_for_review", "created_at": "2026-02-01T00:00:00Z"}, {"event": "labeled", "created_at": "2026-03-01T00:00:00Z"}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.Close)

	return f
}

func listChanges(t *testing.T, f *fakeGitHub, args plugin.ChangesArgs) []plugin.Change {
	t.Helper()

	var a plugin.ChangesAnswer
	if err := json.Unmarshal(drive(t, f, plugin.CallChanges, args), &a); err != nil {
		t.Fatal(err)
	}

	return a.Changes
}

func TestWhereAPullRequestComesFrom(t *testing.T) {
	pull := func(n, headID int, headName string) string {
		head := `{"id": ` + itoa(headID) + `, "full_name": "` + headName + `"}`
		if headID == 0 {
			head = "null"
		}

		return `{"number": ` + itoa(n) + `, "draft": ` + map[bool]string{true: "true", false: "false"}[n == 4] + `, "labels": [{"name": "ready"}],
			"created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-02T00:00:00Z",
			"head": {"sha": "` + strings.Repeat(itoa(n), 40) + `", "ref": "b` + itoa(n) + `", "repo": ` + head + `},
			"base": {"sha": "", "ref": "main", "repo": {"id": 1, "full_name": "o/r"}}}`
	}

	f := pullsHost(t, "["+strings.Join([]string{pull(1, 1, "o/r"), pull(2, 9, "someone/r"), pull(3, 5, "o/r"), pull(4, 0, "")}, ",")+"]")

	got := listChanges(t, f, plugin.ChangesArgs{Base: "main"})

	want := map[string]string{"pr-1": plugin.FromSame, "pr-2": plugin.FromFork, "pr-3": plugin.FromFork, "pr-4": plugin.FromUnknown}
	if len(got) != len(want) {
		t.Fatalf("changes: %+v", got)
	}

	for _, c := range got {
		if c.From != want[c.ID] {
			t.Errorf("%s from %q, want %q", c.ID, c.From, want[c.ID])
		}

		if c.Base != "main" || len(c.Head) != 40 || c.Labels[0] != "ready" || c.ReadySince != nil {
			t.Errorf("%s: %+v", c.ID, c)
		}
	}

	if !got[3].Draft || got[0].Draft {
		t.Errorf("drafts: %+v", got)
	}
}

func TestReadySinceComesFromTheTimeline(t *testing.T) {
	f := pullsHost(t, `[{"number": 7, "created_at": "2026-01-01T00:00:00Z", "head": {"sha": "`+headSHA+`", "ref": "b", "repo": {"id": 1}}, "base": {"ref": "main", "repo": {"id": 1}}}]`)

	got := listChanges(t, f, plugin.ChangesArgs{Base: "main", Filters: plugin.ChangesFilters{ReadySince: true}})
	if len(got) != 1 || got[0].ReadySince == nil || got[0].ReadySince.Format("2006-01-02") != "2026-02-01" {
		t.Fatalf("%+v", got)
	}
}

func TestIdentify(t *testing.T) {
	gh := Config{API: DefaultAPI}
	for remote, want := range map[string]string{
		"https://github.com/o/r.git": "o/r", "git@github.com:o/r.git": "o/r", "ssh://git@github.com/o/r": "o/r",
		"https://gitlab.example.test/o/r.git": "", "https://github.com/o": "",
	} {
		id := Identify(gh, remote)
		if id.Mine != (want != "") || id.Repository != want {
			t.Errorf("%s: %+v", remote, id)
		}
	}

	ghes := Identify(Config{API: "https://git.example.test/api/v3"}, "https://git.example.test/team/app.git")
	if !ghes.Mine || ghes.Repository != "team/app" || ghes.HeadRef != "refs/pull/{id}/head" || ghes.Web != "https://git.example.test/team/app" {
		t.Fatalf("%+v", ghes)
	}
}

func TestLinks(t *testing.T) {
	w := Links(Config{Remote: "git@github.com:o/r.git"}, plugin.LinksArgs{Commit: headSHA, File: "a/b.go", Change: "pr-7"})
	if w.Commit != "https://github.com/o/r/commit/"+headSHA || w.File != "https://github.com/o/r/blob/"+headSHA+"/a/b.go" || w.Change != "https://github.com/o/r/pull/7" {
		t.Fatalf("%+v", w)
	}
}

func TestAnUnchangedCommentIsNotWritten(t *testing.T) {
	f := newFakeGitHub(t)

	drive(t, f, plugin.CallComment, plugin.CommentArgs{Change: "pr-7", Marker: "<!-- m -->", Body: "<!-- m -->\nold\n"})

	if writes := writesOf(f); len(writes) != 1 || writes[0] != "DELETE /repos/o/r/issues/comments/42" {
		t.Fatalf("writes: %v", writes)
	}
}

func TestACommentQuotingTheMarkerStays(t *testing.T) {
	f := &fakeGitHub{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.seen = append(f.seen, sent{Method: r.Method, Path: r.URL.Path})
		f.mu.Unlock()

		if r.Method == http.MethodGet {
			write(w, `[{"id": 5, "body": "see <!-- m -->\nabove"}]`)

			return
		}

		w.WriteHeader(http.StatusCreated)
		write(w, "{}")
	}))
	t.Cleanup(f.Close)

	drive(t, f, plugin.CallComment, plugin.CommentArgs{Change: "pr-7", Marker: "<!-- m -->", Body: "<!-- m -->\nbody"})

	if writes := writesOf(f); len(writes) != 1 || writes[0] != "POST /repos/o/r/issues/7/comments" {
		t.Fatalf("writes: %v", writes)
	}
}

func writesOf(f *fakeGitHub) []string {
	var writes []string

	for _, s := range f.take() {
		if s.Method != http.MethodGet {
			writes = append(writes, s.Method+" "+s.Path)
		}
	}

	return writes
}

func TestStatusesOfAnUnknownCommit(t *testing.T) {
	f := &fakeGitHub{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) //nolint:errcheck // nothing to read

		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	t.Cleanup(f.Close)

	var a plugin.StatusesAnswer
	if err := json.Unmarshal(drive(t, f, plugin.CallStatuses, plugin.StatusesArgs{Commit: headSHA}), &a); err != nil || len(a.Statuses) != 0 {
		t.Fatalf("%+v %v", a, err)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestManyTimelinesTakeRounds(t *testing.T) {
	var pulls []string
	for n := 1; n <= 45; n++ {
		pulls = append(pulls, `{"number": `+itoa(n)+`, "created_at": "2026-01-01T00:00:00Z", "head": {"sha": "`+headSHA+`", "ref": "b`+itoa(n)+`", "repo": {"id": 1}}, "base": {"ref": "main", "repo": {"id": 1}}}`)
	}

	f := pullsHost(t, "["+strings.Join(pulls, ",")+"]")
	req := plugin.ExchangeRequest{Protocol: plugin.Protocol, Work: t.TempDir(), Config: map[string]any{"api": f.URL, "remote": "https://github.com/o/r.git"}}

	args, err := json.Marshal(plugin.ChangesArgs{Base: "main", Filters: plugin.ChangesFilters{ReadySince: true}})
	if err != nil {
		t.Fatal(err)
	}

	req.Args = args

	most := 0

	got := driveWith(t, plugin.CallChanges, req, func(n int) { most = max(most, n) })

	var a plugin.ChangesAnswer
	if err := json.Unmarshal(got, &a); err != nil || len(a.Changes) != 45 || most > maxRound {
		t.Fatalf("%d changes, at most %d requests a round: %v", len(a.Changes), most, err)
	}

	for _, c := range a.Changes {
		if c.ReadySince == nil {
			t.Fatalf("%s: no ready_since", c.ID)
		}
	}
}

func TestFiltersNarrowTheTimelines(t *testing.T) {
	pulls := `[{"number": 7, "draft": false, "labels": [{"name": "ci"}], "created_at": "2026-01-01T00:00:00Z", "head": {"sha": "` + headSHA + `", "ref": "feat/a", "repo": {"id": 1}}, "base": {"ref": "main", "repo": {"id": 1}}},
		{"number": 8, "draft": true, "labels": [], "created_at": "2026-01-01T00:00:00Z", "head": {"sha": "` + headSHA + `", "ref": "feat/b", "repo": {"id": 1}}, "base": {"ref": "main", "repo": {"id": 1}}},
		{"number": 9, "draft": false, "labels": [{"name": "wip"}, {"name": "ci"}], "created_at": "2026-01-01T00:00:00Z", "head": {"sha": "` + headSHA + `", "ref": "fix/c", "repo": {"id": 1}}, "base": {"ref": "main", "repo": {"id": 1}}}]`

	ready := false

	for _, c := range []struct {
		f    plugin.ChangesFilters
		want string
	}{
		{plugin.ChangesFilters{}, "pr-7 pr-8 pr-9"},
		{plugin.ChangesFilters{Draft: &ready}, "pr-7 pr-9"},
		{plugin.ChangesFilters{Labels: []string{"ci"}, LabelsWithout: []string{"wip"}}, "pr-7"},
		{plugin.ChangesFilters{Head: "feat/*"}, "pr-7 pr-8"},
		{plugin.ChangesFilters{Head: "fix/c", ReadySince: true}, "pr-9"},
	} {
		f := pullsHost(t, pulls)

		var ids []string
		for _, ch := range listChanges(t, f, plugin.ChangesArgs{Base: "main", Filters: c.f}) {
			ids = append(ids, ch.ID)
		}

		timelines := 0

		for _, s := range f.take() {
			if strings.HasSuffix(s.Path, "/timeline") {
				timelines++
			}
		}

		if strings.Join(ids, " ") != c.want || (c.f.ReadySince && timelines != 1) {
			t.Errorf("%+v: %v, %d timelines", c.f, ids, timelines)
		}
	}
}
