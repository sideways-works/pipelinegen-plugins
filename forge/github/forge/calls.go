package forge

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

type listed struct {
	Number    int     `json:"number"`
	Draft     bool    `json:"draft"`
	CreatedAt timeOf  `json:"created_at"`
	UpdatedAt timeOf  `json:"updated_at"`
	Labels    []label `json:"labels"`
	Head      side    `json:"head"`
	Base      side    `json:"base"`
}

type label struct {
	Name string `json:"name"`
}

type side struct {
	SHA  string `json:"sha"`
	Ref  string `json:"ref"`
	Repo *struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	} `json:"repo"`
}

// from compares repository ids: a fork, or a repository deleted and made again under
// the same name, has another id; a head repository that is gone can't be told.
func from(p listed) string {
	switch {
	case p.Head.Repo == nil || p.Base.Repo == nil || p.Head.Repo.ID == 0:
		return plugin.FromUnknown
	case p.Head.Repo.ID == p.Base.Repo.ID:
		return plugin.FromSame
	default:
		return plugin.FromFork
	}
}

func changes(c Config, req plugin.ExchangeRequest, read Body) (plugin.Round, error) {
	var a plugin.ChangesArgs
	if err := json.Unmarshal(req.Args, &a); err != nil {
		return plugin.Round{}, fmt.Errorf("%w: args: %w", ErrGitHub, err)
	}

	repo, err := c.repoPath()
	if err != nil {
		return plugin.Round{}, err
	}

	pulls, next, err := paged[listed]("pulls", plugin.PurposeChanges, repo+"/pulls?state=open&base="+url.QueryEscape(a.Base), req.Responses, read, maxPages, false)
	if err != nil || next != nil {
		return roundOf(next, err)
	}

	pulls = narrowed(pulls, a.Filters)

	if !a.Filters.ReadySince {
		return done(plugin.ChangesAnswer{Changes: changesOf(pulls, a.Base, nil)})
	}

	ready, more, err := readyTimes(repo, pulls, req.Responses, read)
	if err != nil || len(more) > 0 {
		return plugin.Round{Requests: more}, err
	}

	return done(plugin.ChangesAnswer{Changes: changesOf(pulls, a.Base, ready)})
}

// narrowed only saves timeline requests: the core applies the run's filters again.
func narrowed(pulls []listed, f plugin.ChangesFilters) []listed {
	out := make([]listed, 0, len(pulls))

	for _, p := range pulls {
		labels := make([]string, 0, len(p.Labels))
		for _, l := range p.Labels {
			labels = append(labels, l.Name)
		}

		has := func(l string) bool { return slices.Contains(labels, l) }

		switch {
		case f.Draft != nil && *f.Draft != p.Draft,
			!all(f.Labels, has),
			slices.ContainsFunc(f.LabelsWithout, has):
			continue
		case f.Head != "":
			if ok, err := path.Match(f.Head, p.Head.Ref); err != nil || !ok {
				continue
			}
		}

		out = append(out, p)
	}

	return out
}

func all(names []string, ok func(string) bool) bool {
	for _, n := range names {
		if !ok(n) {
			return false
		}
	}

	return true
}

func roundOf(next *plugin.HTTPRequest, err error) (plugin.Round, error) {
	if err != nil {
		return plugin.Round{}, err
	}

	return plugin.Round{Requests: []plugin.HTTPRequest{*next}}, nil
}

func changesOf(pulls []listed, base string, ready map[int]time.Time) []plugin.Change {
	out := make([]plugin.Change, 0, len(pulls))

	for _, p := range pulls {
		ch := plugin.Change{
			ID: "pr-" + strconv.Itoa(p.Number), Number: p.Number, Base: base, Head: p.Head.SHA, HeadBranch: p.Head.Ref,
			Draft: p.Draft, From: from(p), Updated: p.UpdatedAt.Time,
		}

		for _, l := range p.Labels {
			ch.Labels = append(ch.Labels, l.Name)
		}

		if t, ok := ready[p.Number]; ok {
			ch.ReadySince = &t
		}

		out = append(out, ch)
	}

	return out
}

// maxRound is the most requests the core sends in one round.
const maxRound = 20

