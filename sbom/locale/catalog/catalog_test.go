package catalog

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLocate(t *testing.T) {
	cases := []struct {
		path string
		want File
		ok   bool
	}{
		{"apps/web/src/locales/de/home.json", File{Catalog: "apps/web/src/locales", Lang: "de", NS: "home"}, true},
		{"backend/mail/locales/de.json", File{Catalog: "backend/mail/locales", Lang: "de", NS: NoNS}, true},
		{"i18n/pt-BR/common.json", File{Catalog: "i18n", Lang: "pt-BR", NS: "common"}, true},
		{"web/locales/zh_Hant/app.json", File{Catalog: "web/locales", Lang: "zh_Hant", NS: "app"}, true},
		{"web/locales/es-419.json", File{Catalog: "web/locales", Lang: "es-419", NS: NoNS}, true},
		{"web/lang/translations/fil.json", File{Catalog: "web/lang/translations", Lang: "fil", NS: NoNS}, true},
		{"web/locales/app/de/home.json", File{Catalog: "web/locales/app", Lang: "de", NS: "home"}, true},
		{"backend/mail/locales/shell.json", File{}, false},
		{"web/locales/ui/home.json", File{}, false},
		{"web/locales/api.json", File{}, false},
		{"web/data/de/home.json", File{}, false},
		{"de/home.json", File{}, false},
		{"web/locales/de/home.yaml", File{}, false},
		// never: outside the tree, installed packages, secrets
		{"../locales/de.json", File{}, false},
		{"/abs/locales/de.json", File{}, false},
		{"node_modules/x/locales/de.json", File{}, false},
		{"web/locales/secrets/de.json", File{}, false},
		{"web/locales/.env.json", File{}, false},
		{"web/locales/de/token.json", File{}, false},
	}

	for _, c := range cases {
		got, ok := Locate(c.path)
		if ok != c.ok {
			t.Errorf("Locate(%q) ok = %v, want %v", c.path, ok, c.ok)

			continue
		}

		if ok {
			c.want.Path = c.path
			if got != c.want {
				t.Errorf("Locate(%q) = %+v, want %+v", c.path, got, c.want)
			}
		}
	}
}

