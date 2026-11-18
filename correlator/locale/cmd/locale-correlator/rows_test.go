package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen-sbom-locale/catalog"

	"github.com/sideways-works/pipelinegen-plugins/correlator/locale/observe"
)

// rowProbes are the records each slice of testdata/rows leaves, as i18next 26
// makes them; TestRowProbesAreI18nexts checks the vitest ones against a real
// run.
var rowProbes = map[string][]string{
	"title-de": {
		`{"f":"app/locales/de/home.json","has":true}`,
		`{"f":"app/locales/de/home.json","k":"title"}`,
	},
	"greeting-de": {
		`{"f":"app/locales/de/home.json","has":true}`,
		`{"f":"app/locales/de/home.json","k":"greeting"}`,
		`{"f":"app/locales/en/home.json","k":"greeting"}`,
	},
	"greeting-male-de": {
		`{"f":"app/locales/de/home.json","has":true}`,
		`{"f":"app/locales/de/home.json","k":"greeting_male"}`,
		`{"f":"app/locales/de/home.json","k":"greeting"}`,
		`{"f":"app/locales/en/home.json","k":"greeting_male"}`,
	},
	"one-en": {
		`{"f":"app/locales/en/home.json","has":true}`,
		`{"f":"app/locales/en/home.json","k":"cart.items_one"}`,
	},
	"zero-en": {
		`{"f":"app/locales/en/home.json","has":true}`,
		`{"f":"app/locales/en/home.json","k":"cart.items_zero"}`,
		`{"f":"app/locales/en/home.json","k":"cart.items_other"}`,
	},
	"title-fr": {
		`{"c":"app/locales","load":true}`,
		`{"c":"app/locales","l":"fr","n":"home","has":true}`,
		`{"c":"app/locales","l":"fr","n":"home","k":"title"}`,
		`{"c":"app/locales","l":"en","n":"home","k":"title"}`,
	},
	"nothing": {
		`{"f":"app/locales/en/home.json","has":true}`,
	},
}

func treeUnits(t *testing.T, root string) map[string]string {
	t.Helper()

	out := map[string]string{}

	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		rel, err := filepath.Rel(root, p)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}

		f, ok := catalog.Locate(filepath.ToSlash(rel))
		if !ok {
			return nil
		}

		b, err := os.ReadFile(p) //nolint:gosec // the test's own tree
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}

		top, err := catalog.Parse(b)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}

		out[catalog.ID(f.Catalog)] = catalog.PresentDigest()
		out[catalog.LangID(f.Catalog, f.Lang)] = catalog.PresentDigest()
		out[catalog.NSID(f.Catalog, f.Lang, f.NS)] = catalog.PresentDigest()

		for _, n := range catalog.Nodes(top) {
			out[catalog.KeyID(f.Catalog, f.Lang, f.NS, n.Key)] = n.Digest
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return out
}

func copyTree(t *testing.T, from string) string {
	t.Helper()

	to := t.TempDir()

	err := filepath.WalkDir(from, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		rel, err := filepath.Rel(from, p)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}

		b, err := os.ReadFile(p) //nolint:gosec // the test's own tree
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}

		if err := os.MkdirAll(filepath.Join(to, filepath.Dir(rel)), 0o750); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}

		if err := os.WriteFile(filepath.Join(to, rel), b, 0o600); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return to
}

// selected is the rule the core applies to a slice that holds a
// catalog's own unit: the slice runs when a unit it touched changed,
// appeared or went.
func selected(before, after map[string]string, touched map[string][]string) []string {
	var out []string

	for slice, ids := range touched {
		for _, id := range ids {
			old, was := before[id]
			now, is := after[id]

			if was != is || old != now {
				out = append(out, slice)

				break
			}
		}
	}

	slices.Sort(out)

	return out
}

