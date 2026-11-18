package correlate

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func configFrom(t *testing.T, s string) Config {
	t.Helper()

	var c Config
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		t.Fatal(err)
	}

	return c
}

func every(t *testing.T, cfg Config) []Link {
	t.Helper()

	m, err := Load(repo, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	hl, err := m.HandlerLinks(repo)
	if err != nil {
		t.Fatal(err)
	}

	mr, err := m.MapReaders(repo)
	if err != nil {
		t.Fatal(err)
	}

	out := slices.Concat(m.CodeLinks(), hl, mr)
	for i := range out {
		out[i].Note = ""
	}

	slices.SortFunc(out, func(a, b Link) int {
		for _, c := range [][2]string{{a.From, b.From}, {a.To, b.To}, {a.Kind, b.Kind}, {a.While, b.While}} {
			if c[0] != c[1] {
				if c[0] < c[1] {
					return -1
				}

				return 1
			}
		}

		return 0
	})

	return out
}

func ofKind(links []Link, kinds ...string) []Link {
	var out []Link

	for _, l := range links {
		if slices.Contains(kinds, l.Kind) {
			out = append(out, l)
		}
	}

	return out
}

func TestClassifyOnlyConfigKeepsDetection(t *testing.T) {
	detected := every(t, Config{})
	classified := every(t, configFrom(t, `{"documents": [{"path": "api/notes.yaml", "go": {
		"readers": ["go:internal/rules#func:Count"],
		"per_operation": ["go:internal/server#func:Middleware"]}}]}`))

	for _, kind := range []string{LinkServes, LinkImplements, LinkEmbeds} {
		if a, b := ofKind(detected, kind), ofKind(classified, kind); len(a) == 0 || !slices.Equal(a, b) {
			t.Errorf("%s links: detection alone %d, with the classify-only config %d", kind, len(a), len(b))
		}
	}

	var reads []Link

	for _, l := range classified {
		if l.Kind == LinkReads && l.From == "go:internal/rules#func:Count" {
			reads = append(reads, l)
		}
	}

	if len(reads) == 0 {
		t.Error("the listed reader is not linked")
	}

	// Beyond the reader's links the two answers agree link for link.
	rest := slices.DeleteFunc(slices.Clone(classified), func(l Link) bool {
		return l.Kind == LinkReads && l.From == "go:internal/rules#func:Count"
	})
	if !slices.Equal(rest, detected) {
		t.Errorf("without the reader's links, %d links, detection alone %d", len(rest), len(detected))
	}
}

func TestConfigOverridesOnlyWhatItSets(t *testing.T) {
	detected := every(t, Config{})

	cases := []struct {
		name, goBlock string
		gone          func(Link) bool
	}{
		{"strict off", `{"strict": false}`, func(l Link) bool {
			return l.Kind == LinkServes && strings.Contains(l.From, "#method:strictHandler.")
		}},
		{"no document variables", `{"spec_vars": []}`, func(l Link) bool { return l.Kind == LinkEmbeds }},
		{"handlers elsewhere", `{"handlers": ["internal/access"]}`, func(l Link) bool { return l.Kind == LinkImplements }},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := every(t, configFrom(t, `{"documents": [{"path": "api/notes.yaml", "go": `+c.goBlock+`}]}`))
			want := slices.DeleteFunc(slices.Clone(detected), c.gone)

			if len(want) == len(detected) {
				t.Fatal("the case removes nothing from what detection found")
			}

			if !slices.Equal(got, want) {
				t.Errorf("%d links, want %d: detection's without the overridden key's", len(got), len(want))
			}
		})
	}

	t.Run("package", func(t *testing.T) {
		got := every(t, configFrom(t, `{"documents": [{"path": "api/notes.yaml", "go": {"package": "gen/notesgen"}}]}`))
		if !slices.Equal(got, detected) {
			t.Errorf("naming the detected package changed the links: %d, detection alone %d", len(got), len(detected))
		}
	})
}