func readyTimes(repo string, pulls []listed, responses []plugin.HTTPResponse, read Body) (map[int]time.Time, []plugin.HTTPRequest, error) {
	type event struct {
		Event     string `json:"event"`
		CreatedAt timeOf `json:"created_at"`
	}

	got := map[string]plugin.HTTPResponse{}

	for _, r := range responses {
		if strings.HasPrefix(r.ID, "timeline-") {
			got[r.ID] = r
		}
	}

	ready := map[int]time.Time{}

	var more []plugin.HTTPRequest

	for _, p := range pulls {
		id := "timeline-" + strconv.Itoa(p.Number)

		r, ok := got[id]
		if !ok {
			q, err := request(id, "GET", pageURL(repo+"/issues/"+strconv.Itoa(p.Number)+"/timeline", 1), plugin.PurposeChanges, nil)
			if err != nil {
				return nil, nil, err
			}

			withoutVersion(&q)

			// Requests past the cap are asked again next round.
			if len(more) < maxRound {
				more = append(more, q)
			}

			continue
		}

		var events []event
		if err := decode(r, read, &events); err != nil {
			return nil, nil, err
		}

		since := p.CreatedAt.Time

		for _, e := range events {
			if e.Event == "ready_for_review" && e.CreatedAt.After(since) {
				since = e.CreatedAt.Time
			}
		}

		ready[p.Number] = since
	}

	return ready, more, nil
}

func status(c Config, req plugin.ExchangeRequest, read Body) (plugin.Round, error) {
	var a plugin.StatusArgs
	if err := json.Unmarshal(req.Args, &a); err != nil {
		return plugin.Round{}, fmt.Errorf("%w: args: %w", ErrGitHub, err)
	}

	if len(req.Responses) > 0 {
		if err := decode(req.Responses[0], read, nil); err != nil {
			return plugin.Round{}, err
		}

		return done(struct{}{})
	}

	if !slices.Contains(plugin.States, a.State) {
		return plugin.Round{}, fmt.Errorf("%w: state %q", ErrGitHub, a.State)
	}

	repo, err := c.repoPath()
	if err != nil {
		return plugin.Round{}, err
	}

	r, err := request("status", "POST", repo+"/statuses/"+url.PathEscape(a.Commit), plugin.PurposeReport, statusBody{
		State: a.State, Context: a.Context, Description: a.Text, TargetURL: a.URL,
	})
	if err != nil {
		return plugin.Round{}, err
	}

	return plugin.Round{Requests: []plugin.HTTPRequest{r}}, nil
}

type statusBody struct {
	State       string `json:"state"`
	Context     string `json:"context"`
	Description string `json:"description"`
	TargetURL   string `json:"target_url,omitempty"`
}

func statuses(c Config, req plugin.ExchangeRequest, read Body) (plugin.Round, error) {
	var a plugin.StatusesArgs
	if err := json.Unmarshal(req.Args, &a); err != nil {
		return plugin.Round{}, fmt.Errorf("%w: args: %w", ErrGitHub, err)
	}

	repo, err := c.repoPath()
	if err != nil {
		return plugin.Round{}, err
	}

	type combined struct {
		Statuses []struct {
			Context     string `json:"context"`
			State       string `json:"state"`
			Description string `json:"description"`
			TargetURL   string `json:"target_url"`
			UpdatedAt   timeOf `json:"updated_at"`
		} `json:"statuses"`
	}

	var all plugin.StatusesAnswer

	n := 0

	for _, r := range req.Responses {
		// A commit GitHub doesn't know has no statuses.
		if r.Status == 404 || r.Status == 422 {
			return done(plugin.StatusesAnswer{Statuses: []plugin.Status{}})
		}

		var page combined
		if err := decode(r, read, &page); err != nil {
			return plugin.Round{}, err
		}

		for _, s := range page.Statuses {
			st := plugin.Status{Context: s.Context, State: s.State, Text: s.Description, URL: s.TargetURL}
			if !s.UpdatedAt.IsZero() {
				t := s.UpdatedAt.Time
				st.Updated = &t
			}

			all.Statuses = append(all.Statuses, st)
		}

		n++

		if len(page.Statuses) < perPage {
			if all.Statuses == nil {
				all.Statuses = []plugin.Status{}
			}

			return done(all)
		}
	}

	r, err := request(fmt.Sprintf("statuses-%d", n+1), "GET", pageURL(repo+"/commits/"+url.PathEscape(a.Commit)+"/status", n+1), plugin.PurposeReport, nil)
	if err != nil {
		return plugin.Round{}, err
	}

	return plugin.Round{Requests: []plugin.HTTPRequest{r}}, nil
}

