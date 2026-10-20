package openapi

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var notesDocs = []Document{{Path: "api.yaml"}, {Path: "common.yaml"}, {Path: "hooks.yaml"}}

func tree(t *testing.T, edits ...[3]string) string {
	t.Helper()

	dir := t.TempDir()

	for _, f := range []string{"api.yaml", "common.yaml", "hooks.yaml"} {
		data, err := os.ReadFile(filepath.Join("testdata", "notes", f))
		if err != nil {
			t.Fatal(err)
		}

		text := string(data)

		for _, e := range edits {
			if e[0] != f {
				continue
			}

			if !strings.Contains(text, e[1]) {
				t.Fatalf("edit: %q is not in %s", e[1], f)
			}

			text = strings.Replace(text, e[1], e[2], 1)
		}

		if err := os.WriteFile(filepath.Join(dir, f), []byte(text), 0o600); err != nil { //nolint:gosec // f is one of the fixture's own names
			t.Fatal(err)
		}
	}

	return dir
}

func build(t *testing.T, dir string, opts Options) *Model {
	t.Helper()

	m, err := Build(dir, notesDocs, opts)
	if err != nil {
		t.Fatal(err)
	}

	return m
}

func digests(m *Model) map[string]string {
	out := map[string]string{}
	for _, u := range m.Units {
		out[u.ID] = u.Digest
	}

	return out
}

func changed(t *testing.T, opts Options, edits ...[3]string) []string {
	t.Helper()

	before := digests(build(t, tree(t), opts))
	after := digests(build(t, tree(t, edits...), opts))

	var out []string

	for id, d := range after {
		if before[id] != d && !strings.HasPrefix(id, "api:docs:") {
			out = append(out, id)
		}
	}

	for id := range before {
		if _, ok := after[id]; !ok && !strings.HasPrefix(id, "api:docs:") {
			out = append(out, id)
		}
	}

	slices.Sort(out)

	return out
}

func operations(ids []string) []string {
	var out []string

	for _, id := range ids {
		if !strings.Contains(id, "#") && !strings.HasSuffix(id, ".yaml") && !strings.HasPrefix(id, "api:docs:") {
			out = append(out, id)
		}
	}

	return out
}

func TestBuildFindsUnits(t *testing.T) {
	m := build(t, tree(t), Options{})

	var ops []string
	for _, op := range m.Operations {
		ops = append(ops, op.ID+" "+op.Method+" "+op.Path+" "+op.GoName)
	}

	want := []string{
		"api:listNotes GET /notes ListNotes",
		"api:createNote POST /notes CreateNote",
		"api:getNote GET /notes/{id} GetNote",
		"api:delete-note DELETE /notes/{id} DeleteNote",
		"api:listComments GET /notes/{id}/comments ListComments",
		"api:listShapes GET /shapes ListShapes",
		"api:listTags GET /tags ListTags",
		"api:noteCreated POST webhook:noteCreated NoteCreated",
	}
	if !slices.Equal(ops, want) {
		t.Errorf("operations:\n got %q\nwant %q", ops, want)
	}

	ids := map[string]string{}
	for _, u := range m.Units {
		ids[u.ID] = u.Kind
	}

	for id, kind := range map[string]string{
		"api:api.yaml":                                     KindDocument,
		"api:api.yaml#/servers":                            KindTop,
		"api:api.yaml#/openapi":                            KindTop,
		"api:api.yaml#/components/schemas/Note":            "schema",
		"api:api.yaml#/components/schemas/Unused":          "schema",
		"api:api.yaml#/components/parameters/Limit":        "parameter",
		"api:api.yaml#/components/securitySchemes/session": "securityScheme",
		"api:common.yaml#/Tag":                             KindFragment,
		"api:common.yaml":                                  KindDocument,
		"api:listNotes":                                    KindOperation,
	} {
		if ids[id] != kind {
			t.Errorf("unit %s: kind %q, want %q", id, ids[id], kind)
		}
	}

	if got := m.Servers["api.yaml"]; !slices.Equal(got, []string{"https://notes.example/api"}) {
		t.Errorf("servers: %q", got)
	}
}

