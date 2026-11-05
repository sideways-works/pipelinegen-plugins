package cli

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen-correlator-oapi-codegen/correlate"
)

const tree = "../../correlate/testdata/repo"

func run(t *testing.T, call string, req map[string]any) (int, string, string) {
	t.Helper()

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer

	code := Run([]string{call}, bytes.NewReader(data), &out, &errOut)

	return code, out.String(), errOut.String()
}

func request(t *testing.T, extra map[string]any) map[string]any {
	t.Helper()

	req := map[string]any{
		"protocol": 1, "work": t.TempDir(), "cache": t.TempDir(), "tree": tree,
		"config": map[string]any{
			"documents": []map[string]any{{
				"path": "api/notes.yaml",
				"go": map[string]any{
					"package": "gen/notesgen", "strict": true, "handlers": []string{"internal/handlers"},
					"readers":       []string{"go:internal/rules#func:Count"},
					"per_operation": []string{"go:internal/server#func:Middleware"},
				},
			}},
		},
		"narrowings": []string{},
	}

	maps.Copy(req, extra)

	return req
}

func TestDescribe(t *testing.T) {
	code, out, _ := run(t, callDescribe, nil)
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}

	var d description
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatal(err)
	}

	if d.Kind != "correlator" || len(d.Namespaces) != 0 || d.Inputs[callLinks] != "per-set" || d.Narrowings[0].Name != "generated-from-source" || d.Narrowings[0].OffAt != 5 {
		t.Errorf("describe = %+v", d)
	}
}

func TestLinks(t *testing.T) {
	code, out, stderr := run(t, callLinks, request(t, nil))
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	a := parseLinks(t, out)
	if len(a.Sets) != 1 {
		t.Fatalf("sets %+v, want one", a.Sets)
	}

	set := a.Sets[0]

	kinds := map[string]int{}
	for _, l := range set.Links {
		kinds[l.Kind]++
	}

	if kinds["serves"] != 10 || kinds["implements"] != 2 || kinds["embeds"] != 26 || kinds["reads"] != 30 {
		t.Errorf("links by kind %v", kinds)
	}

	// The digest relations travel in the set, cached with its files; the
	// answer has no other key.
	if len(set.DigestFrom) != 11 {
		t.Errorf("%d digest_from relations in the set, want 11", len(set.DigestFrom))
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &top); err != nil {
		t.Fatal(err)
	}

	if keys := slices.Sorted(maps.Keys(top)); !slices.Equal(keys, []string{"sets"}) {
		t.Errorf("answer keys %q, want only sets", keys)
	}

	// The unclassified referrer is bound wide; the classified ones and the
	// wrapper are not listed.
	if len(set.CannotTell) != 1 || set.CannotTell[0].Unit != "go:internal/server#func:Raw" {
		t.Errorf("cannot tell %+v", set.CannotTell)
	}

	// The inputs are exactly the files read, never a directory, so the
	// set's cache key changes whenever one of them does.
	want := []string{
		"api/notes.yaml", "api/tags.yaml", "gen/notesgen/generate.go", "gen/notesgen/notes.gen.go", "go.mod",
		"internal/access/access.go", "internal/access/access_test.go", "internal/handlers/notes.go",
		"internal/rules/rules.go", "internal/server/server.go", "internal/spec/spec.go",
	}
	if !slices.Equal(set.Inputs, want) {
		t.Errorf("inputs %q, want %q", set.Inputs, want)
	}
}

func TestLinksWithAClassifyOnlyConfig(t *testing.T) {
	req := request(t, map[string]any{"config": map[string]any{
		"documents": []map[string]any{{
			"path": "api/notes.yaml",
			"go": map[string]any{
				"readers":       []string{"go:internal/rules#func:Count"},
				"per_operation": []string{"go:internal/server#func:Middleware"},
			},
		}},
	}})

	code, out, stderr := run(t, callLinks, req)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	_, full, _ := run(t, callLinks, request(t, nil))

	got, want := parseLinks(t, out), parseLinks(t, full)
	if len(got.Sets) != 1 || len(want.Sets) != 1 {
		t.Fatalf("sets %d and %d, want one each", len(got.Sets), len(want.Sets))
	}

	if !slices.EqualFunc(got.Sets[0].Links, want.Sets[0].Links, func(a, b struct {
		From string `json:"from"`
		Kind string `json:"kind"`
	},
	) bool {
		return a == b
	}) {
		t.Errorf("%d links, the full config's %d", len(got.Sets[0].Links), len(want.Sets[0].Links))
	}

	if !slices.Equal(got.Sets[0].CannotTell, want.Sets[0].CannotTell) || len(got.Sets[0].DigestFrom) != len(want.Sets[0].DigestFrom) {
		t.Errorf("cannot tell %+v and %d digest_from, the full config's %+v and %d",
			got.Sets[0].CannotTell, len(got.Sets[0].DigestFrom), want.Sets[0].CannotTell, len(want.Sets[0].DigestFrom))
	}
}

