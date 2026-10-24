package analysis

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
)

type fixture struct {
	once sync.Once
	r    *Result
	err  error
}

var fixtures = map[string]*fixture{}
var fixturesMu sync.Mutex

// analyzeFixture analyses a fixture module once per set of narrowings:
// loading type-checks it, which takes seconds.
func analyzeFixture(t *testing.T, dir string, cfg Config) *Result {
	t.Helper()
	t.Setenv("GOPROXY", "off")

	key := dir + "|" + cfg.Schema + cfg.Naming.TablePrefix

	fixturesMu.Lock()

	f, ok := fixtures[key]
	if !ok {
		f = &fixture{}
		fixtures[key] = f
	}
	fixturesMu.Unlock()

	f.once.Do(func() {
		f.r, f.err = Analyze(context.Background(), dir, cfg, Options{})
	})

	if f.err != nil {
		t.Fatal(f.err)
	}

	return f.r
}

func has(r *Result, l Link) bool {
	return slices.Contains(r.Links, l)
}

func linksFrom(r *Result, from, to string) []Link {
	var out []Link

	for _, x := range r.Links {
		if x.From == from && x.To == to {
			out = append(out, x)
		}
	}

	return out
}

func store(name string) string  { return "go:store#" + name }
func models(name string) string { return "go:models#" + name }
func rel(name string) string    { return RelationPrefix + name }

func TestLinks(t *testing.T) {
	r := analyzeFixture(t, "testdata/app", Config{})

	want := []Link{
		// a model's table from the naming strategy
		{store("method:Store.NoteByID"), rel("public.notes"), KindReads, "", ""},
		// saving writes the model and every association, transitively
		{store("method:Store.CreateNote"), rel("public.notes"), KindWrites, "", ""},
		{store("method:Store.CreateNote"), rel("public.labels"), KindWrites, "", ""},
		{store("method:Store.CreateNote"), rel("public.note_tags"), KindWrites, "", ""},
		{store("method:Store.CreateNote"), rel("archive.comments"), KindWrites, "", ""},
		// hooks and callbacks GORM runs for the statement
		{store("method:Store.CreateNote"), models("method:Note.BeforeSave"), KindCalls, "", ""},
		{store("method:Store.CreateNote"), store("func:stamp"), KindCalls, "", ""},
		{store("method:Store.WithNotes"), models("method:Author.AfterFind"), KindCalls, "", ""},
		// Model() and Update
		{store("method:Store.Rename"), rel("public.authors"), KindWrites, "", ""},
		// a literal Table() name
		{store("method:Store.CountTagged"), rel("public.note_tags"), KindReads, "", ""},
		// nested Preload
		{store("method:Store.WithNotes"), rel("public.authors"), KindReads, "", ""},
		{store("method:Store.WithNotes"), rel("public.notes"), KindReads, "", ""},
		{store("method:Store.WithNotes"), rel("public.labels"), KindReads, "", ""},
		{store("method:Store.WithNotes"), rel("public.note_tags"), KindReads, "", ""},
		// Joins by association, TableName(namer)
		{store("method:Store.WithProfile"), rel("public.author_profiles"), KindReads, "", ""},
		// Joins with SQL
		{store("method:Store.Labelled"), rel("public.note_tags"), KindReads, "", ""},
		// a subquery handle
		{store("method:Store.ByAuthorName"), rel("public.authors"), KindReads, "", ""},
		// a subquery in a condition's text
		{store("method:Store.Tagged"), rel("public.note_tags"), KindReads, "", ""},
		// Raw, Exec with a constant and with a variable assigned once
		{store("method:Store.AuthorOf"), rel("public.authors"), KindReads, "", ""},
		{store("method:Store.AuthorOf"), rel("public.notes"), KindReads, "", ""},
		{store("method:Store.Purge"), rel("public.drafts"), KindWrites, "", ""},
		{store("method:Store.Blank"), rel("public.notes"), KindWrites, "", ""},
		// Association().Append
		{store("method:Store.Speak"), rel("public.languages"), KindWrites, "", ""},
		{store("method:Store.Speak"), rel("public.author_languages"), KindWrites, "", ""},
		// the generic API
		{store("method:Store.FirstNote"), rel("public.notes"), KindReads, "", ""},
		{store("method:Store.AddTag"), rel("public.labels"), KindWrites, "", ""},
		// a transaction's handle
		{store("method:Store.AddLanguage"), rel("public.languages"), KindWrites, "", ""},
		// a scope that sets the table
		{store("method:Store.Archived"), rel("archive.old_comments"), KindReads, "", ""},
		{store("func:archived"), rel("archive.old_comments"), KindReads, "", ""},
		// a helper that picks the model, and the helper itself
		{store("method:Store.Deactivate"), rel("public.authors"), KindWrites, "", ""},
		{store("method:Store.authors"), rel("public.authors"), KindReads, "", ""},
		// TableName promoted from an embedded struct
		{store("method:Store.Record"), rel("public.events"), KindWrites, "", ""},
		// gorm.Expr with a subquery
		{store("method:Store.FirstLabel"), rel("public.labels"), KindReads, "", ""},
		// deleting with Select(clause.Associations)
		{store("method:Store.Forget"), rel("public.author_profiles"), KindWrites, "", ""},
		// clause literals
		{store("method:Store.Locked"), rel("public.note_tags"), KindReads, "", ""},
		{store("method:Store.Locked"), rel("public.drafts"), KindReads, "", ""},
		// a test file's declaration
		{store("func:seed@test"), rel("public.labels"), KindWrites, "", ""},
		// TableName methods link to their table
		{models("method:Tag.TableName"), rel("public.labels"), KindUses, "", ""},
		{models("method:stored.TableName"), rel("public.events"), KindUses, "", ""},
	}

	for _, l := range want {
		if !has(r, l) {
			t.Errorf("missing %+v; from %s to %s: %+v", l, l.From, l.To, linksFrom(r, l.From, l.To))
		}
	}
}