func TestDocumentationChangesNothing(t *testing.T) {
	got := changed(t, Options{},
		[3]string{"api.yaml", "summary: Lists notes.", "summary: Lists every note."},
		[3]string{"api.yaml", "description: Creates a note.", "description: Makes a note."},
		[3]string{"api.yaml", "description: The note's id.", "description: Which note."},
		[3]string{"api.yaml", "example: [{ id: n1, text: hi }]", "example: [{ id: n2, text: ho }]"},
		[3]string{"api.yaml", "id: { type: string, example: n1 }", "id: { type: string, example: n9 }"},
		[3]string{"api.yaml", "text: { type: string, title: Text }", "text: { type: string, title: Body }"},
		[3]string{"api.yaml", "description: The session cookie.", "description: The cookie."},
		[3]string{"api.yaml", "description: The only server.", "description: Production."},
		[3]string{"api.yaml", "description: A small notes service.", "description: Notes."},
		[3]string{"api.yaml", "\"204\": { description: Gone. }", "\"204\": { description: Deleted. }"},
		[3]string{"common.yaml", "description: A label on a note.", "description: A label."},
	)
	if len(got) != 0 {
		t.Errorf("a documentation edit changed %q", got)
	}
}

func TestDocumentationUnits(t *testing.T) {
	before := digests(build(t, tree(t), Options{}))
	after := digests(build(t, tree(t, [3]string{"api.yaml", "summary: Lists notes.", "summary: Lists every note."}), Options{}))

	var got []string

	for id, d := range after {
		if before[id] != d {
			got = append(got, id)
		}
	}

	slices.Sort(got)

	if want := []string{"api:docs:api.yaml", "api:docs:listNotes"}; !slices.Equal(got, want) {
		t.Errorf("changed %q, want %q", got, want)
	}

	m := build(t, tree(t), Options{})
	if !slices.Contains(m.Links, Link{From: "api:listNotes", To: "api:docs:listNotes", Kind: LinkUses, Narrowing: NarrowDocText, While: "off"}) {
		t.Error("no link to the documentation unit")
	}

	if !slices.Contains(m.Links, Link{From: "file:api.yaml", To: "api:listNotes", Kind: LinkChanges}) {
		t.Error("no changes link from the file")
	}

	for _, u := range build(t, tree(t), Options{KeepDocs: true}).Units {
		if u.Kind == KindDocs {
			t.Errorf("with documentation kept, a documentation unit %s", u.ID)
		}
	}
}

func TestKeepDocsCountsDocumentation(t *testing.T) {
	got := operations(changed(t, Options{KeepDocs: true},
		[3]string{"api.yaml", "summary: Lists notes.", "summary: Lists every note."}))
	if !slices.Equal(got, []string{"api:listNotes"}) {
		t.Errorf("changed %q", got)
	}
}

func TestSharedSchemaChangesItsUsers(t *testing.T) {
	got := operations(changed(t, Options{},
		[3]string{"api.yaml", "text: { type: string, title: Text }", "text: { type: string, title: Text, maxLength: 9 }"}))
	if want := []string{"api:createNote", "api:getNote", "api:listNotes"}; !slices.Equal(got, want) {
		t.Errorf("changed %q, want %q", got, want)
	}
}

func TestPropertyNamedLikeAKeywordIsData(t *testing.T) {
	got := operations(changed(t, Options{},
		[3]string{"api.yaml", "description: { type: string }", "description: { type: integer }"}))
	if want := []string{"api:createNote", "api:getNote", "api:listNotes"}; !slices.Equal(got, want) {
		t.Errorf("changed %q, want %q", got, want)
	}
}

func TestExtensionsCount(t *testing.T) {
	got := operations(changed(t, Options{},
		[3]string{"api.yaml", "operationId: getNote\n      x-access: user", "operationId: getNote\n      x-access: public"}))
	if !slices.Equal(got, []string{"api:getNote"}) {
		t.Errorf("changed %q", got)
	}
}