type linksSet struct {
	Group  string   `json:"group"`
	Inputs []string `json:"inputs"`
	Links  []struct {
		From string `json:"from"`
		Kind string `json:"kind"`
	} `json:"links"`
	DigestFrom []struct {
		Unit string `json:"unit"`
	} `json:"digest_from"`
	CannotTell []cannotTell `json:"cannot_tell"`
}

type linksOut struct {
	Sets []linksSet `json:"sets"`
}

func parseLinks(t *testing.T, out string) linksOut {
	t.Helper()

	var a linksOut
	if err := json.Unmarshal([]byte(out), &a); err != nil {
		t.Fatal(err)
	}

	return a
}

func TestLinksReadOnlyClaimedFiles(t *testing.T) {
	claimed := []map[string]string{
		{"path": "go.mod", "role": "manifest"},
		{"path": "api/notes.yaml", "role": "contract"},
		{"path": "gen/notesgen/notes.gen.go", "role": "generated"},
		{"path": "gen/notesgen/generate.go", "role": "directive"},
		{"path": "internal/spec/spec.go", "role": "source"},
		{"path": "internal/rules/rules.go", "role": "source"},
	}

	code, out, stderr := run(t, callLinks, request(t, map[string]any{"claimed": claimed}))
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	set := parseLinks(t, out).Sets[0]

	want := []string{"api/notes.yaml", "gen/notesgen/generate.go", "gen/notesgen/notes.gen.go", "go.mod", "internal/rules/rules.go", "internal/spec/spec.go"}
	if !slices.Equal(set.Inputs, want) {
		t.Errorf("inputs %q, want the claimed files %q", set.Inputs, want)
	}

	// internal/server and internal/handlers weren't claimed, so nothing of
	// them is read.
	for _, l := range set.Links {
		if strings.HasPrefix(l.From, "go:internal/server") || strings.HasPrefix(l.From, "go:internal/handlers") {
			t.Errorf("link from %s, a file nobody claimed", l.From)
		}
	}

	if len(set.CannotTell) != 0 {
		t.Errorf("cannot tell %+v from files nobody claimed", set.CannotTell)
	}

	for name, tc := range map[string]struct {
		paths []string
		sets  int
	}{
		"a claimed file":   {[]string{"internal/spec/spec.go"}, 1},
		"a deleted file":   {[]string{"internal/old/old.go"}, 1},
		"a file of no set": {[]string{"api/tags.yaml"}, 0},
	} {
		code, out, _ := run(t, callLinks, request(t, map[string]any{"claimed": claimed, "paths": tc.paths}))
		if code != exitOK {
			t.Errorf("%s: exit %d", name, code)

			continue
		}

		// Paths that touch no input answer no set and nothing else: the
		// relations are cached with the set and need no asking again.
		if tc.sets == 0 {
			if got := strings.TrimSpace(out); got != `{"sets":[]}` {
				t.Errorf("%s: answer %s, want {\"sets\":[]}", name, got)
			}

			continue
		}

		if a := parseLinks(t, out); len(a.Sets) != tc.sets || len(a.Sets[0].DigestFrom) == 0 {
			t.Errorf("%s: %d sets, want %d with the relations", name, len(a.Sets), tc.sets)
		}
	}
}

func TestObserve(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "requests.txt"), []byte("GET /api/notes/n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	missing := filepath.Join(t.TempDir(), "missing")

	// The setup's directory comes first; without a request log a suite
	// never fills it, which is fine.
	for name, setup := range map[string]string{"missing": missing, "empty": t.TempDir()} {
		code, out, stderr := run(t, callObserve, request(t, map[string]any{"slice": "a.spec.ts", "dirs": []string{setup, dir}}))
		if code != exitOK || strings.TrimSpace(out) != `{"touched":["api:getNote"]}` {
			t.Errorf("%s setup: exit %d, %s %s", name, code, out, stderr)
		}
	}

	for name, d := range map[string]string{"missing": missing, "empty": t.TempDir()} {
		if code, _, _ := run(t, callObserve, request(t, map[string]any{"dirs": []string{dir, d}})); code != exitCannotTell {
			t.Errorf("%s part: exit %d, want 3", name, code)
		}
	}
}

