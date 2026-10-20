package openapi

import (
	"slices"
	"strings"
	"testing"
)

func closure(m *Model, id string) []string {
	next := map[string][]string{}

	for _, l := range m.Links {
		if l.Narrowing == "" || l.While == "on" {
			next[l.From] = append(next[l.From], l.To)
		}
	}

	seen := map[string]bool{id: true}
	stack := []string{id}

	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		for _, to := range next[cur] {
			if !seen[to] {
				seen[to] = true
				stack = append(stack, to)
			}
		}
	}

	out := make([]string, 0, len(seen))
	for u := range seen {
		out = append(out, u)
	}

	return out
}

// reaching lists the operations a change selects. base edits apply to both
// trees.
func reaching(t *testing.T, base [][3]string, change ...[3]string) []string {
	t.Helper()

	before := build(t, tree(t, base...), Options{})
	after := build(t, tree(t, append(slices.Clone(base), change...)...), Options{})
	db, da := digests(before), digests(after)

	differs := func(id string) bool {
		b, inBefore := db[id]
		a, inAfter := da[id]

		return inBefore != inAfter || a != b
	}

	var out []string

	for _, m := range []*Model{before, after} {
		for _, op := range m.Operations {
			if slices.Contains(out, op.ID) {
				continue
			}

			if slices.ContainsFunc(closure(m, op.ID), differs) {
				out = append(out, op.ID)
			}
		}
	}

	slices.Sort(out)

	return out
}

var components = [][3]string{
	{"api.yaml", "      requestBody:\n        description: The new note.\n        content:\n          application/json:\n            schema: { $ref: \"#/components/schemas/NoteInput\" }\n",
		"      requestBody: { $ref: \"#/components/requestBodies/NewNote\" }\n"},
	{"api.yaml", "        \"204\": { description: Gone. }", "        \"204\": { $ref: \"#/components/responses/Gone\" }"},
	{"api.yaml", "components:\n", "components:\n" +
		"  requestBodies:\n    NewNote:\n      content:\n        application/json:\n          schema: { $ref: \"#/components/schemas/NoteInput\" }\n" +
		"  responses:\n    Gone:\n      description: Gone.\n      headers:\n        X-Trace: { $ref: \"#/components/headers/Trace\" }\n" +
		"  headers:\n    Trace:\n      schema: { $ref: \"#/components/schemas/TraceId\" }\n"},
	{"api.yaml", "        text: { type: string, maxLength: 500 }", "        text: { type: string, maxLength: 500 }\n        label: { $ref: \"#/components/schemas/Label\" }"},
	{"api.yaml", "    Unused:\n", "    TraceId: { type: string, maxLength: 32 }\n    Label: { type: string, minLength: 1 }\n    Unused:\n"},
}

