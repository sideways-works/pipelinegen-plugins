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

	"github.com/sideways-works/pipelinegen-sbom-openapi/openapi"
)

const fixture = "../../openapi/testdata/notes"

func run(t *testing.T, call, request string) (int, string, string) {
	t.Helper()

	var out, errOut bytes.Buffer

	code := Run([]string{call}, strings.NewReader(request), &out, &errOut)

	return code, out.String(), errOut.String()
}

func request(t *testing.T, tree string, extra map[string]any) string {
	t.Helper()

	req := map[string]any{
		"protocol": 1, "work": t.TempDir(), "cache": t.TempDir(), "tree": tree,
		"config": map[string]any{"documents": []map[string]string{
			{"path": "api.yaml"}, {"path": "common.yaml"}, {"path": "hooks.yaml"},
		}},
		"narrowings": []string{"doc-text"},
	}

	maps.Copy(req, extra)

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func TestDescribe(t *testing.T) {
	code, out, _ := run(t, "describe", "")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}

	var d description
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatal(err)
	}

	if d.Kind != "sbom" || d.Inputs["units"] != "per-set" || d.Inputs["document"] != "per-set" || d.Namespaces[0] != "api" || d.Narrowings[1].Name != "document-units" || d.Narrowings[1].OffAt != 2 {
		t.Errorf("describe = %+v", d)
	}
}

type flat struct {
	Units []openapi.Unit
	Links []openapi.Link
	Sets  []unitSet
}

func parse(t *testing.T, out string) flat {
	t.Helper()

	var a unitsAnswer
	if err := json.Unmarshal([]byte(out), &a); err != nil {
		t.Fatal(err)
	}

	f := flat{Sets: a.Sets}
	for _, s := range a.Sets {
		f.Units = append(f.Units, s.Units...)
		f.Links = append(f.Links, s.Links...)
	}

	return f
}

func TestUnits(t *testing.T) {
	code, out, stderr := run(t, "units", request(t, fixture, nil))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	a := parse(t, out)

	files := map[string]int{}
	for _, u := range a.Units {
		files[u.File]++
	}

	if files["api.yaml"] == 0 || files["common.yaml"] == 0 || files["hooks.yaml"] == 0 || len(a.Links) == 0 {
		t.Errorf("units per file %v, %d links", files, len(a.Links))
	}
}

func TestUnitsForSomePaths(t *testing.T) {
	code, out, stderr := run(t, "units", request(t, fixture, map[string]any{"paths": []string{"common.yaml"}}))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	// The contracts check their refs into common.yaml, so their set is answered
	// too.
	a := parse(t, out)
	groups := map[string]int{}

	for _, s := range a.Sets {
		groups[s.Group] = len(s.Units)

		for _, u := range s.Units {
			if s.Group == "common.yaml" && u.File != "common.yaml" {
				t.Errorf("unit %s of another file in common.yaml's set", u.ID)
			}
		}
	}

	if len(groups) != 2 || groups["common.yaml"] != 6 || groups["contracts"] == 0 {
		t.Errorf("sets %v, want common.yaml's (Tag, Color, the file and their documentation) and the contracts'", groups)
	}
}

func TestUnitsComeInSets(t *testing.T) {
	code, out, stderr := run(t, "units", request(t, fixture, nil))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	got := map[string][]string{}
	for _, s := range parse(t, out).Sets {
		got[s.Group] = s.Inputs
	}

	want := map[string][]string{"contracts": {"api.yaml", "common.yaml", "hooks.yaml"}, "common.yaml": {"common.yaml"}}
	if !maps.EqualFunc(got, want, slices.Equal) {
		t.Errorf("sets %q, want %q", got, want)
	}

	for name, paths := range map[string][]string{
		"one contract":       {"hooks.yaml"},
		"a deleted contract": {"gone.yaml"},
	} {
		_, out, _ := run(t, "units", request(t, fixture, map[string]any{"paths": paths}))

		sets := parse(t, out).Sets
		if len(sets) != 1 || sets[0].Group != "contracts" || !slices.Contains(unitIDs(sets[0].Units), "api:getNote") {
			t.Errorf("%s: sets %+v, want the contracts' whole set", name, sets)
		}
	}
}

