package forge

import (
	"encoding/json"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

const (
	shaA    = "1111111111111111111111111111111111111111"
	shaB    = "2222222222222222222222222222222222222222"
	shaC    = "3333333333333333333333333333333333333333"
	shaWip  = "4444444444444444444444444444444444444444"
	shaMain = "5555555555555555555555555555555555555555"
)

func config(patterns ...string) Config {
	return Config{
		Remote: "file:///srv/r.git", Branches: patterns,
		Heads: map[string]string{"review/a": shaA, "review/b": shaB, "review/team/c": shaC, "personal/wip": shaWip, "main": shaMain},
	}
}

func TestChangesAreMatchingBranches(t *testing.T) {
	got := Changes(config("review/*", "main"), "main")

	want := []plugin.Change{
		{ID: "branch-review%2Fa", Base: "main", Head: shaA, HeadBranch: "review/a", From: plugin.FromSame},
		{ID: "branch-review%2Fb", Base: "main", Head: shaB, HeadBranch: "review/b", From: plugin.FromSame},
	}

	if len(got) != len(want) {
		t.Fatalf("changes: %+v", got)
	}

	for i := range want {
		if got[i].ID != want[i].ID || got[i].Head != want[i].Head || got[i].HeadBranch != want[i].HeadBranch || got[i].Base != "main" || got[i].From != plugin.FromSame {
			t.Errorf("change %d: %+v, want %+v", i, got[i], want[i])
		}
	}

	if nested := Changes(config("review/*/*"), "main"); len(nested) != 1 || nested[0].HeadBranch != "review/team/c" {
		t.Fatalf("nested: %+v", nested)
	}
}

func TestChangeIDsKeepTheirRecords(t *testing.T) {
	for branch, id := range map[string]string{
		"review/a":        "branch-review%2Fa",
		"release/1.2":     "branch-release%2F1.2",
		".hidden":         "branch-%2Ehidden",
		"a..b":            "branch-a.%2Eb",
		"x.lock":          "branch-x%2Elock",
		"feature/naïve":   "branch-feature%2Fna%C3%AFve",
		"fix/#12 @home~1": "branch-fix%2F%2312%20%40home%7E1",
	} {
		if got := ChangeID(branch); got != id {
			t.Errorf("%s: %s, want %s", branch, got, id)
		}
	}
}

func TestConfigRefusesWhatIsNoCommit(t *testing.T) {
	if _, err := ConfigFrom(map[string]any{"heads": map[string]any{"review/a": "main"}}); err == nil {
		t.Fatal("a head that is no commit")
	}
}

func TestIdentifyClaimsOnlyTheRemote(t *testing.T) {
	c := config()

	if id := Identify(c, "file:///srv/r.git"); !id.Mine || id.Repository != "srv/r" || id.GitRemote != "file:///srv/r.git" {
		t.Fatalf("%+v", id)
	}

	if id := Identify(c, "https://elsewhere.invalid/r.git"); id.Mine {
		t.Fatalf("%+v", id)
	}

	if r := repository("git@git.example.test:team/app.git"); r != "team/app" {
		t.Fatal(r)
	}
}

func TestDescribe(t *testing.T) {
	d := Describe("test")
	if d.Kind != plugin.KindForge || len(d.Capabilities) != 2 || d.Capabilities[0] != plugin.CapChanges || d.Auth != nil || len(d.APIHosts) != 0 {
		t.Fatalf("%+v", d)
	}
}

func TestLinks(t *testing.T) {
	c := Config{Links: "https://cgit.example.test/r/tree/{path}?id={commit}&h={branch}"}

	w := Links(c, plugin.LinksArgs{Commit: "abc", File: "a/b.go", Change: "branch-review%2Fa"})
	if w.Commit != "https://cgit.example.test/r/tree/?id=abc&h=review/a" || w.File != "https://cgit.example.test/r/tree/a/b.go?id=abc&h=review/a" ||
		w.Change != "https://cgit.example.test/r/tree/?id=abc&h=review/a" {
		t.Fatalf("%+v", w)
	}

	if (Links(Config{}, plugin.LinksArgs{Commit: "abc"})) != (plugin.WebLinks{}) {
		t.Fatal("links without a template")
	}
}

func TestReportingCallsAreRefused(t *testing.T) {
	for _, call := range []string{plugin.CallStatus, plugin.CallStatuses, plugin.CallComment, plugin.CallSubject} {
		if _, err := Round(call, plugin.ExchangeRequest{Args: json.RawMessage(`{}`)}); err == nil {
			t.Errorf("%s answered", call)
		}
	}
}
