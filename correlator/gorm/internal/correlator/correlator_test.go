package correlator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"

	"github.com/sideways-works/pipelinegen-correlator-gorm/internal/analysis"
)

const tree = "../analysis/testdata"

func request(t *testing.T, extra map[string]any) []byte {
	t.Helper()
	t.Setenv("GOPROXY", "off")

	req := map[string]any{
		"protocol": plugin.Protocol, "work": t.TempDir(), "cache": t.TempDir(), "tree": tree,
		"config": map[string]any{},
	}
	maps.Copy(req, extra)

	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func links(t *testing.T, extra map[string]any) plugin.LinksAnswer {
	t.Helper()

	out, err := Links(context.Background(), request(t, extra), "v0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	ans, ok := out.(plugin.LinksAnswer)
	if !ok {
		t.Fatalf("answer %T", out)
	}

	return ans
}

func TestDescribe(t *testing.T) {
	d := Describe("v1.2.3")
	if d.Kind != plugin.KindCorrelator || len(d.Namespaces) != 0 || d.Version != "v1.2.3" {
		t.Errorf("describe: %+v", d)
	}

	if d.Inputs[plugin.CallLinks] != "per-set" || d.Inputs[plugin.CallDetect] != "per-file" || len(d.Candidates) == 0 {
		t.Errorf("inputs %v, candidates %v", d.Inputs, d.Candidates)
	}

	names := map[string]bool{}
	for _, n := range d.Narrowings {
		names[n.Name] = n.OffAt == 5
	}

	for _, n := range []string{analysis.NarrowModels, analysis.NarrowAssociations, analysis.NarrowModelTypes} {
		if !names[n] {
			t.Errorf("narrowing %s not declared off from 5", n)
		}
	}
}

func TestDetect(t *testing.T) {
	out, err := Detect(context.Background(), request(t, map[string]any{
		"paths": []string{"app/go.mod", "app/store/store.go", "app/models/models.go", "app/store/missing.go", "app/go.sum"},
	}), "v0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	ans, _ := out.(plugin.DetectAnswer) //nolint:errcheck // checked below

	want := []plugin.Claim{
		{Path: "app/go.mod", Role: RoleManifest},
		{Path: "app/store/store.go", Role: RoleSource},
		{Path: "app/models/models.go", Role: RoleSource},
		{Path: "app/go.sum", Role: RoleModuleFile},
	}
	if !slices.EqualFunc(ans.Claims, want, func(a, b plugin.Claim) bool { return a.Path == b.Path && a.Role == b.Role }) {
		t.Errorf("claims %+v, want %+v", ans.Claims, want)
	}
}

func TestLinks(t *testing.T) {
	ans := links(t, nil)
	if len(ans.Sets) != 1 || ans.Sets[0].Group != "app" || ans.Cacheable != nil {
		t.Fatalf("sets %+v cacheable %v", ans.Sets, ans.Cacheable)
	}

	s := ans.Sets[0]

	if !slices.Contains(s.Links, plugin.Link{From: "go:app/store#method:Store.NoteByID", To: "pg:relation:public.notes", Kind: "reads", Ref: true}) {
		t.Errorf("no untagged link from NoteByID to notes in %d links", len(s.Links))
	}

	if !slices.Contains(s.Links, plugin.Link{From: "go:app/models#type:Note", To: "pg:relation:public.notes", Kind: "uses",
		Narrowing: analysis.NarrowModelTypes, While: analysis.WhileOff}) {
		t.Error("no wide type link")
	}

	var drop *plugin.CannotTell

	for i, c := range s.CannotTell {
		if c.Unit == "go:app/store#method:Store.Drop" {
			drop = &s.CannotTell[i]
		}
	}

	if drop == nil || !strings.Contains(drop.Reason, "app/store/store.go:") {
		t.Errorf("Drop: want a cannot tell with its place, got %+v", drop)
	}

	for _, in := range []string{"app/go.mod", "app/go.sum", "app/store/store.go", "app/store/store_test.go", "app/models/models.go"} {
		if !slices.Contains(s.Inputs, in) {
			t.Errorf("inputs %v lack %s", s.Inputs, in)
		}
	}
}

func TestLinksMarkReferences(t *testing.T) {
	s := links(t, nil).Sets[0]

	for _, c := range []struct {
		from, to, kind string
		ref            bool
	}{
		{"go:app/store#method:Store.CreateNote", "pg:relation:public.notes", "writes", true},
		{"go:app/store#method:Store.Score", "pg:routine:public.note_score", "uses", true},
		{"go:app/models#method:Tag.TableName", "pg:relation:public.labels", "uses", true},
		{"go:app/models#type:Note", "pg:relation:public.notes", "uses", false},
		{"go:app/store#method:Store.CreateNote", "go:app/models#method:Note.BeforeSave", "calls", false},
		{"go:app/store#method:Store.CreateNote", "go:app/store#func:stamp", "calls", false},
		{"go:app/store#method:Store.Archived", "pg:relation:archive.comments", "reads", false},
	} {
		found := false

		for _, l := range s.Links {
			if l.From != c.from || l.To != c.to || l.Kind != c.kind {
				continue
			}

			found = true

			if l.Ref != c.ref {
				t.Errorf("%s %s %s: ref %v, want %v", c.from, c.kind, c.to, l.Ref, c.ref)
			}
		}

		if !found {
			t.Errorf("no link %s %s %s", c.from, c.kind, c.to)
		}
	}
}

func TestLinksAnswerDoesNotDependOnNarrowings(t *testing.T) {
	a := links(t, nil)
	b := links(t, map[string]any{"narrowings": []string{analysis.NarrowModels}})

	if len(a.Sets[0].Links) != len(b.Sets[0].Links) {
		t.Errorf("%d links without narrowings, %d with", len(a.Sets[0].Links), len(b.Sets[0].Links))
	}
}

func TestLinksForPaths(t *testing.T) {
	if ans := links(t, map[string]any{"paths": []string{"elsewhere/x.go"}}); len(ans.Sets) != 0 {
		t.Errorf("a path outside every module answered %+v", ans.Sets)
	}

	if ans := links(t, map[string]any{"paths": []string{"app/store/store.go"}}); len(ans.Sets) != 1 {
		t.Errorf("a module's path answered %d sets", len(ans.Sets))
	}

	// a module the core still has claimed but whose go.mod is gone answers
	// an empty set
	ans := links(t, map[string]any{
		"paths":   []string{"gone/go.mod"},
		"claimed": []plugin.Claim{{Path: "gone/go.mod", Role: RoleManifest}},
	})
	if len(ans.Sets) != 1 || ans.Sets[0].Group != "gone" || len(ans.Sets[0].Links) != 0 {
		t.Errorf("deleted module: %+v", ans.Sets)
	}
}

func TestLinksInFiles(t *testing.T) {
	old := inlineLinks
	inlineLinks = 1

	defer func() { inlineLinks = old }()

	work := t.TempDir()

	ans := links(t, map[string]any{"work": work})
	if len(ans.Sets) != 0 || len(ans.Files) != 1 {
		t.Fatalf("want the sets in a file, got %d inline and files %v", len(ans.Sets), ans.Files)
	}

	sets, err := plugin.ReadLines(work, ans.Files, []plugin.Set(nil))
	if err != nil || len(sets) != 1 || len(sets[0].Links) == 0 {
		t.Fatalf("reading %v: %v (%d sets)", ans.Files, err, len(sets))
	}
}

func TestBadConfig(t *testing.T) {
	_, err := Links(context.Background(), request(t, map[string]any{"config": map[string]any{"modulez": []string{"app"}}}), "v0.0.1")
	if !errors.Is(err, ErrRequest) {
		t.Errorf("want ErrRequest for an unknown config field, got %v", err)
	}
}

func TestWhy(t *testing.T) {
	out, err := Why(context.Background(), request(t, map[string]any{
		"from": "go:app/store#method:Store.Purge", "to": "pg:relation:public.drafts",
	}), "v0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	ans, _ := out.(WhyAnswer) //nolint:errcheck // checked below
	if len(ans.Path) != 2 || !strings.Contains(ans.Path[0].Note, "app/store/store.go:") {
		t.Errorf("why: %+v", ans)
	}

	_, err = Why(context.Background(), request(t, map[string]any{
		"from": "go:app/store#method:Store.Purge", "to": "pg:relation:public.nowhere",
	}), "v0.0.1")
	if !errors.Is(err, plugin.ErrCantTell) {
		t.Errorf("want can't tell for a link that doesn't exist, got %v", err)
	}
}

func TestCache(t *testing.T) {
	cache := t.TempDir()
	links(t, map[string]any{"cache": cache})

	if _, err := os.Stat(cacheFile(cache, "v0.0.1")); err != nil {
		t.Errorf("no parse cache written: %v", err)
	}

	dev := t.TempDir()

	if _, err := Links(context.Background(), request(t, map[string]any{"cache": dev}), "dev"); err != nil {
		t.Fatal(err)
	}

	if entries, _ := os.ReadDir(dev); len(entries) != 0 { //nolint:errcheck // an unreadable directory has no entries either
		t.Errorf("a development build wrote a cache: %v", entries)
	}
}

func TestMissingDependency(t *testing.T) {
	root := t.TempDir()

	for name, content := range map[string]string{
		"svc/go.mod": "module svc\n\ngo 1.27.1\n\nrequire (\n\tgorm.io/gorm v1.31.2\n\texample.com/absent v1.0.0\n)\n",
		"svc/repo.go": "package svc\n\nimport (\n\t\"example.com/absent\"\n\t\"gorm.io/gorm\"\n)\n\n" +
			"func Load(db *gorm.DB) error { return db.Exec(absent.Query).Error }\n",
		"svc/plain.go": "package svc\n\nfunc Plain() {}\n",
	} {
		p := root + "/" + name
		if err := os.MkdirAll(p[:strings.LastIndex(p, "/")], 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	ans := links(t, map[string]any{"tree": root})
	if ans.Cacheable == nil || *ans.Cacheable {
		t.Errorf("want cacheable false, got %v", ans.Cacheable)
	}

	if len(ans.Sets) != 1 || len(ans.Sets[0].CannotTell) != 1 || ans.Sets[0].CannotTell[0].Unit != "go:svc#func:Load" {
		t.Errorf("sets %+v", ans.Sets)
	}
}

func TestLinksAreDeterministic(t *testing.T) {
	first, err := json.Marshal(links(t, nil))
	if err != nil {
		t.Fatal(err)
	}

	for range 3 {
		again, err := json.Marshal(links(t, nil))
		if err != nil {
			t.Fatal(err)
		}

		if string(again) != string(first) {
			t.Fatal("two runs on one tree answered differently")
		}
	}
}

func TestOldToolchain(t *testing.T) {
	root := t.TempDir()

	for name, content := range map[string]string{
		"svc/go.mod":  "module svc\n\ngo 9.99\n\nrequire gorm.io/gorm v1.31.2\n",
		"svc/repo.go": "package svc\n\nimport \"gorm.io/gorm\"\n\nfunc Load(db *gorm.DB) error { return db.Exec(\"SELECT 1\").Error }\n",
	} {
		p := root + "/" + name
		if err := os.MkdirAll(p[:strings.LastIndex(p, "/")], 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("GOTOOLCHAIN", "local")

	ans := links(t, map[string]any{"tree": root})
	if ans.Cacheable == nil || *ans.Cacheable {
		t.Errorf("want cacheable false, got %v", ans.Cacheable)
	}

	if len(ans.Sets) != 1 || len(ans.Sets[0].CannotTell) != 1 || !strings.Contains(ans.Sets[0].CannotTell[0].Reason, "toolchain") {
		t.Errorf("sets %+v", ans.Sets)
	}
}

func claimsIn(t *testing.T, dir string) []plugin.Claim {
	t.Helper()

	var paths []string

	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return fmt.Errorf("walk: %w", err)
		}

		paths = append(paths, filepath.ToSlash(rel))

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	out, err := Detect(context.Background(), request(t, map[string]any{"tree": dir, "paths": paths}), "v0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	ans, _ := out.(plugin.DetectAnswer) //nolint:errcheck // an empty answer fails below

	return ans.Claims
}

func TestNewFileWithoutTheORM(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(tree)); err != nil {
		t.Fatal(err)
	}

	writes := func(ans plugin.LinksAnswer, table string) bool {
		return slices.ContainsFunc(ans.Sets[0].Links, func(l plugin.Link) bool {
			return l.From == "go:app/store#method:Store.AddLanguage" && l.To == "pg:relation:public."+table
		})
	}

	before := links(t, map[string]any{"tree": dir, "claimed": claimsIn(t, dir)})
	if !writes(before, "languages") {
		t.Fatal("AddLanguage doesn't reach languages before the change")
	}

	const added = "app/models/names.go"
	if slices.Contains(before.Sets[0].Inputs, added) {
		t.Fatal("the new file is an input before it exists")
	}

	body := "package models\n\n// TableName names the languages' table.\nfunc (Language) TableName() string { return \"tongues\" }\n"
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(added)), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	claims := claimsIn(t, dir)
	if !slices.ContainsFunc(claims, func(c plugin.Claim) bool { return c.Path == added && c.Role == RoleSource }) {
		t.Fatalf("%s isn't claimed, so pipelinegen would keep the cached set: %+v", added, claims)
	}

	after := links(t, map[string]any{"tree": dir, "claimed": claims})
	if !writes(after, "tongues") || writes(after, "languages") {
		t.Error("AddLanguage doesn't reach tongues alone after the change")
	}

	if !slices.Contains(after.Sets[0].Inputs, added) {
		t.Errorf("inputs %v lack %s", after.Sets[0].Inputs, added)
	}
}

func TestPathsAnswerEverySetThatHoldsThem(t *testing.T) {
	dir := t.TempDir()

	for name, content := range map[string]string{
		"go.work":  "go 1.27\n\nuse (\n\t./b\n\t./z\n)\n",
		"b/go.mod": "module example.com/b\n\ngo 1.27\n\nrequire gorm.io/gorm v1.31.2\n",
		"b/b.go":   "package b\n",
		"z/go.mod": "module example.com/z\n\ngo 1.27\n\nrequire (\n\texample.com/b v0.0.0\n\tgorm.io/gorm v1.31.2\n)\n\nreplace example.com/b => ../b\n",
		"z/z.go":   "package z\n",
		"y/go.mod": "module example.com/y\n\ngo 1.27\n\nrequire gorm.io/gorm v1.31.2\n",
		"y/y.go":   "package y\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	claimed := []plugin.Claim{{Path: "b/go.mod", Role: RoleManifest}, {Path: "y/go.mod", Role: RoleManifest}, {Path: "z/go.mod", Role: RoleManifest}}

	for _, tc := range []struct {
		paths []string
		want  []string
	}{
		{[]string{"b/b.go"}, []string{"b", "z"}},
		{[]string{"z/z.go"}, []string{"z"}},
		{[]string{"go.work"}, []string{"b", "y", "z"}},
		{[]string{"y/y.go"}, []string{"y"}},
	} {
		got, err := modules(plugin.TreeRequest{Tree: dir, Claimed: claimed, Paths: tc.paths}, analysis.Config{})
		if err != nil {
			t.Fatal(err)
		}

		slices.Sort(got)

		if !slices.Equal(got, tc.want) {
			t.Errorf("paths %q answer %q, want %q", tc.paths, got, tc.want)
		}
	}
}