func unitIDs(us []openapi.Unit) []string {
	out := make([]string, 0, len(us))
	for _, u := range us {
		out = append(out, u.ID)
	}

	return out
}

func digestOf(t *testing.T, out, id string) string {
	t.Helper()

	a := parse(t, out)

	for _, u := range a.Units {
		if u.ID == id {
			return u.Digest
		}
	}

	t.Fatalf("no unit %s", id)

	return ""
}

func TestAnswerIgnoresTheLevel(t *testing.T) {
	_, on, _ := run(t, "units", request(t, fixture, map[string]any{"narrowings": []string{"doc-text"}}))
	_, off, _ := run(t, "units", request(t, fixture, map[string]any{"narrowings": []string{}}))

	if on != off {
		t.Error("the units answer differs by narrowing level")
	}

	keep := request(t, fixture, nil)
	keep = strings.Replace(keep, `"config":{`, `"config":{"keep_docs":true,`, 1)

	_, kept, _ := run(t, "units", keep)
	if digestOf(t, kept, "api:listNotes") == digestOf(t, on, "api:listNotes") || strings.Contains(kept, `"kind":"docs"`) {
		t.Error("keep_docs must keep documentation in the digests, with no documentation units")
	}
}

func TestCannotTellExitsThree(t *testing.T) {
	dir := t.TempDir()
	doc := "openapi: 3.0.3\npaths:\n  /a:\n    $ref: other.yaml#/a\n"

	if err := os.WriteFile(filepath.Join(dir, "api.yaml"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	req := request(t, dir, map[string]any{"config": map[string]any{"documents": []map[string]string{{"path": "api.yaml"}}}})
	if code, _, stderr := run(t, "units", req); code != exitCannotTell {
		t.Errorf("exit %d, want 3: %s", code, stderr)
	}
}

func TestFailures(t *testing.T) {
	for name, c := range map[string]struct{ call, req string }{
		"unknown call":   {"bind", `{"protocol":1}`},
		"wrong protocol": {"units", `{"protocol":2}`},
		"bad json":       {"units", `{`},
		"missing file":   {"units", `{"protocol":1,"tree":".","config":{"documents":[{"path":"nope.yaml"}]}}`},
	} {
		t.Run(name, func(t *testing.T) {
			if code, _, _ := run(t, c.call, c.req); code != exitFailed {
				t.Errorf("exit %d, want 1", code)
			}
		})
	}
}

func TestDocument(t *testing.T) {
	code, out, stderr := run(t, "document", request(t, fixture, nil))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	var b bom
	if err := json.Unmarshal([]byte(out), &b); err != nil {
		t.Fatal(err)
	}

	if b.BOMFormat != "CycloneDX" || b.SpecVersion != "1.6" {
		t.Fatalf("bom = %+v", b)
	}

	var api *service

	for i := range b.Services {
		if b.Services[i].Name == "api.yaml" {
			api = &b.Services[i]
		}
	}

	if api == nil {
		t.Fatal("no service for api.yaml")
	}

	var getNote *service

	for i := range api.Services {
		if api.Services[i].BOMRef == "api:getNote" {
			getNote = &api.Services[i]
		}
	}

	if getNote == nil || getNote.Endpoints[0] != "https://notes.example/api/notes/{id}" {
		t.Errorf("getNote = %+v", getNote)
	}
}

func TestDetectCall(t *testing.T) {
	req := request(t, fixture, map[string]any{"paths": []string{"api.yaml", "common.yaml", "hooks.yaml"}})

	code, out, stderr := run(t, callDetect, req)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	if want := `{"claims":[{"path":"api.yaml","role":"contract","depends":["common.yaml"],"by_units":"document-units"},{"path":"hooks.yaml","role":"contract","depends":["api.yaml"],"by_units":"document-units"}]}`; strings.TrimSpace(out) != want {
		t.Errorf("detect = %s", out)
	}
}

func TestUnitsWithoutConfig(t *testing.T) {
	req := request(t, fixture, map[string]any{"config": map[string]any{}})

	code, out, stderr := run(t, callUnits, req)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	for _, id := range []string{`"api:getNote"`, `"api:common.yaml#/Tag"`, `"api:noteCreated"`} {
		if !strings.Contains(out, id) {
			t.Errorf("no %s in %.300s", id, out)
		}
	}
}

const splitFixture = "../../openapi/testdata/split"

func TestSplitContractSetCoversItsFiles(t *testing.T) {
	// What pipelinegen sends: the contract and its fragments as claimed files.
	claimed := []map[string]string{
		{"path": "api.yaml", "role": "contract"},
		{"path": "accounts/accounts.yaml", "role": "fragment"},
		{"path": "notes/notes.yaml", "role": "fragment"},
		{"path": "shared/common.yaml", "role": "fragment"},
	}
	req := request(t, splitFixture, map[string]any{"config": map[string]any{}, "claimed": claimed})

	code, out, stderr := run(t, "units", req)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	got := map[string][]string{}
	for _, s := range parse(t, out).Sets {
		got[s.Group] = s.Inputs
	}

	want := map[string][]string{
		"contracts":              {"accounts/accounts.yaml", "api.yaml", "notes/notes.yaml", "shared/common.yaml"},
		"accounts/accounts.yaml": {"accounts/accounts.yaml"},
		"notes/notes.yaml":       {"notes/notes.yaml"},
		"shared/common.yaml":     {"shared/common.yaml"},
	}
	if !maps.EqualFunc(got, want, slices.Equal) {
		t.Errorf("sets %q, want %q", got, want)
	}

	for _, p := range []string{"notes/notes.yaml", "shared/common.yaml"} {
		_, out, _ := run(t, "units", request(t, splitFixture, map[string]any{"config": map[string]any{}, "claimed": claimed, "paths": []string{p}}))

		var groups []string
		for _, s := range parse(t, out).Sets {
			groups = append(groups, s.Group)
		}

		if !slices.Contains(groups, "contracts") || !slices.Contains(groups, p) {
			t.Errorf("asking about %s answers %q, want the contracts' set and its own", p, groups)
		}
	}

	if !slices.Contains(unitIDs(parse(t, out).Units), "api:getNote") {
		t.Error("no api:getNote")
	}
}

func TestSplitContractCannotTellExitsThree(t *testing.T) {
	dir := t.TempDir()
	doc := "openapi: 3.0.3\npaths:\n  /a:\n    $ref: part.yaml#/paths/~1a\n"
	part := "paths:\n  /a:\n    $ref: api.yaml#/paths/~1a\n"

	for name, data := range map[string]string{"api.yaml": doc, "part.yaml": part} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	code, _, stderr := run(t, "units", request(t, dir, map[string]any{"config": map[string]any{}}))
	if code != exitCannotTell || !strings.Contains(stderr, "cycle") {
		t.Errorf("exit %d, want 3 naming the cycle: %s", code, stderr)
	}
}

func TestDocumentOfASplitContract(t *testing.T) {
	req := request(t, splitFixture, map[string]any{
		"config": map[string]any{}, "paths": []string{"api.yaml"},
		"claimed": []map[string]string{{"path": "api.yaml", "role": "contract"}, {"path": "notes/notes.yaml", "role": "fragment"}},
	})

	code, out, stderr := run(t, "document", req)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	var b bom
	if err := json.Unmarshal([]byte(out), &b); err != nil {
		t.Fatal(err)
	}

	if len(b.Services) != 1 || b.Services[0].Name != "api.yaml" || len(b.Services[0].Services) != 6 {
		t.Errorf("services %+v, want api.yaml alone with its six operations", b.Services)
	}
}
