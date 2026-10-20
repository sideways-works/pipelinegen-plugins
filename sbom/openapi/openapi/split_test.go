package openapi

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// splitFiles are a root whose paths are $refs into one file per context, each
// referring to a file of shared parts.
var splitFiles = []string{"api.yaml", "notes/notes.yaml", "accounts/accounts.yaml", "shared/common.yaml"}

var splitOperations = []string{"api:createNote", "api:deleteNote", "api:getMe", "api:getNote", "api:health", "api:listNotes"}

func splitTree(t *testing.T, edits ...[3]string) string {
	t.Helper()

	dir := t.TempDir()

	for _, f := range splitFiles {
		data, err := os.ReadFile(filepath.Join("testdata", "split", filepath.FromSlash(f)))
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

		out := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(out, []byte(text), 0o600); err != nil { //nolint:gosec // f is one of the fixture's own names
			t.Fatal(err)
		}
	}

	return dir
}

func buildSplit(t *testing.T, dir string) *Model {
	t.Helper()

	m, err := Build(dir, []Document{{Path: "api.yaml"}}, Options{})
	if err != nil {
		t.Fatal(err)
	}

	return m
}

func splitReaching(t *testing.T, change ...[3]string) []string {
	t.Helper()

	before, after := buildSplit(t, splitTree(t)), buildSplit(t, splitTree(t, change...))
	db, da := digests(before), digests(after)

	differs := func(id string) bool {
		b, inBefore := db[id]
		a, inAfter := da[id]

		return inBefore != inAfter || a != b
	}

	var out []string

	for _, m := range []*Model{before, after} {
		for _, op := range m.Operations {
			if !slices.Contains(out, op.ID) && slices.ContainsFunc(closure(m, op.ID), differs) {
				out = append(out, op.ID)
			}
		}
	}

	slices.Sort(out)

	return out
}

func TestSplitContractHasEveryOperation(t *testing.T) {
	m := buildSplit(t, splitTree(t))

	var ids []string

	for _, op := range m.Operations {
		ids = append(ids, op.ID)

		if op.File != "api.yaml" {
			t.Errorf("%s belongs to %s, want the root that serves it", op.ID, op.File)
		}
	}

	slices.Sort(ids)

	if !slices.Equal(ids, splitOperations) {
		t.Fatalf("operations %q, want %q", ids, splitOperations)
	}

	i := slices.IndexFunc(m.Operations, func(op Operation) bool { return op.ID == "api:getNote" })
	if op := m.Operations[i]; op.From != "notes/notes.yaml" || op.Path != "/notes/{id}" || op.Method != http.MethodGet {
		t.Errorf("getNote = %+v", op)
	}

	if got := m.ReadFiles("api.yaml"); !slices.Equal(got, []string{"accounts/accounts.yaml", "api.yaml", "notes/notes.yaml", "shared/common.yaml"}) {
		t.Errorf("the root's answer is read from %q, want every file it refers to", got)
	}

	if got := m.ReadFiles("shared/common.yaml"); !slices.Equal(got, []string{"shared/common.yaml"}) {
		t.Errorf("a file of parts is read from %q, want itself alone", got)
	}

	units := map[string]bool{}
	for _, u := range m.Units {
		units[u.ID] = true
	}

	for _, l := range m.Links {
		if !strings.HasPrefix(l.From, "file:") && !units[l.To] {
			t.Errorf("link %s -> %s lands on no unit", l.From, l.To)
		}
	}

	for _, want := range []Link{
		{From: "api:getNote", To: "api:notes/notes.yaml#/components/schemas/Note", Kind: LinkUses},
		{From: "api:getNote", To: "api:notes/notes.yaml#/components/parameters/NoteId", Kind: LinkUses},
		{From: "api:listNotes", To: "api:shared/common.yaml#/components/parameters/Limit", Kind: LinkUses},
		{From: "api:createNote", To: "api:shared/common.yaml#/components/requestBodies/NewNote", Kind: LinkUses},
		{From: "api:getMe", To: "api:shared/common.yaml#/components/responses/Unauthorized", Kind: LinkUses},
		{From: "api:getMe", To: "api:api.yaml#/components/securitySchemes/session", Kind: LinkUses},
	} {
		if !slices.Contains(m.Links, want) {
			t.Errorf("no link %+v", want)
		}
	}
}

