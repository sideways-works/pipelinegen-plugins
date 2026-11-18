package correlate

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestMapReaders(t *testing.T) {
	links, err := load(t).MapReaders(repo)
	if err != nil {
		t.Fatal(err)
	}

	from := map[string]int{}

	for _, l := range links {
		if l.Kind != LinkReads {
			t.Errorf("link %+v isn't a read", l)
		}

		from[l.From]++
	}

	units := len(load(t).DocumentUnits("api/notes.yaml"))
	want := map[string]int{
		// keyed by operationIds
		"go:internal/access#var:Roles": units,
		// keyed by the generated names, in a test file
		"go:internal/access#func:TestEveryOperationHasARole@test": units,
	}

	if len(from) != len(want) {
		t.Errorf("readers %v, want %v", from, want)
	}

	for id, n := range want {
		if from[id] != n {
			t.Errorf("%s reads %d units, want every unit of the document (%d)", id, from[id], n)
		}
	}
}

func TestServingCodeReachesWhatItsOperationUses(t *testing.T) {
	wrapper := func(op string) string { return "go:gen/notesgen#method:ServerInterfaceWrapper." + op }
	serving := []string{
		wrapper("ListNotes"), wrapper("CreateNote"), wrapper("GetNote"), wrapper("ListDrafts"), wrapper("GetNoteText"),
		"go:internal/handlers#method:Notes.GetNote",
	}

	for _, tc := range []struct {
		name string
		file string
		old  string
		new  string
		want []string
	}{
		{
			"a shared schema", "api/notes.yaml", "text: { type: string }", "text: { type: string, maxLength: 9 }",
			[]string{wrapper("CreateNote"), wrapper("GetNote"), wrapper("ListNotes"), "go:internal/handlers#method:Notes.GetNote"},
		},
		{
			"a path item's parameter", "api/notes.yaml", "{ name: id, in: path, required: true, schema: { type: string } }\n    get:\n      operationId: getNote",
			"{ name: id, in: path, required: true, schema: { type: string, format: uuid } }\n    get:\n      operationId: getNote",
			[]string{wrapper("GetNote"), "go:internal/handlers#method:Notes.GetNote"},
		},
		{"one operation's own part", "api/notes.yaml", "\"200\": { description: Drafts. }", "\"200\": { description: Drafts. }\n        \"404\": { description: None. }", []string{wrapper("ListDrafts")}},
		{"a route in another document", "api/tags.yaml", "  /tags:", "  /labels:", serving},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := loadTree(t, "", "", "")
			after := loadTree(t, tc.file, tc.old, tc.new)

			if got := reach(t, before, after, serving); !slices.Equal(got, sorted(tc.want)) {
				t.Errorf("selects %q, want %q", got, sorted(tc.want))
			}
		})
	}
}

func TestRouteRegistrationTakesEveryRoutingTable(t *testing.T) {
	before := loadTree(t, "", "", "")
	after := loadTree(t, "api/tags.yaml", "  /tags:", "  /labels:")

	if !digestFromChanged(t, before, after, "go:gen/notesgen#func:HandlerWithOptions") {
		t.Error("a route moved in another document leaves HandlerWithOptions' digest as it was")
	}

	prose := loadTree(t, "api/notes.yaml", "description: One note.", "description: A single note.")
	if digestFromChanged(t, before, prose, "go:gen/notesgen#func:HandlerWithOptions") {
		t.Error("a prose edit changes HandlerWithOptions' digest")
	}
}

type treeModel struct {
	*Model

	dir string
}

func loadTree(t *testing.T, file, old, replacement string) treeModel {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.CopyFS(dir, os.DirFS(repo)); err != nil {
		t.Fatal(err)
	}

	if file != "" {
		p := filepath.Join(dir, filepath.FromSlash(file))

		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}

		if !strings.Contains(string(data), old) {
			t.Fatalf("edit: %q is not in %s", old, file)
		}

		write(t, p, strings.Replace(string(data), old, replacement, 1))
	}

	m, err := Load(dir, Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	return treeModel{Model: m, dir: dir}
}

func digestsOf(m *Model) map[string]string {
	out := map[string]string{}
	for _, u := range m.Units {
		out[u.ID] = u.Digest
	}

	return out
}

func reach(t *testing.T, before, after treeModel, roots []string) []string {
	t.Helper()

	db, da := digestsOf(before.Model), digestsOf(after.Model)
	differs := func(id string) bool {
		b, inB := db[id]
		a, inA := da[id]

		return inB != inA || a != b
	}

	var out []string

	for _, m := range []treeModel{before, after} {
		next := map[string][]string{}

		for _, l := range allOf(t, m) {
			if l.Narrowing == "" || l.While == "on" {
				next[l.From] = append(next[l.From], l.To)
			}
		}

		for _, root := range roots {
			if !slices.Contains(out, root) && slices.ContainsFunc(closureOf(next, root), differs) {
				out = append(out, root)
			}
		}
	}

	slices.Sort(out)

	return out
}

func allOf(t *testing.T, m treeModel) []Link {
	t.Helper()

	hl, err := m.HandlerLinks(m.dir)
	if err != nil {
		t.Fatal(err)
	}

	out := append(m.CodeLinks(), hl...)
	for _, l := range m.Links {
		out = append(out, Link{From: l.From, To: l.To, Kind: l.Kind, Narrowing: l.Narrowing, While: l.While})
	}

	return out
}

func closureOf(next map[string][]string, root string) []string {
	seen := map[string]bool{root: true}
	stack := []string{root}

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
	for id := range seen {
		out = append(out, id)
	}

	return out
}

func digestFromChanged(t *testing.T, before, after treeModel, unit string) bool {
	t.Helper()

	db, da := digestsOf(before.Model), digestsOf(after.Model)

	for _, r := range after.DigestFrom() {
		if r.Unit != unit {
			continue
		}

		return slices.ContainsFunc(r.From, func(id string) bool { return db[id] != da[id] })
	}

	t.Fatalf("no digest_from for %s", unit)

	return false
}

func sorted(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)

	return out
}
