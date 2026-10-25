package correlate

import (
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen-sbom-go/goid"
)

const repo = "testdata/repo"

func notesDoc() Document {
	return Document{
		Path: "api/notes.yaml",
		Go:   &GoConfig{Package: "gen/notesgen", Strict: true, Handlers: []string{"internal/handlers"}},
	}
}

func load(t *testing.T, docs ...Document) *Model {
	t.Helper()

	if len(docs) == 0 {
		docs = []Document{notesDoc()}
	}

	m, err := Load(repo, Config{Documents: docs}, nil)
	if err != nil {
		t.Fatal(err)
	}

	return m
}

func has(links []Link, from, to, kind string) bool {
	return slices.ContainsFunc(links, func(l Link) bool { return l.From == from && l.To == to && l.Kind == kind })
}

func TestCodeLinks(t *testing.T) {
	m := load(t)
	links := m.CodeLinks()

	for _, op := range []string{"ListNotes", "CreateNote", "GetNote", "ListDrafts", "GetNoteText"} {
		id := map[string]string{
			"ListNotes": "api:listNotes", "CreateNote": "api:createNote", "GetNote": "api:getNote",
			"ListDrafts": "api:list-drafts", "GetNoteText": "api:getNoteText",
		}[op]

		if !has(links, "go:gen/notesgen#method:ServerInterfaceWrapper."+op, id, LinkServes) {
			t.Errorf("no wrapper link for %s", op)
		}

		if !has(links, "go:gen/notesgen#method:strictHandler."+op, id, LinkServes) {
			t.Errorf("no strict handler link for %s", op)
		}
	}

	t.Run("narrowed, the embedded document binds the top-level parts", func(t *testing.T) {
		var to []string

		for _, l := range links {
			if l.From == "go:gen/notesgen#var:swaggerSpec" && l.While == "on" {
				to = append(to, l.To)

				if l.Narrowing != NarrowDocument {
					t.Errorf("link %v carries no narrowing", l)
				}
			}
		}

		if want := []string{"api:api/notes.yaml#/servers", "api:api/notes.yaml#/openapi", "api:api/notes.yaml#/paths"}; !slices.Equal(to, want) {
			t.Errorf("swaggerSpec links to %q, want %q", to, want)
		}
	})

	t.Run("wide, it binds every unit of the document", func(t *testing.T) {
		for _, id := range m.DocumentUnits("api/notes.yaml") {
			if !slices.Contains(links, Link{
				From: "go:gen/notesgen#var:rawSpec", To: id, Kind: LinkEmbeds, Narrowing: NarrowDocument, While: "off",
				Note: "the embedded document holds all of it",
			}) {
				t.Errorf("rawSpec doesn't bind %s while document-units is off", id)
			}
		}
	})
}

func TestGeneratedNamesExist(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(repo, "gen/notesgen/notes.gen.go"), nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	decls := map[string]bool{}

	// go-sbom's own naming, so the links name what go-sbom lists.
	for _, d := range goid.File("gen/notesgen", "notes.gen.go", f) {
		decls[string(d.ID)] = true
	}

	m := load(t)
	for _, l := range m.CodeLinks() {
		if strings.HasPrefix(l.From, "go:gen/notesgen#") && !decls[l.From] {
			t.Errorf("link from %s: no such declaration", l.From)
		}
	}
}

func TestHandlerLinks(t *testing.T) {
	links, err := load(t).HandlerLinks(repo)
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, l := range links {
		got = append(got, l.From+" "+l.To)
	}

	want := []string{
		"go:internal/handlers#method:Notes.GetNote api:getNote",
		"go:internal/handlers#method:Notes.ListNotes api:listNotes",
	}
	if !slices.Equal(got, want) {
		t.Errorf("handler links %q, want %q", got, want)
	}
}