func TestNoLinks(t *testing.T) {
	r := analyzeFixture(t, "testdata/app", Config{})

	cases := []struct{ from, to, why string }{
		{store("method:Store.CreateNoteOnly"), rel("public.labels"), "Omit(clause.Associations) saves no association"},
		{store("method:Store.Rename"), rel("public.languages"), "Model(&Author{}) sets no association"},
		{store("method:Store.Deactivate"), rel("public.notes"), "the helper's Model(&Author{}) sets no association"},
		{store("method:Store.Archived"), rel("archive.comments"), "the scope's Table() replaces the model's table"},
		{store("method:Store.NoteByID"), models("method:Note.BeforeSave"), "a query runs no save hook"},
		{store("method:Store.NoteByID"), store("func:stamp"), "the callback runs for creates only"},
	}

	for _, c := range cases {
		for _, l := range linksFrom(r, c.from, c.to) {
			if l.While == "" {
				t.Errorf("%s -> %s: want no link at every level (%s), got %+v", c.from, c.to, c.why, l)
			}
		}
	}
}

func TestCannotTell(t *testing.T) {
	r := analyzeFixture(t, "testdata/app", Config{})

	want := map[string]string{
		store("method:Store.Drop"):       "SQL returned by fmt.Sprintf at store/store.go:",
		store("method:Store.Size"):       "a table name passed in as a parameter",
		store("method:Store.Save"):       "the model is an interface value",
		store("func:Total"):              "the statement's table is set outside this declaration",
		store("method:Store.Shards"):     "TableName returns a name built at run time",
		models("method:Shard.TableName"): "TableName returns a name built at run time",
		store("method:Store.Clause"):     "a clause with SQL built elsewhere",
		store("func:runSQL"):             "SQL passed in as a parameter",
	}

	got := map[string]string{}
	for _, c := range r.CannotTell {
		got[c.Unit] = strings.Join(c.Reasons, "; ")
	}

	for unit, reason := range want {
		if !strings.Contains(got[unit], reason) {
			t.Errorf("%s: want a reason with %q, got %q", unit, reason, got[unit])
		}
	}

	for unit, reason := range got {
		if _, ok := want[unit]; !ok {
			t.Errorf("%s: cannot tell (%s), want links", unit, reason)
		}
	}
}