func TestPathParametersReachTheirOperations(t *testing.T) {
	got := operations(changed(t, Options{},
		[3]string{"api.yaml", "description: The note's id.\n        schema: { type: string }", "description: The note's id.\n        schema: { type: string, format: uuid }"}))
	if want := []string{"api:delete-note", "api:getNote"}; !slices.Equal(got, want) {
		t.Errorf("changed %q, want %q", got, want)
	}
}

func TestSharedParameterReachesItsUser(t *testing.T) {
	got := operations(changed(t, Options{},
		[3]string{"api.yaml", "maximum: 100", "maximum: 50"}))
	if !slices.Equal(got, []string{"api:listNotes"}) {
		t.Errorf("changed %q", got)
	}
}

func TestOverlappingTemplateChangesItsNeighbours(t *testing.T) {
	// /notes/drafts can take a URL of /notes/{id}, so only /notes/{id}'s digests
	// change; the routing table still selects every operation.
	add := [3]string{"api.yaml", "  /tags:\n", "  /notes/drafts:\n    get:\n      operationId: listDrafts\n      responses:\n        \"200\": { description: Drafts. }\n  /tags:\n"}

	got := operations(changed(t, Options{}, add))
	if want := []string{"api:delete-note", "api:getNote", "api:listDrafts"}; !slices.Equal(got, want) {
		t.Errorf("changed %q, want %q", got, want)
	}

	want := append(slices.Clone(everyOperation), "api:listDrafts")
	slices.Sort(want)

	if got := reaching(t, nil, add); !slices.Equal(got, want) {
		t.Errorf("selects %q, want %q", got, want)
	}
}

func TestSecurity(t *testing.T) {
	t.Run("the document's requirement reaches operations without their own", func(t *testing.T) {
		got := operations(changed(t, Options{},
			[3]string{"api.yaml", "security:\n  - session: []\npaths:", "security: []\npaths:"}))
		if want := []string{"api:createNote", "api:delete-note", "api:getNote", "api:listNotes"}; !slices.Equal(got, want) {
			t.Errorf("changed %q, want %q", got, want)
		}
	})

	t.Run("a scheme reaches the operations it guards", func(t *testing.T) {
		got := operations(changed(t, Options{},
			[3]string{"api.yaml", "name: sid", "name: session_id"}))
		if want := []string{"api:createNote", "api:delete-note", "api:getNote", "api:listNotes"}; !slices.Equal(got, want) {
			t.Errorf("changed %q, want %q", got, want)
		}
	})
}

func TestRecursiveSchema(t *testing.T) {
	got := operations(changed(t, Options{},
		[3]string{"api.yaml", "    Comment:\n      type: object\n      properties:\n        text: { type: string }", "    Comment:\n      type: object\n      properties:\n        text: { type: string, minLength: 1 }"}))
	if !slices.Equal(got, []string{"api:listComments"}) {
		t.Errorf("changed %q", got)
	}
}

func TestDiscriminatorMapping(t *testing.T) {
	// Circle is reached through oneOf and through the mapping's bare name.
	got := operations(changed(t, Options{},
		[3]string{"api.yaml", "radius: { type: number }", "radius: { type: integer }"}))
	if !slices.Equal(got, []string{"api:listShapes"}) {
		t.Errorf("changed %q", got)
	}

	m := build(t, tree(t), Options{})
	if !slices.ContainsFunc(m.Links, func(l Link) bool {
		return l.From == "api:api.yaml#/components/schemas/Shape" && l.To == "api:api.yaml#/components/schemas/Circle"
	}) {
		t.Error("no link from Shape to Circle")
	}
}