func subject(c Config, req plugin.ExchangeRequest, read Body) (plugin.Round, error) {
	var a plugin.SubjectArgs
	if err := json.Unmarshal(req.Args, &a); err != nil {
		return plugin.Round{}, fmt.Errorf("%w: args: %w", ErrGitHub, err)
	}

	if len(req.Responses) > 0 {
		var commit struct {
			Commit struct {
				Message string `json:"message"`
			} `json:"commit"`
		}

		if err := decode(req.Responses[0], read, &commit); err != nil {
			return plugin.Round{}, err
		}

		first, _, _ := strings.Cut(commit.Commit.Message, "\n")

		return done(plugin.SubjectAnswer{Subject: strings.TrimSpace(first)})
	}

	repo, err := c.repoPath()
	if err != nil {
		return plugin.Round{}, err
	}

	r, err := request("commit", "GET", repo+"/commits/"+url.PathEscape(a.Commit), plugin.PurposeReport, nil)
	if err != nil {
		return plugin.Round{}, err
	}

	return plugin.Round{Requests: []plugin.HTTPRequest{r}}, nil
}

type issueComment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	UpdatedAt timeOf `json:"updated_at"`
}

// comment keeps one comment starting with the marker: the oldest is updated, the others
// deleted. A comment that only quotes the marker further down is someone else's.
func comment(c Config, req plugin.ExchangeRequest, read Body) (plugin.Round, error) {
	var a plugin.CommentArgs
	if err := json.Unmarshal(req.Args, &a); err != nil {
		return plugin.Round{}, fmt.Errorf("%w: args: %w", ErrGitHub, err)
	}

	number, ok := strings.CutPrefix(a.Change, "pr-")
	if _, err := strconv.Atoi(number); !ok || err != nil || a.Marker == "" {
		return plugin.Round{}, fmt.Errorf("%w: change %q and marker %q", ErrGitHub, a.Change, a.Marker)
	}

	repo, err := c.repoPath()
	if err != nil {
		return plugin.Round{}, err
	}

	// Writes are the last round.
	for _, r := range req.Responses {
		if strings.HasPrefix(r.ID, "write-") {
			for _, w := range req.Responses {
				if strings.HasPrefix(w.ID, "write-") {
					if err := decode(w, read, nil); err != nil {
						return plugin.Round{}, err
					}
				}
			}

			return done(struct{}{})
		}
	}

	all, next, err := paged[issueComment]("comments", plugin.PurposeReport, repo+"/issues/"+number+"/comments", req.Responses, read, 1<<20, true)
	if err != nil || next != nil {
		return roundOf(next, err)
	}

	writes, err := commentWrites(repo, number, a, all)
	if err != nil {
		return plugin.Round{}, err
	}

	if len(writes) == 0 {
		return done(struct{}{})
	}

	return plugin.Round{Requests: writes}, nil
}

func commentWrites(repo, number string, a plugin.CommentArgs, all []issueComment) ([]plugin.HTTPRequest, error) {
	var mine []issueComment

	for _, cm := range all {
		if body := normal(cm.Body); strings.HasPrefix(body, a.Marker+"\n") || body == a.Marker {
			mine = append(mine, cm)
		}
	}

	slices.SortFunc(mine, func(x, y issueComment) int { return cmp.Compare(x.ID, y.ID) })

	var writes []plugin.HTTPRequest

	add := func(method, u string, body any) error {
		r, err := request(fmt.Sprintf("write-%d", len(writes)+1), method, u, plugin.PurposeReport, body)
		writes = append(writes, r)

		return err
	}

	if len(mine) == 0 {
		return writes, add("POST", repo+"/issues/"+number+"/comments", map[string]string{"body": a.Body})
	}

	keep := mine[0]

	if normal(keep.Body) != normal(a.Body) {
		if err := add("PATCH", repo+"/issues/comments/"+strconv.FormatInt(keep.ID, 10), map[string]string{"body": a.Body}); err != nil {
			return nil, err
		}
	}

	for _, cm := range mine[1:] {
		if err := add("DELETE", repo+"/issues/comments/"+strconv.FormatInt(cm.ID, 10), nil); err != nil {
			return nil, err
		}
	}

	return writes, nil
}

// normal drops what GitHub may change in a body it gives back: line ends and trailing space.
func normal(body string) string {
	return strings.TrimRight(strings.ReplaceAll(body, "\r\n", "\n"), " \n\t")
}