func TestReferrers(t *testing.T) {
	doc := notesDoc()
	doc.Go.Readers = []string{"go:internal/rules#func:Count"}
	doc.Go.PerOperation = []string{"go:internal/server#func:Middleware"}

	m := load(t, doc)

	refs, err := m.Referrers(repo)
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, r := range refs {
		got = append(got, r.ID+" "+r.Through+" "+r.As)
	}

	want := []string{
		"go:internal/rules#func:Count spec.Notes reader",
		"go:internal/server#func:Middleware spec.Notes per-operation",
		"go:internal/server#func:Raw notesgen.GetSpecJSON ",
		"go:internal/spec#var:Notes notesgen.GetSpec wrapper",
	}
	if !slices.Equal(got, want) {
		t.Errorf("referrers\n got %q\nwant %q", got, want)
	}

	links := m.CodeLinks()
	for _, id := range m.DocumentUnits("api/notes.yaml") {
		if !has(links, "go:internal/rules#func:Count", id, LinkReads) {
			t.Errorf("the reader doesn't bind %s", id)
		}
	}
}

func TestReferrersOnlyInScannedDirs(t *testing.T) {
	m, err := Load(repo, Config{Documents: []Document{notesDoc()}, Scan: []string{"internal/rules"}}, nil)
	if err != nil {
		t.Fatal(err)
	}

	refs, err := m.Referrers(repo)
	if err != nil {
		t.Fatal(err)
	}

	// The wrapper lives outside the scanned directory, so its users can't
	// be told from the code that was read: none is found.
	if len(refs) != 0 {
		t.Errorf("referrers %v", refs)
	}
}

func TestRouter(t *testing.T) {
	r := load(t).NewRouter(nil)

	for _, c := range []struct{ method, url, want string }{
		{"GET", "/api/notes", "api:listNotes"},
		{"post", "/api/notes", "api:createNote"},
		{"GET", "/api/notes/n1", "api:getNote"},
		{"GET", "/api/notes/drafts", "api:list-drafts"},
		{"GET", "/api/notes/n1.txt", "api:getNoteText"},
		{"GET", "https://notes.example/api/notes/n1?full=1", "api:getNote"},
		{"DELETE", "/api/notes/n1", ""},
		{"GET", "/notes/n1", ""},
		{"GET", "/apinotes", ""},
		{"GET", "/static/app.js", ""},
	} {
		got, _ := r.Match(c.method, c.url)
		if got != c.want {
			t.Errorf("%s %s = %q, want %q", c.method, c.url, got, c.want)
		}
	}

	doc := notesDoc()
	doc.Base = "/v2"

	m := load(t, doc)
	if got, _ := m.NewRouter(m.bases()).Match("GET", "/v2/notes"); got != "api:listNotes" {
		t.Errorf("with a base: %q", got)
	}
}

func write(t *testing.T, p, s string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(p, []byte(s), 0o600); err != nil { //nolint:gosec // a path inside the test's own temporary directory
		t.Fatal(err)
	}
}

func TestObserve(t *testing.T) {
	m := load(t)
	setup, slice := t.TempDir(), t.TempDir()

	write(t, filepath.Join(setup, RequestsFile), "GET /api/notes\nGET /static/app.js\n")
	write(t, filepath.Join(slice, RequestsFile), "POST /api/notes 201\n\ngarbage\n")
	write(t, filepath.Join(slice, "network.har"), `{"log":{"entries":[{"request":{"method":"GET","url":"https://notes.example/api/notes/n1"}}]}}`)

	got, err := m.Observe([]string{setup, slice})
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"api:createNote", "api:getNote", "api:listNotes"}; !slices.Equal(got, want) {
		t.Errorf("touched %q, want %q", got, want)
	}

	t.Run("an empty log is an answer", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, RequestsFile), "")

		got, err := m.Observe([]string{t.TempDir(), dir})
		if err != nil || len(got) != 0 {
			t.Errorf("got %q, %v", got, err)
		}
	})

	t.Run("a broken HAR fails", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "x.har"), "{")

		if _, err := m.Observe([]string{t.TempDir(), dir}); err == nil || errors.Is(err, ErrNoData) {
			t.Errorf("err = %v, want a failure", err)
		}
	})

	t.Run("a broken HAR in the setup fails", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "x.har"), "{")

		if _, err := m.Observe([]string{dir, slice}); err == nil || errors.Is(err, ErrNoData) {
			t.Errorf("err = %v, want a failure", err)
		}
	})
}