func TestOtherFilesAreLinkedNotRead(t *testing.T) {
	// A change in common.yaml changes its own units; links carry it to the users.
	got := changed(t, Options{}, [3]string{"common.yaml", "minLength: 1", "minLength: 2"})
	if want := []string{"api:common.yaml", "api:common.yaml#/Tag"}; !slices.Equal(got, want) {
		t.Errorf("changed %q, want %q", got, want)
	}

	m := build(t, tree(t), Options{})

	for _, want := range []Link{
		{From: "api:listTags", To: "api:common.yaml#/Tag", Kind: "uses"},
		{From: "api:api.yaml#/components/schemas/Note", To: "api:common.yaml#/Tag", Kind: "uses"},
		{From: "api:noteCreated", To: "api:api.yaml#/components/schemas/Note", Kind: "uses"},
		{From: "api:api.yaml", To: "api:common.yaml#/Tag", Kind: "uses"},
		{From: "api:listTags", To: "api:api.yaml#/servers", Kind: "uses"},
	} {
		if !slices.Contains(m.Links, want) {
			t.Errorf("no link %+v", want)
		}
	}
}

func TestWholeDocumentUnit(t *testing.T) {
	got := changed(t, Options{}, [3]string{"api.yaml", "    Unused:\n      type: string", "    Unused:\n      type: integer"})
	if want := []string{"api:api.yaml", "api:api.yaml#/components/schemas/Unused"}; !slices.Equal(got, want) {
		t.Errorf("changed %q, want %q", got, want)
	}
}

func TestDuplicateOperationIDs(t *testing.T) {
	dir := tree(t, [3]string{"hooks.yaml", "operationId: noteCreated", "operationId: getNote"})

	if _, err := Build(dir, notesDocs, Options{}); err == nil || !strings.Contains(err.Error(), "prefix") {
		t.Fatalf("err = %v, want one asking for a prefix", err)
	}

	docs := []Document{{Path: "api.yaml"}, {Path: "hooks.yaml", Prefix: "hooks."}}

	m, err := Build(dir, docs, Options{})
	if err != nil {
		t.Fatal(err)
	}

	if !slices.ContainsFunc(m.Operations, func(op Operation) bool { return op.ID == "api:hooks.getNote" }) {
		t.Error("no api:hooks.getNote")
	}
}

func TestCannotTell(t *testing.T) {
	for name, edit := range map[string][3]string{
		"a remote ref":                  {"api.yaml", `$ref: "#/components/schemas/NoteInput"`, `$ref: "https://schemas.example/note.yaml"`},
		"a path item in a missing file": {"api.yaml", "  /shapes:\n    get:", "  /more:\n    $ref: \"more.yaml#/item\"\n  /shapes:\n    get:"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Build(tree(t, edit), notesDocs, Options{})
			if !errors.Is(err, ErrCannotTell) {
				t.Errorf("err = %v, want ErrCannotTell", err)
			}
		})
	}
}

func TestBrokenRef(t *testing.T) {
	dir := tree(t, [3]string{"api.yaml", `$ref: "#/components/schemas/NoteInput"`, `$ref: "#/components/schemas/Missing"`})
	if _, err := Build(dir, notesDocs, Options{}); err == nil || !strings.Contains(err.Error(), "no such node") {
		t.Errorf("err = %v", err)
	}
}

func TestAnchorsAndMergeKeys(t *testing.T) {
	data := []byte("base: &b {type: string, maxLength: 3}\nuse:\n  <<: *b\n  maxLength: 5\n")

	v, err := decode(data)
	if err != nil {
		t.Fatal(err)
	}

	use := asMap(asMap(v)["use"])
	if use["type"] != "string" || use["maxLength"] != 5 {
		t.Errorf("use = %v", use)
	}
}

func TestGoName(t *testing.T) {
	for in, want := range map[string]string{
		"getNote":       "GetNote",
		"delete-note":   "DeleteNote",
		"list_notes.v2": "ListNotesV2",
		"2fa":           "N2fa",
		"":              "Empty",
		"get-by-ID":     "GetByID",
	} {
		if got := GoName(in); got != want {
			t.Errorf("GoName(%q) = %q, want %q", in, got, want)
		}
	}
}