func TestSplitContractEditsReachTheirOperations(t *testing.T) {
	for name, c := range map[string]struct {
		edit [3]string
		want []string
	}{
		"an operation in a context's file": {
			[3]string{"notes/notes.yaml", "        \"204\": { description: Gone. }", "        \"204\": { description: Gone. }\n        \"409\": { description: Locked. }"},
			[]string{"api:deleteNote"},
		},
		"a path item's parameter": {
			[3]string{"notes/notes.yaml", "      required: true\n      schema: { type: string }", "      required: true\n      schema: { type: string, format: uuid }"},
			[]string{"api:deleteNote", "api:getNote"},
		},
		"a context's schema": {
			[3]string{"notes/notes.yaml", "text: { type: string, maxLength: 500 }", "text: { type: string, maxLength: 600 }"},
			[]string{"api:createNote", "api:getNote", "api:listNotes"},
		},
		"a shared parameter": {
			[3]string{"shared/common.yaml", "maximum: 200", "maximum: 100"},
			[]string{"api:listNotes"},
		},
		"a shared request body": {
			[3]string{"shared/common.yaml", "      required: true\n", "      required: false\n"},
			[]string{"api:createNote"},
		},
		"a shared response": {
			[3]string{"shared/common.yaml", "      description: Not signed in.\n      content:", "      description: Not signed in.\n      headers:\n        WWW-Authenticate: { schema: { type: string } }\n      content:"},
			[]string{"api:getMe"},
		},
		"a shared schema": {
			[3]string{"shared/common.yaml", "status: { type: integer }", "status: { type: integer, minimum: 100 }"},
			[]string{"api:getMe", "api:getNote", "api:listNotes"},
		},
		"a route added in a context's file": {
			[3]string{"accounts/accounts.yaml", "  /accounts/me:\n    get:", "  /accounts/me:\n    put:\n      operationId: putMe\n      responses:\n        \"204\": { description: Saved. }\n    get:"},
			append(slices.Clone(splitOperations), "api:putMe"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			want := slices.Sorted(slices.Values(c.want))
			if got := splitReaching(t, c.edit); !slices.Equal(got, want) {
				t.Errorf("selects %q, want %q", got, want)
			}
		})
	}
}

func TestSplitContractKeysNextToTheRef(t *testing.T) {
	dir := splitTree(t, [3]string{"api.yaml", "    $ref: \"accounts/accounts.yaml#/paths/~1accounts~1me\"",
		"    $ref: \"accounts/accounts.yaml#/paths/~1accounts~1me\"\n    parameters:\n      - $ref: \"#/components/parameters/Limit\""})

	m := buildSplit(t, dir)
	if want := (Link{From: "api:getMe", To: "api:api.yaml#/components/parameters/Limit", Kind: LinkUses}); !slices.Contains(m.Links, want) {
		t.Errorf("no link %+v", want)
	}
}

