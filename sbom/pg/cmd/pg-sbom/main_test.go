package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func call(t *testing.T, name, req string) (int, string, string) {
	t.Helper()

	var out, errs bytes.Buffer

	code := run([]string{name}, strings.NewReader(req), &out, &errs)

	return code, out.String(), errs.String()
}

func TestDescribe(t *testing.T) {
	code, out, _ := call(t, "describe", "")
	if code != 0 || !strings.Contains(out, `"kind":"sbom"`) {
		t.Fatalf("exit %d: %s", code, out)
	}
}

type answer struct {
	Sets []struct {
		Group  string   `json:"group"`
		Inputs []string `json:"inputs"`
		Units  []struct {
			ID         string   `json:"id"`
			Group      string   `json:"group"`
			Digest     string   `json:"digest"`
			DigestFrom []string `json:"digest_from"`
			File       string   `json:"file"`
		} `json:"units"`
		Links []struct {
			From      string `json:"from"`
			To        string `json:"to"`
			Kind      string `json:"kind"`
			Narrowing string `json:"narrowing"`
			While     string `json:"while"`
		} `json:"links"`
	} `json:"sets"`
}

func TestUnitsOfATree(t *testing.T) {
	root, err := filepath.Abs("../../testdata/notes")
	if err != nil {
		t.Fatal(err)
	}

	code, out, errs := call(t, "units", `{"protocol":1,"tree":"`+root+`"}`)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}

	var a answer
	if err := json.Unmarshal([]byte(out), &a); err != nil {
		t.Fatal(err)
	}

	if len(a.Sets) != 1 {
		t.Fatalf("%d sets", len(a.Sets))
	}

	s := a.Sets[0]
	if s.Group != "db/migrations" || len(s.Units) == 0 || len(s.Links) == 0 || len(s.Inputs) != 6 {
		t.Fatalf("group %s, units %d, links %d, inputs %q", s.Group, len(s.Units), len(s.Links), s.Inputs)
	}

	for _, u := range s.Units {
		if u.ID == "pg:table:public.notes" && (u.Digest == "" || u.File != "db/migrations/000001_init.up.sql" || u.Group != "db/migrations") {
			t.Errorf("notes: %+v", u)
		}

		if u.ID == "pg:relation:public.notes" && (u.Digest != "" || len(u.DigestFrom) != 1) {
			t.Errorf("the alias: %+v", u)
		}
	}

	tagged := false

	for _, l := range s.Links {
		tagged = tagged || l.Narrowing == "rows-to-readers" && l.While == "off"
	}

	if !tagged {
		t.Error("no link tagged with its narrowing")
	}
}

func TestDetect(t *testing.T) {
	root, err := filepath.Abs("../../testdata/notes")
	if err != nil {
		t.Fatal(err)
	}

	code, out, errs := call(t, "detect", `{"protocol":1,"tree":"`+root+`","paths":["db/migrations/000001_init.up.sql","db/migrations/000001_init.down.sql","db/queries/notes.sql","gone.sql"]}`)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}

	want := `{"claims":[{"path":"db/migrations/000001_init.up.sql","role":"migration","by_units":"migrations-by-table"},{"path":"db/migrations/000001_init.down.sql","role":"migration"}]}`
	if strings.TrimSpace(out) != want {
		t.Fatalf("got %s", out)
	}
}

func TestUnitsOfTheClaimedFilesForAPath(t *testing.T) {
	root, err := filepath.Abs("../../testdata/layouts")
	if err != nil {
		t.Fatal(err)
	}

	req := `{"protocol":1,"tree":"` + root + `","paths":["plain/drizzle/0001_second.sql"],
	  "claimed":[{"path":"plain/drizzle/0000_first.sql","role":"migration"},{"path":"plain/drizzle/0001_second.sql","role":"migration"},
	             {"path":"flyway/sql/V1__notes.sql","role":"migration"}]}`

	code, out, errs := call(t, "units", req)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}

	var a answer
	if err := json.Unmarshal([]byte(out), &a); err != nil {
		t.Fatal(err)
	}

	// both directories in one set: two databases may name the same table
	if len(a.Sets) != 1 || a.Sets[0].Group != "flyway/sql + plain/drizzle" || len(a.Sets[0].Units) != 4 {
		t.Fatalf("got %s", out)
	}

	code, out, _ = call(t, "units", strings.Replace(req, `"paths":["plain/drizzle/0001_second.sql"]`, `"paths":["elsewhere/notes.sql"]`, 1))
	if code != 0 || strings.Contains(out, `"group"`) {
		t.Fatalf("a path in no set: exit %d, %s", code, out)
	}
}

func TestConfiguredDirectory(t *testing.T) {
	root, err := filepath.Abs("../../testdata/layouts")
	if err != nil {
		t.Fatal(err)
	}

	code, out, errs := call(t, "units", `{"protocol":1,"tree":"`+root+`","config":{"migrations":[{"dir":"flyway/sql","layout":"flyway"}]}}`)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}

	if !strings.Contains(out, `"pg:view:public.note_titles"`) || strings.Contains(out, `"group":"plain/drizzle"`) {
		t.Fatalf("got %s", out)
	}

	if code, _, _ := call(t, "units", `{"protocol":1,"tree":"`+root+`","config":{"migrations":[{"dir":"flyway/sql","layout":"pairs"}]}}`); code != 1 {
		t.Errorf("a wrong layout: exit %d", code)
	}

	if code, _, _ := call(t, "units", `{"protocol":1,"tree":"`+root+`","config":{"migration":[]}}`); code != 1 {
		t.Errorf("a misspelt key: exit %d", code)
	}
}

func TestNoMigrationsCannotTell(t *testing.T) {
	if code, _, _ := call(t, "units", `{"protocol":1,"tree":"`+t.TempDir()+`"}`); code != plugin.ExitCantTell {
		t.Fatalf("exit %d", code)
	}
}

func TestUnitsCarryLabels(t *testing.T) {
	root, err := filepath.Abs("../../testdata/notes")
	if err != nil {
		t.Fatal(err)
	}

	req := `{"protocol":1,"tree":"` + root + `"}`

	_, out, _ := call(t, "units", req)

	var a struct {
		Sets []struct {
			Units []struct {
				ID    string `json:"id"`
				Label string `json:"label"`
			} `json:"units"`
		} `json:"sets"`
	}
	if err := json.Unmarshal([]byte(out), &a); err != nil {
		t.Fatal(err)
	}

	for _, u := range a.Sets[0].Units {
		if u.Label == "" || len([]rune(u.Label)) > 80 || strings.Contains(u.Label, root) {
			t.Errorf("%s: label %q", u.ID, u.Label)
		}

		if u.ID == "pg:table:public.notes" && u.Label != "the table notes" {
			t.Errorf("notes: label %q", u.Label)
		}
	}

	defer func(f func(string) string) { labelOf = f }(labelOf)

	labelOf = func(string) string { return "x" }
	_, other, _ := call(t, "units", req)

	strip := regexp.MustCompile(`"label":"[^"]*",?`)
	if strip.ReplaceAllString(out, "") != strip.ReplaceAllString(other, "") {
		t.Error("an answer changes with its labels")
	}
}