func TestModels(t *testing.T) {
	r := analyzeFixture(t, "testdata/app", Config{})

	byType := map[string]ModelInfo{}
	for _, m := range r.Models {
		byType[strings.TrimPrefix(m.Type, "example.com/app/models.")] = m
	}

	note := byType["Note"]
	if !note.SoftDelete {
		t.Error("Note embeds gorm.Model: want soft delete")
	}

	cols := map[string]string{}
	for _, c := range note.Columns {
		cols[c.Field] = c.Column
	}

	for field, col := range map[string]string{"Body": "text", "By": "audit_by", "DeletedAt": "deleted_at", "Meta": "meta", "AuthorID": "author_id"} {
		if cols[field] != col {
			t.Errorf("Note.%s: column %q, want %q", field, cols[field], col)
		}
	}

	if _, ok := cols["Scratch"]; ok {
		t.Error(`Note.Scratch is gorm:"-": want no column`)
	}

	if note.Associations["Tags"] != "example.com/app/models.Tag" || note.Associations["Meta"] != "" {
		t.Errorf("Note associations: %v", note.Associations)
	}

	if got := byType["Profile"].Tables; len(got) != 1 || got[0] != rel("public.author_profiles") {
		t.Errorf("Profile tables %v", got)
	}

	if got := byType["Shard"].Unknown; got == "" {
		t.Error("Shard: want an unknown table")
	}
}

func TestWideLinks(t *testing.T) {
	r := analyzeFixture(t, "testdata/app", Config{})

	for _, l := range []Link{
		// a model's type reaches its table while gorm-model-types is off
		{models("type:Note"), rel("public.notes"), KindUses, NarrowModelTypes, WhileOff},
		// a literal that sets no association saves none, unless the wide rule
		{store("method:Store.Rename"), rel("public.languages"), KindWrites, NarrowAssociations, WhileOff},
		// Table() replaces the model's table, unless the wide rule
		{store("method:Store.Archived"), rel("archive.comments"), KindReads, NarrowModels, WhileOff},
		// a query runs no save hook, unless the wide rule
		{store("method:Store.NoteByID"), models("method:Note.BeforeSave"), KindCalls, NarrowModels, WhileOff},
	} {
		if !has(r, l) {
			t.Errorf("missing %+v; got %+v", l, linksFrom(r, l.From, l.To))
		}
	}

	for _, l := range r.Links {
		if l.While != "" && (l.Narrowing == "" || l.While != WhileOff) {
			t.Errorf("a tagged link needs its narrowing and while off: %+v", l)
		}

		if l.While == "" && l.Narrowing != "" {
			t.Errorf("a link that holds at every level has no narrowing: %+v", l)
		}
	}
}

func TestNaming(t *testing.T) {
	r := analyzeFixture(t, "testdata/app", Config{Schema: "app", Naming: Naming{TablePrefix: "t_"}})

	for _, l := range []Link{
		{store("method:Store.NoteByID"), rel("app.t_notes"), KindReads, "", ""},
		// TableName ignores the prefix; a qualified name keeps its schema
		{store("method:Store.AddTag"), rel("app.labels"), KindWrites, "", ""},
		{store("method:Store.Archived"), rel("archive.old_comments"), KindReads, "", ""},
		// unqualified names in SQL take the configured schema
		{store("method:Store.Purge"), rel("app.drafts"), KindWrites, "", ""},
		// a many-to-many join table takes the prefix
		{store("method:Store.Speak"), rel("app.t_author_languages"), KindWrites, "", ""},
	} {
		if !has(r, l) {
			t.Errorf("missing %+v", l)
		}
	}
}