func TestObserveSetup(t *testing.T) {
	m := load(t)

	part := t.TempDir()
	write(t, filepath.Join(part, "page.har"), `{"log":{"entries":[{"request":{"method":"GET","url":"https://notes.example/api/notes/n1"}}]}}`)

	withLog := t.TempDir()
	write(t, filepath.Join(withLog, RequestsFile), "GET /api/notes\n")

	for _, tc := range []struct {
		name  string
		setup string
		want  []string
	}{
		{"no setup directory", filepath.Join(t.TempDir(), "missing"), []string{"api:getNote"}},
		{"an empty setup directory", t.TempDir(), []string{"api:getNote"}},
		{"a setup with a request log", withLog, []string{"api:getNote", "api:listNotes"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := m.Observe([]string{tc.setup, part})
			if err != nil {
				t.Fatal(err)
			}

			if !slices.Equal(got, tc.want) {
				t.Errorf("touched %q, want %q", got, tc.want)
			}
		})
	}
}

func TestObservePartsWithoutEvidence(t *testing.T) {
	m := load(t)

	withLog := t.TempDir()
	write(t, filepath.Join(withLog, RequestsFile), "GET /api/notes\n")

	recorded := t.TempDir()
	write(t, filepath.Join(recorded, RequestsFile), "GET /api/notes/n1\n")

	for _, tc := range []struct {
		name string
		dirs []string
	}{
		{"an empty part", []string{withLog, t.TempDir()}},
		{"a missing part", []string{withLog, filepath.Join(t.TempDir(), "missing")}},
		{"an empty part beside a recorded one", []string{withLog, recorded, t.TempDir()}},
		{"an empty part and no setup", []string{filepath.Join(t.TempDir(), "missing"), t.TempDir()}},
		{"no part at all", []string{withLog}},
		{"no directory at all", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := m.Observe(tc.dirs)
			if !errors.Is(err, ErrNoData) {
				t.Errorf("got %q, %v; want can't tell", got, err)
			}
		})
	}
}

func TestCollect(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "requests.log")
	setup, slice := filepath.Join(dir, "_setup"), filepath.Join(dir, "a.spec")

	write(t, log, "GET /api/notes\n")

	if err := Collect(log, ActionSetupEnd, setup); err != nil {
		t.Fatal(err)
	}

	write(t, log, "GET /between/slices\n")

	if err := Collect(log, ActionSliceStart, slice); err != nil {
		t.Fatal(err)
	}

	f, err := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.WriteString("POST /api/notes\n"); err != nil {
		t.Fatal(err)
	}

	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if err := Collect(log, ActionSliceEnd, slice); err != nil {
		t.Fatal(err)
	}

	for p, want := range map[string]string{
		filepath.Join(setup, RequestsFile): "GET /api/notes\n",
		filepath.Join(slice, RequestsFile): "POST /api/notes\n",
		log:                                "",
	} {
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}

		if string(got) != want {
			t.Errorf("%s = %q, want %q", p, got, want)
		}
	}

	if err := Collect("", ActionSliceEnd, slice); err != nil {
		t.Errorf("without a log, nothing to do: %v", err)
	}

	if err := Collect(filepath.Join(dir, "missing.log"), ActionSliceEnd, slice); err == nil {
		t.Error("no error for a missing log")
	}

	if err := Collect(log, "pause", slice); err == nil {
		t.Error("no error for an unknown action")
	}
}

func TestObserveNeedsAContract(t *testing.T) {
	part := t.TempDir()
	write(t, filepath.Join(part, RequestsFile), "GET /api/notes/n1\nGET /favicon.ico\n")

	empty, err := Load(t.TempDir(), Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if got, err := empty.Observe([]string{t.TempDir(), part}); !errors.Is(err, ErrNoContract) {
		t.Errorf("no contract: got %q, %v; want can't tell", got, err)
	}

	pages := t.TempDir()
	write(t, filepath.Join(pages, RequestsFile), "GET /favicon.ico\nGET /api/nowhere\n")

	got, err := load(t).Observe([]string{t.TempDir(), pages})
	if err != nil || len(got) != 0 {
		t.Errorf("requests matching no operation: got %q, %v; want an empty list", got, err)
	}
}