// The table in the README: which slices run for each kind of catalog change.
func TestWhichTestsSeeAChange(t *testing.T) {
	base := filepath.Join("..", "..", "testdata", "rows")
	rec := t.TempDir()
	touched := map[string][]string{}
	claimed := []string{"app/locales/en/home.json", "app/locales/de/home.json"}

	for slice, lines := range rowProbes {
		dir := filepath.Join(rec, slice)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}

		text := `{"recorder":"locale-keys","version":1,"runtime":"vitest"}` + "\n" + strings.Join(lines, "\n") + "\n" + `{"end":true}` + "\n"
		if err := os.WriteFile(filepath.Join(dir, "locale-keys.vitest-1-1.jsonl"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}

		ans, err := observe.Observe([]string{filepath.Join(rec, "_setup"), dir}, observe.NewCatalogs(claimed))
		if err != nil {
			t.Fatalf("%s: %v", slice, err)
		}

		touched[slice] = ans.Touched
	}

	en, de, fr := "app/locales/en/home.json", "app/locales/de/home.json", "app/locales/fr/home.json"

	rows := []struct {
		row  string
		path string
		text string
		want []string
	}{
		{"layout, key order, escapes: nothing", en,
			`{"greeting_male":"Hello, sir","greeting":"Hello","title":"Welcome","cart":{"items_other":"{{count}} tickets","items_one":"{{count}} ticket"}}`,
			nil},
		{"a changed value: the tests that rendered it in that language", de,
			`{"title": "Willkommen!", "cart": {"items_one": "{{count}} Ticket", "items_other": "{{count}} Tickets"}}`,
			[]string{"title-de"}},
		{"a changed value: fallback included", en,
			`{"title": "Welcome", "greeting": "Hi", "greeting_male": "Hello, sir", "cart": {"items_one": "{{count}} ticket", "items_other": "{{count}} tickets"}}`,
			[]string{"greeting-de"}},
		{"a key added that nothing looks up: none", en,
			`{"title": "Welcome", "intro": "Hi", "greeting": "Hello", "greeting_male": "Hello, sir", "cart": {"items_one": "{{count}} ticket", "items_other": "{{count}} tickets"}}`,
			nil},
		{"a key added where a probe missed it: those slices", de,
			`{"title": "Willkommen", "greeting": "Hallo", "cart": {"items_one": "{{count}} Ticket", "items_other": "{{count}} Tickets"}}`,
			[]string{"greeting-de", "greeting-male-de"}},
		{"a key removed: the tests that looked it up", en,
			`{"title": "Welcome", "greeting": "Hello", "greeting_male": "Hello, sir", "cart": {"items_other": "{{count}} tickets"}}`,
			[]string{"one-en"}},
		{"a plural variant added: the tests whose probe missed it", en,
			`{"title": "Welcome", "greeting": "Hello", "greeting_male": "Hello, sir", "cart": {"items_zero": "No tickets", "items_one": "{{count}} ticket", "items_other": "{{count}} tickets"}}`,
			[]string{"zero-en"}},
		{"a context variant added: the tests whose probe missed it", de,
			`{"title": "Willkommen", "greeting_male": "Hallo, Herr", "cart": {"items_one": "{{count}} Ticket", "items_other": "{{count}} Tickets"}}`,
			[]string{"greeting-male-de"}},
		{"a context variant removed: the tests that looked it up", en,
			`{"title": "Welcome", "greeting": "Hello", "cart": {"items_one": "{{count}} ticket", "items_other": "{{count}} tickets"}}`,
			[]string{"greeting-male-de"}},
		{"a whole language added: the tests that ran in it", fr,
			`{"title": "Bienvenue"}`,
			[]string{"title-fr"}},
	}

	for _, r := range rows {
		t.Run(r.row, func(t *testing.T) {
			tree := copyTree(t, base)
			before := treeUnits(t, tree)

			if err := os.MkdirAll(filepath.Join(tree, filepath.Dir(r.path)), 0o750); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(filepath.Join(tree, r.path), []byte(r.text), 0o600); err != nil {
				t.Fatal(err)
			}

			got := selected(before, treeUnits(t, tree), touched)
			if !slices.Equal(got, r.want) {
				t.Errorf("selected %v, want %v (changed units: %v)", got, r.want, changedIDs(before, treeUnits(t, tree)))
			}
		})
	}
}

func changedIDs(before, after map[string]string) []string {
	var out []string

	for _, id := range slices.Sorted(maps.Keys(after)) {
		if before[id] != after[id] {
			out = append(out, id)
		}
	}

	for id := range before {
		if _, ok := after[id]; !ok {
			out = append(out, id+" (gone)")
		}
	}

	return out
}