func TestRefsReachEveryOperationThatUsesThem(t *testing.T) {
	notes := []string{"api:createNote", "api:getNote", "api:listNotes"}

	for _, tc := range []struct {
		name   string
		change [3]string
		want   []string
	}{
		{"a request body component", [3]string{"api.yaml", "    NewNote:\n      content:", "    NewNote:\n      required: true\n      content:"}, []string{"api:createNote"}},
		{"a response component", [3]string{"api.yaml", "X-Trace: {", "X-Request: {"}, []string{"api:delete-note"}},
		{"a header component", [3]string{"api.yaml", "    Trace:\n      schema:", "    Trace:\n      required: true\n      schema:"}, []string{"api:delete-note"}},
		{"a schema behind a header behind a response", [3]string{"api.yaml", "maxLength: 32", "maxLength: 64"}, []string{"api:delete-note"}},
		{"a schema behind a schema behind a request body", [3]string{"api.yaml", "Label: { type: string, minLength: 1 }", "Label: { type: string, minLength: 2 }"}, []string{"api:createNote"}},
		{"a shared parameter", [3]string{"api.yaml", "maximum: 100", "maximum: 50"}, []string{"api:listNotes"}},
		{"a schema in another file", [3]string{"common.yaml", "minLength: 1", "minLength: 2"}, append(slices.Clone(notes), "api:listTags", "api:noteCreated")},
		{"a schema used by a schema in another file", [3]string{"api.yaml", "text: { type: string, title: Text }", "text: { type: string, title: Text, maxLength: 9 }"}, append(slices.Clone(notes), "api:noteCreated")},
		{"a path item's parameter", [3]string{"api.yaml", "description: The note's id.\n        schema: { type: string }", "description: The note's id.\n        schema: { type: string, format: uuid }"}, []string{"api:delete-note", "api:getNote"}},
		{"the document's security", [3]string{"api.yaml", "security:\n  - session: []\npaths:", "security: []\npaths:"}, []string{"api:createNote", "api:delete-note", "api:getNote", "api:listNotes"}},
		{"a security scheme", [3]string{"api.yaml", "name: sid", "name: session_id"}, []string{"api:createNote", "api:delete-note", "api:getNote", "api:listNotes"}},
		{"a top-level extension", [3]string{"api.yaml", "servers:\n", "x-rate: 10\nservers:\n"}, slices.DeleteFunc(slices.Clone(everyOperation), func(id string) bool { return id == "api:noteCreated" })},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := reaching(t, components, tc.change); !slices.Equal(got, tc.want) {
				t.Errorf("selects %q, want %q", got, tc.want)
			}
		})
	}
}

var everyOperation = []string{
	"api:createNote", "api:delete-note", "api:getNote", "api:listComments",
	"api:listNotes", "api:listShapes", "api:listTags", "api:noteCreated",
}