func TestIDs(t *testing.T) {
	cat := "apps/web/src/locales"

	for got, want := range map[string]string{
		ID(cat):                                "locale:apps/web/src/locales",
		LangID(cat, "de"):                      "locale:apps/web/src/locales#de",
		NSID(cat, "de", "home"):                "locale:apps/web/src/locales#de/home",
		KeyID(cat, "de", "home", "a.b_one"):    "locale:apps/web/src/locales#de/home:a.b_one",
		KeyID(cat, "de", "home", ""):           "locale:apps/web/src/locales#de/home:",
		Group(cat, "de", "home", "a.b.c"):      "apps/web/src/locales#de/home:a",
		KeyID("mail/locales", "de", NoNS, "x"): "locale:mail/locales#de/-:x",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func nodes(t *testing.T, src string) map[string]Node {
	t.Helper()

	top, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse(%s): %v", src, err)
	}

	out := map[string]Node{}
	for _, n := range Nodes(top) {
		out[n.Key] = n
	}

	return out
}

func TestDigestIsMeaning(t *testing.T) {
	base := nodes(t, `{"a": {"title": "Café", "body": "x"}, "b": ["one", "two"]}`)

	for _, same := range []string{
		"{\n  \"b\": [\"one\", \"two\"],\n  \"a\": {\n    \"body\": \"x\",\n    \"title\": \"Caf\\u00e9\"\n  }\n}\n",
		`{"a":{"body":"x","title":"Café"},"b":["one","two"]}`,
		"\t{ \"a\" : { \"title\" : \"Caf\\u00E9\" , \"body\" : \"\\u0078\" } , \"b\" : [ \"one\" , \"two\" ] }\r\n",
	} {
		other := nodes(t, same)
		if len(other) != len(base) {
			t.Fatalf("%d keys, want %d", len(other), len(base))
		}

		for k, n := range base {
			if other[k].Digest != n.Digest {
				t.Errorf("%q: digest changed for %s", k, same)
			}
		}
	}
}

func TestDigestChangedValue(t *testing.T) {
	a := nodes(t, `{"a": {"title": "Hello", "body": "x"}, "b": "y"}`)
	b := nodes(t, `{"a": {"title": "Hello!", "body": "x"}, "b": "y"}`)

	for k := range a {
		changed := a[k].Digest != b[k].Digest
		want := k == "a.title" || k == "a" || k == ""

		if changed != want {
			t.Errorf("%q changed = %v, want %v", k, changed, want)
		}
	}
}

func TestNodesKinds(t *testing.T) {
	n := nodes(t, `{"a": {"x_one": "1 thing", "x_other": "{{count}} things"}, "lines": ["l0", "l1"]}`)

	for k, leaf := range map[string]bool{"": false, "a": false, "a.x_one": true, "a.x_other": true, "lines": false, "lines.0": true, "lines.1": true} {
		got, ok := n[k]
		if !ok {
			t.Errorf("no node %q", k)

			continue
		}

		if got.Leaf != leaf {
			t.Errorf("%q leaf = %v, want %v", k, got.Leaf, leaf)
		}
	}

	if len(n) != 7 {
		t.Errorf("%d nodes, want 7", len(n))
	}
}

func TestFlatAndNestedMerge(t *testing.T) {
	flat := nodes(t, `{"a.b": "x"}`)
	nested := nodes(t, `{"a": {"b": "x"}}`)

	if flat["a.b"].Digest != nested["a.b"].Digest {
		t.Errorf("a flat and a nested key with one value differ")
	}

	both := nodes(t, `{"a.b": "x", "a": {"b": "y"}}`)
	if both["a.b"].Leaf || both["a.b"].Digest == flat["a.b"].Digest {
		t.Errorf("both spellings: %+v", both["a.b"])
	}
}

func TestParseRefuses(t *testing.T) {
	for _, src := range []string{`{"a": 1}`, `{"a": {"b": true}}`, `{"a": null}`, `["a"]`, `"a"`, `{"a": ["x", 2]}`} {
		if _, err := Parse([]byte(src)); !errors.Is(err, ErrNotCatalog) {
			t.Errorf("Parse(%s) = %v, want not a catalog", src, err)
		}
	}

	for _, src := range []string{`{"a": "x"`, `{"a": "x"} {}`, ``, `{a: "x"}`} {
		_, err := Parse([]byte(src))
		if err == nil || errors.Is(err, ErrNotCatalog) {
			t.Errorf("Parse(%q) = %v, want a syntax error", src, err)
		}
	}
}

func TestClaimable(t *testing.T) {
	tree := t.TempDir()

	write := func(p, s string) {
		full := filepath.Join(tree, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(full, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write("web/locales/en/home.json", `{"a": "x"}`)
	write("web/locales/en/empty.json", `{}`)
	write("web/locales/en/config.json", `{"port": 8080}`)
	write("web/locales/de/broken.json", `{"a": `)
	write("web/data/en/home.json", `{"a": "x"}`)
	write("web/locales/secrets/en.json", `{"a": "x"}`)

	if err := os.Symlink("home.json", filepath.Join(tree, "web/locales/en/link.json")); err != nil {
		t.Fatal(err)
	}

	for p, want := range map[string]bool{
		"web/locales/en/home.json":    true,
		"web/locales/en/empty.json":   true,
		"web/locales/en/config.json":  false, // JSON that is no catalog
		"web/locales/de/broken.json":  true,  // no JSON: claimed, and its units can't tell
		"web/data/en/home.json":       false,
		"web/locales/secrets/en.json": false,
		"web/locales/en/link.json":    false,
		"web/locales/en/gone.json":    false,
		"../web/locales/en/home.json": false,
	} {
		got, err := Claimable(tree, p)
		if err != nil {
			t.Fatalf("Claimable(%q): %v", p, err)
		}

		if got != want {
			t.Errorf("Claimable(%q) = %v, want %v", p, got, want)
		}
	}
}