func TestSplitContractChain(t *testing.T) {
	dir := splitTree(t,
		[3]string{"api.yaml", "  /health:\n", "  /other:\n    $ref: \"accounts/accounts.yaml#/paths/~1other\"\n  /health:\n"},
		[3]string{"accounts/accounts.yaml", "components:\n", "  /other:\n    $ref: \"../notes/notes.yaml#/paths/~1notes~1{id}\"\ncomponents:\n"})

	_, err := Build(dir, []Document{{Path: "api.yaml"}}, Options{})
	if err == nil || !strings.Contains(err.Error(), "getNote") {
		// The same item under two paths is two operations with one id.
		t.Fatalf("err = %v, want a duplicate getNote", err)
	}

	dir = splitTree(t,
		[3]string{"api.yaml", "  /health:\n", "  /other:\n    $ref: \"accounts/accounts.yaml#/paths/~1other\"\n  /health:\n"},
		[3]string{"accounts/accounts.yaml", "components:\n", "  /other:\n    $ref: \"../shared/more.yaml#/item\"\ncomponents:\n"})

	if err := os.WriteFile(filepath.Join(dir, "shared", "more.yaml"), []byte("item:\n  get:\n    operationId: getOther\n    responses:\n      \"200\": { $ref: \"common.yaml#/components/responses/NotFound\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := buildSplit(t, dir)
	if !slices.Contains(m.ReadFiles("api.yaml"), "shared/more.yaml") {
		t.Errorf("the root's answer is read from %q, without the chain's last file", m.ReadFiles("api.yaml"))
	}

	if want := (Link{From: "api:getOther", To: "api:shared/common.yaml#/components/responses/NotFound", Kind: LinkUses}); !slices.Contains(m.Links, want) {
		t.Errorf("no link %+v", want)
	}
}

func TestSplitContractCannotTell(t *testing.T) {
	for name, c := range map[string]struct {
		edits []([3]string)
		says  string
	}{
		"a path item in a missing file": {
			[][3]string{{"api.yaml", "  /health:\n", "  /gone:\n    $ref: \"gone/gone.yaml#/paths/~1gone\"\n  /health:\n"}},
			"gone/gone.yaml is not in the tree",
		},
		"a schema in a missing file": {
			[][3]string{{"accounts/accounts.yaml", "schema: { $ref: \"#/components/schemas/Account\" }", "schema: { $ref: \"../gone.yaml#/Account\" }"}},
			"gone.yaml is not in the tree",
		},
		"a path item outside the tree": {
			[][3]string{{"api.yaml", "  /health:\n", "  /out:\n    $ref: \"../../out.yaml#/paths/~1out\"\n  /health:\n"}},
			"outside the tree",
		},
		"a cycle of path items": {
			[][3]string{
				{"api.yaml", "  /health:\n", "  /loop:\n    $ref: \"notes/notes.yaml#/paths/~1loop\"\n  /health:\n"},
				{"notes/notes.yaml", "components:\n", "  /loop:\n    $ref: \"../accounts/accounts.yaml#/paths/~1loop\"\ncomponents:\n"},
				{"accounts/accounts.yaml", "components:\n", "  /loop:\n    $ref: \"../notes/notes.yaml#/paths/~1loop\"\ncomponents:\n"},
			},
			"path item refs form a cycle: notes/notes.yaml#/paths/~1loop -> accounts/accounts.yaml#/paths/~1loop -> notes/notes.yaml#/paths/~1loop",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Build(splitTree(t, c.edits...), []Document{{Path: "api.yaml"}}, Options{})
			if !errors.Is(err, ErrCannotTell) || !strings.Contains(err.Error(), c.says) {
				t.Errorf("err = %v, want can't tell saying %q", err, c.says)
			}
		})
	}
}

func TestSplitContractMissingNode(t *testing.T) {
	dir := splitTree(t, [3]string{"api.yaml", "notes/notes.yaml#/paths/~1notes~1{id}", "notes/notes.yaml#/paths/~1notes~1{key}"})

	_, err := Build(dir, []Document{{Path: "api.yaml"}}, Options{})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "no such node") {
		t.Errorf("err = %v, want a ref to no node", err)
	}
}

func TestSplitContractOverlappingDocuments(t *testing.T) {
	dir := splitTree(t, [3]string{"accounts/accounts.yaml", "paths:\n", "openapi: 3.0.3\ninfo: { title: Accounts, version: \"1\" }\npaths:\n"})

	_, err := Build(dir, []Document{{Path: "api.yaml"}, {Path: "accounts/accounts.yaml"}}, Options{})
	if !errors.Is(err, ErrCannotTell) {
		t.Fatalf("err = %v, want can't tell", err)
	}

	for _, want := range []string{"getMe", "api.yaml (its path /accounts/me is a $ref into accounts/accounts.yaml)", "accounts/accounts.yaml", "prefix"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to say %q", err, want)
		}
	}

	m, err := Build(dir, []Document{{Path: "api.yaml"}, {Path: "accounts/accounts.yaml", Prefix: "accounts."}}, Options{})
	if err != nil {
		t.Fatal(err)
	}

	if !slices.ContainsFunc(m.Operations, func(op Operation) bool { return op.ID == "api:accounts.getMe" }) {
		t.Error("the prefix doesn't settle it")
	}
}