func TestRoutesCountForTheWholeSurface(t *testing.T) {
	plus := func(ids ...string) []string {
		out := append(slices.Clone(everyOperation), ids...)
		slices.Sort(out)

		return out
	}

	for _, tc := range []struct {
		name   string
		change [3]string
		want   []string
	}{
		{"a route that overlaps nothing added", [3]string{"api.yaml", "  /tags:\n", "  /labels:\n    get:\n      operationId: listLabels\n      responses:\n        \"200\": { description: Labels. }\n  /tags:\n"}, plus("api:listLabels")},
		{"a route removed", [3]string{"api.yaml", "  /tags:\n    get:\n      operationId: listTags\n      security: []\n      responses:\n        \"200\":\n          description: Tags.\n          content:\n            application/json:\n              schema:\n                type: array\n                items: { $ref: \"common.yaml#/Tag\" }\n", ""}, everyOperation},
		{"a route moved", [3]string{"api.yaml", "  /shapes:", "  /figures:"}, everyOperation},
		{"a method changed", [3]string{"api.yaml", "    delete:\n      operationId: delete-note", "    put:\n      operationId: delete-note"}, everyOperation},
		{"HEAD added beside GET", [3]string{"api.yaml", "    delete:\n      operationId: delete-note", "    head:\n      operationId: headNote\n      responses:\n        \"200\": { description: Head. }\n    delete:\n      operationId: delete-note"}, plus("api:headNote")},
		{"a route added in another document", [3]string{"hooks.yaml", "paths: {}", "paths:\n  /ping:\n    get:\n      operationId: ping\n      responses:\n        \"200\": { description: Pong. }"}, plus("api:ping")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := reaching(t, nil, tc.change); !slices.Equal(got, tc.want) {
				t.Errorf("selects %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("an edit that moves no route stays with its operation", func(t *testing.T) {
		got := reaching(t, nil, [3]string{"api.yaml", "operationId: getNote\n      x-access: user", "operationId: getNote\n      x-access: public"})
		if !slices.Equal(got, []string{"api:getNote"}) {
			t.Errorf("selects %q", got)
		}
	})
}

func TestRouteUnits(t *testing.T) {
	m := build(t, tree(t), Options{})

	if got, want := m.RouteUnits(), []string{"api:api.yaml#/paths", "api:hooks.yaml#/paths"}; !slices.Equal(got, want) {
		t.Errorf("route units %q, want %q", got, want)
	}

	if !slices.Contains(m.TopUnits("api.yaml"), "api:api.yaml#/paths") {
		t.Errorf("top units %q lack the routing table", m.TopUnits("api.yaml"))
	}

	for _, want := range []Link{
		{From: "api:getNote", To: "api:api.yaml#/paths", Kind: LinkUses},
		{From: "api:getNote", To: "api:hooks.yaml#/paths", Kind: LinkUses},
		{From: "api:noteCreated", To: "api:api.yaml#/paths", Kind: LinkUses},
	} {
		if !slices.Contains(m.Links, want) {
			t.Errorf("no link %+v", want)
		}
	}

	if slices.ContainsFunc(m.Units, func(u Unit) bool { return strings.HasPrefix(u.ID, "api:common.yaml#/paths") }) {
		t.Error("a file of parts has no routing table")
	}
}

func TestPathItemThroughARef(t *testing.T) {
	// Build the document with the item in components and a ref in paths.
	base := [][3]string{{"api.yaml", "  /notes/{id}:\n    parameters:\n      - name: id\n        in: path\n        required: true\n        description: The note's id.\n        schema: { type: string }\n    get:\n      operationId: getNote\n      x-access: user\n      responses:\n        \"200\":\n          description: One note.\n          content:\n            application/json:\n              schema: { $ref: \"#/components/schemas/Note\" }\n    delete:\n      operationId: delete-note\n      x-access: user\n      responses:\n        \"204\": { description: Gone. }\n",
		"  /notes/{id}:\n    $ref: \"#/components/pathItems/NoteItem\"\n"},
		{"api.yaml", "components:\n", "components:\n  pathItems:\n    NoteItem:\n      parameters:\n        - name: id\n          in: path\n          required: true\n          schema: { type: string }\n      get:\n        operationId: getNote\n        responses:\n          \"200\": { description: One note. }\n      delete:\n        operationId: delete-note\n        responses:\n          \"204\": { description: Gone. }\n"}}

	m := build(t, tree(t, base...), Options{})
	if !slices.ContainsFunc(m.Operations, func(op Operation) bool { return op.ID == "api:getNote" && op.Path == "/notes/{id}" }) {
		t.Fatalf("getNote isn't read through the ref: %+v", m.Operations)
	}

	t.Run("an edit inside the item", func(t *testing.T) {
		got := reaching(t, base, [3]string{"api.yaml", "          schema: { type: string }\n      get:", "          schema: { type: string, format: uuid }\n      get:"})
		if want := []string{"api:delete-note", "api:getNote"}; !slices.Equal(got, want) {
			t.Errorf("selects %q, want %q", got, want)
		}
	})

	t.Run("a method added inside the item", func(t *testing.T) {
		got := reaching(t, base, [3]string{"api.yaml", "      delete:\n        operationId: delete-note", "      head:\n        operationId: headNote\n        responses:\n          \"200\": { description: Head. }\n      delete:\n        operationId: delete-note"})

		want := append(slices.Clone(everyOperation), "api:headNote")
		slices.Sort(want)

		if !slices.Equal(got, want) {
			t.Errorf("selects %q, want %q", got, want)
		}
	})
}

// Go's request patterns carry a wildcard's name and code may key by the
// pattern, so renaming a parameter is a route change.
func TestParameterRenameIsARouteChange(t *testing.T) {
	got := reaching(t, nil,
		[3]string{"api.yaml", "  /notes/{id}:\n    parameters:\n      - name: id", "  /notes/{noteId}:\n    parameters:\n      - name: noteId"})
	if !slices.Equal(got, everyOperation) {
		t.Errorf("selects %q, want every operation", got)
	}
}