func TestCollect(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "requests.log")

	if err := os.WriteFile(log, []byte("GET /api/notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	req := request(t, map[string]any{"action": "slice-end", "slice": "a.spec.ts", "dir": filepath.Join(dir, "a")})
	req["config"] = map[string]any{"request_log": log}

	if code, out, stderr := run(t, callCollect, req); code != exitOK || strings.TrimSpace(out) != "{}" {
		t.Errorf("exit %d: %s %s", code, out, stderr)
	}

	if data, err := os.ReadFile(filepath.Join(dir, "a", "requests.txt")); err != nil || string(data) != "GET /api/notes\n" {
		t.Errorf("collected %q, %v", data, err)
	}
}

func TestWhy(t *testing.T) {
	code, out, stderr := run(t, callWhy, request(t, map[string]any{
		"from": "go:gen/notesgen#var:swaggerSpec", "to": "api:api/notes.yaml#/servers",
	}))
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	var a struct {
		Path []step `json:"path"`
	}
	if err := json.Unmarshal([]byte(out), &a); err != nil {
		t.Fatal(err)
	}

	var units []string
	for _, s := range a.Path {
		units = append(units, s.Unit)
	}

	want := []string{
		"go:gen/notesgen#var:swaggerSpec", "api:api/notes.yaml#/servers",
		"go:internal/rules#func:Count", "go:internal/server#func:Middleware",
		"go:internal/server#func:Raw", "go:internal/spec#var:Notes",
	}
	if !slices.Equal(units, want) {
		t.Errorf("why %q, want %q", units, want)
	}

	if !strings.Contains(a.Path[4].Note, "bound wide") {
		t.Errorf("note %q", a.Path[4].Note)
	}

	_, out, _ = run(t, callWhy, request(t, map[string]any{"from": "go:x#func:Y", "to": "api:getNote"}))
	if strings.TrimSpace(out) != `{"path":[]}` {
		t.Errorf("unknown link: %s", out)
	}
}

func TestFailures(t *testing.T) {
	for name, c := range map[string]struct {
		call string
		req  map[string]any
	}{
		"unknown call":   {"bind", map[string]any{"protocol": 1}},
		"wrong protocol": {callLinks, map[string]any{"protocol": 2}},
		"bad action":     {callCollect, map[string]any{"protocol": 1, "action": "x", "config": map[string]any{"request_log": "x"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if code, _, _ := run(t, c.call, c.req); code != exitFailed {
				t.Errorf("exit %d, want 1", code)
			}
		})
	}

	if code := Run(nil, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); code != exitFailed {
		t.Errorf("no call: exit %d", code)
	}
}

func TestCannotTell(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "api.yaml"), []byte("openapi: 3.0.3\npaths:\n  /a:\n    $ref: other.yaml#/a\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	req := request(t, map[string]any{"tree": dir, "config": map[string]any{"documents": []map[string]any{{"path": "api.yaml"}}}})
	if code, _, _ := run(t, callLinks, req); code != exitCannotTell {
		t.Errorf("exit %d, want 3", code)
	}
}

func TestLinksFromClaimedFiles(t *testing.T) {
	req := request(t, map[string]any{"claimed": []map[string]string{
		{"path": "go.mod", "role": "manifest"},
		{"path": "gen/notesgen/notes.gen.go", "role": "generated"},
		{"path": "gen/notesgen/generate.go", "role": "directive"},
	}})
	req["config"] = map[string]any{}

	code, out, stderr := run(t, callLinks, req)
	if code != exitOK || !strings.Contains(out, `"from":"go:gen/notesgen#method:strictHandler.GetNote","to":"api:getNote"`) {
		t.Errorf("exit %d: %.300s %s", code, out, stderr)
	}

	if !strings.Contains(out, `"narrowing":"generated-from-source"`) || !strings.Contains(out, `"while":"on"`) {
		t.Errorf("relations or tagged links missing: %.300s", out)
	}
}

func TestObserveReadsOnlyContracts(t *testing.T) {
	claimed := Request{Tree: tree, Claimed: []claim{{Path: "api/notes.yaml", Role: "contract"}, {Path: "go.mod", Role: "manifest"}, {Path: "internal/spec/spec.go", Role: "source"}}}
	configured := Request{Tree: tree}
	configured.Config.Documents = []correlate.Document{{}}
	neither := Request{Tree: tree}

	for name, tc := range map[string]struct {
		req  Request
		want []string
	}{
		"claimed":    {claimed, []string{"api/notes.yaml"}},
		"configured": {configured, []string{".pipelinegen-kit.json", "api/notes.yaml", "api/tags.yaml", "gen/notesgen/codegen.yaml"}},
		"neither":    {neither, []string{".pipelinegen-kit.json", "api/notes.yaml", "api/tags.yaml", "gen/notesgen/codegen.yaml"}},
	} {
		got, err := tc.req.observed()
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q, %v; want %q", name, got, err, tc.want)
		}
	}
}

func TestObserveSeesContractsTheConfigDoesntName(t *testing.T) {
	setup, part := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(part, "requests.txt"), []byte("GET /api/tags\nGET /api/notes/n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// without claimed, and with the plugin's own claims: the same operations
	// either way
	claimed := []map[string]string{
		{"path": "api/notes.yaml", "role": "contract"}, {"path": "api/tags.yaml", "role": "contract"},
		{"path": "go.mod", "role": "manifest"}, {"path": "gen/notesgen/notes.gen.go", "role": "generated"},
	}

	for name, extra := range map[string]map[string]any{"without claimed": {}, "with claimed": {"claimed": claimed}} {
		req := request(t, map[string]any{"slice": "a.spec.ts", "dirs": []string{setup, part}})
		maps.Copy(req, extra)

		code, out, stderr := run(t, callObserve, req)
		if code != exitOK || strings.TrimSpace(out) != `{"touched":["api:getNote","api:listTags"]}` {
			t.Errorf("%s: exit %d, %s %s", name, code, out, stderr)
		}
	}
}

func TestObserveCantTellWhatItCantMap(t *testing.T) {
	part := t.TempDir()
	if err := os.WriteFile(filepath.Join(part, "requests.txt"), []byte("GET /api/notes/{id}\nGET /api/old\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	noContract := request(t, map[string]any{"tree": t.TempDir(), "config": map[string]any{}, "slice": "s", "dirs": []string{t.TempDir(), part}})
	if code, out, _ := run(t, callObserve, noContract); code != exitCannotTell {
		t.Errorf("no contract: exit %d, %s; want %d", code, out, exitCannotTell)
	}

	patterns := request(t, map[string]any{"config": map[string]any{"request_log_lines": "patterns"}, "slice": "s", "dirs": []string{t.TempDir(), part}})

	code, _, stderr := run(t, callObserve, patterns)
	if code != exitCannotTell || !strings.Contains(stderr, "GET /api/old") {
		t.Errorf("unknown pattern: exit %d, %s; want %d naming the pattern", code, stderr, exitCannotTell)
	}
}

// splitTree is a contract whose root lists its paths as $refs into one
// file per context, which refer to a file of shared parts.
const splitTree = "../../correlate/testdata/split"

// What pipelinegen sends for it: the root, claimed as the contract, and the
// files its refs name, as fragments.
var splitClaims = []map[string]string{
	{"path": "api/api.yaml", "role": "contract"},
	{"path": "api/accounts/accounts.yaml", "role": "fragment"},
	{"path": "api/notes/notes.yaml", "role": "fragment"},
	{"path": "api/shared/common.yaml", "role": "fragment"},
}

func TestObserveSplitContract(t *testing.T) {
	setup, part := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(part, "requests.txt"), []byte("GET /api/v1/notes/n1\nPOST /api/v1/notes\nGET /api/v1/accounts/me\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for name, extra := range map[string]map[string]any{"detected": {}, "claimed": {"claimed": splitClaims}} {
		req := request(t, map[string]any{"tree": splitTree, "config": map[string]any{}, "slice": "s", "dirs": []string{setup, part}})
		maps.Copy(req, extra)

		code, out, stderr := run(t, callObserve, req)
		if code != exitOK || strings.TrimSpace(out) != `{"touched":["api:createNote","api:getMe","api:getNote"]}` {
			t.Errorf("%s: exit %d, %s %s", name, code, out, stderr)
		}
	}
}

func TestLinksSplitContractInputs(t *testing.T) {
	req := request(t, map[string]any{"tree": splitTree, "config": map[string]any{}, "claimed": splitClaims})

	code, out, stderr := run(t, callLinks, req)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	var a linksAnswer
	if err := json.Unmarshal([]byte(out), &a); err != nil {
		t.Fatal(err)
	}

	want := []string{"api/accounts/accounts.yaml", "api/api.yaml", "api/notes/notes.yaml", "api/shared/common.yaml"}
	if len(a.Sets) != 1 || !slices.Equal(a.Sets[0].Inputs, want) {
		t.Fatalf("sets %+v, want one with inputs %q", a.Sets, want)
	}

	req["paths"] = []string{"api/notes/notes.yaml"}

	if _, out, _ := run(t, callLinks, req); !strings.Contains(out, `"group":"oapi-codegen"`) {
		t.Errorf("asking about a context's file answers no set: %s", out)
	}
}

func TestCannotTellSplitContract(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "api.yaml"), []byte("openapi: 3.0.3\npaths:\n  /a:\n    $ref: parts/a.yaml#/paths/~1a\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	req := request(t, map[string]any{"tree": dir, "config": map[string]any{}, "slice": "s", "dirs": []string{t.TempDir(), t.TempDir()}})
	if code, _, stderr := run(t, callObserve, req); code != exitCannotTell || !strings.Contains(stderr, "parts/a.yaml is not in the tree") {
		t.Errorf("exit %d, want 3 naming the missing file: %s", code, stderr)
	}
}