func TestValuesBuiltFromConstants(t *testing.T) {
	r := analyzeFixture(t, "testdata/app", Config{})

	for _, l := range []Link{
		// every caller passes a constant table name
		{store("func:purgeFrom"), rel("public.drafts"), KindWrites, "", ""},
		{store("func:purgeFrom"), rel("archive.old_drafts"), KindWrites, "", ""},
		// a condition appended to in a branch
		{store("func:Filter"), rel("public.authors"), KindReads, "", ""},
		// strings.Join over appended conditions, inside fmt.Sprintf
		{store("func:Joined"), rel("public.note_tags"), KindReads, "", ""},
		{store("func:Joined"), rel("public.notes"), KindReads, "", ""},
	} {
		if !has(r, l) {
			t.Errorf("missing %+v", l)
		}
	}

	for _, c := range r.CannotTell {
		if c.Unit == store("func:runSQL") {
			if !strings.Contains(strings.Join(c.Reasons, ";"), "passed in as a parameter") {
				t.Errorf("runSQL: %v", c.Reasons)
			}

			return
		}
	}

	t.Error("runSQL is passed as a value: its SQL can't be told from its callers")
}

func TestRoutineLinks(t *testing.T) {
	r := analyzeFixture(t, "testdata/app", Config{})

	if !has(r, Link{store("method:Store.Score"), RoutinePrefix + "public.note_score", KindUses, "", ""}) {
		t.Errorf("want a link to the routine; got %+v", linksFrom(r, store("method:Store.Score"), RoutinePrefix+"public.note_score"))
	}

	for _, l := range r.Links {
		if strings.HasPrefix(l.To, RoutinePrefix) && l.To != RoutinePrefix+"public.note_score" {
			t.Errorf("link to a built-in or unknown routine: %+v", l)
		}
	}
}

func TestDetect(t *testing.T) {
	cases := []struct {
		name, content, role string
	}{
		{"go.mod", "module x\n\nrequire gorm.io/gorm v1.31.2\n", RoleModule},
		{"go.mod", "module x\n\nrequire gorm.io/gorm v1.31.2 // indirect\n", RoleModuleFile},
		{"go.mod", "module x\n", RoleModuleFile},
		{"go.sum", "", RoleModuleFile},
		{"go.work", "go 1.27\n", RoleModuleFile},
		{"a.go", "package a\n\nimport \"gorm.io/gorm\"\n\nvar _ *gorm.DB\n", RoleUser},
		{"a.go", "package a\n\nimport \"gorm.io/gorm/clause\"\n", RoleUser},
		{"a.go", "package a\n\nimport \"gorm.io/gormish\"\n", RoleCode},
		{"a.go", "package a\n\n// gorm.io/gorm in a comment\n", RoleCode},
		{"a.go", "not go", RoleCode},
		{"testdata/a.go", "package a\n\nimport \"gorm.io/gorm\"\n", ""},
		{"x/vendor/a.go", "package a\n", ""},
		{".cache/go.mod", "module x\n\nrequire gorm.io/gorm v1.31.2\n", ""},
		{"notes.txt", "gorm.io/gorm", ""},
	}

	for _, c := range cases {
		role, ok := DetectFile(c.name, []byte(c.content))
		if role != c.role || ok != (c.role != "") {
			t.Errorf("%s %q: role %q %v, want %q", c.name, c.content, role, ok, c.role)
		}
	}

	mods, err := DetectModules("testdata")
	if err != nil || strings.Join(mods, ",") != "app" {
		t.Errorf("modules %v %v, want [app]", mods, err)
	}

	// testdata directories are skipped, as the Go tool skips them
	if mods, _ := DetectModules("."); len(mods) != 0 { //nolint:errcheck // checked through mods
		t.Errorf("modules under testdata claimed: %v", mods)
	}
}

func TestClaimedInputs(t *testing.T) {
	claimed := []string{
		"go.work", "app/go.mod", "app/go.sum", "app/main.go", "app/store/store.go",
		"app/tools/go.mod", "app/tools/tool.go", // a nested module
		"lib/go.mod", "lib/go.sum", "lib/lib.go", // replaced into app
		"other/go.mod", "other/x.go",
	}

	got := ClaimedInputs(claimed, "app", []string{"lib"})
	want := []string{"app/go.mod", "app/go.sum", "app/main.go", "app/store/store.go", "go.work", "lib/go.mod", "lib/lib.go"}

	if !slices.Equal(got, want) {
		t.Errorf("inputs %q, want %q", got, want)
	}
}
