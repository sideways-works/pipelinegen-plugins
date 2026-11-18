package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

var update = flag.Bool("update", false, "rewrite the golden answers")

func call(t *testing.T, name string, req any) (int, []byte, string) {
	t.Helper()

	in, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer

	code := run([]string{name}, bytes.NewReader(in), &out, &errOut)

	return code, out.Bytes(), errOut.String()
}

func tree(t *testing.T, files map[string]string) string {
	t.Helper()

	dir := t.TempDir()

	for p, text := range files {
		write(t, dir, p, text)
	}

	return dir
}

func write(t *testing.T, root, p, text string) {
	t.Helper()

	full := filepath.Join(root, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(full, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func detected(t *testing.T, root string, paths ...string) []plugin.Claim {
	t.Helper()

	code, out, stderr := call(t, plugin.CallDetect, plugin.DetectRequest{Protocol: 1, Tree: root, Paths: paths})
	if code != 0 {
		t.Fatalf("detect exited %d: %s", code, stderr)
	}

	var ans plugin.DetectAnswer
	if err := json.Unmarshal(out, &ans); err != nil {
		t.Fatal(err)
	}

	return ans.Claims
}

func unitsOf(t *testing.T, root string, claims []plugin.Claim, paths ...string) plugin.UnitsAnswer {
	t.Helper()

	code, out, stderr := call(t, plugin.CallUnits, plugin.TreeRequest{Protocol: 1, Tree: root, Claimed: claims, Paths: paths})
	if code != 0 {
		t.Fatalf("units exited %d: %s", code, stderr)
	}

	var ans plugin.UnitsAnswer
	if err := json.Unmarshal(out, &ans); err != nil {
		t.Fatal(err)
	}

	return ans
}

func ids(ans plugin.UnitsAnswer) map[string]string {
	out := map[string]string{}

	for _, s := range ans.Sets {
		for _, u := range s.Units {
			out[u.ID] = u.Digest
		}
	}

	return out
}

var sample = map[string]string{ //nolint:gosec // fixture texts, no credentials
	"web/locales/en/home.json":    `{"title": "Welcome", "cart": {"items_one": "{{count}} ticket", "items_other": "{{count}} tickets"}}`,
	"web/locales/de/home.json":    `{"title": "Willkommen"}`,
	"web/locales/en/config.json":  `{"port": 8080}`,
	"web/locales/secrets/en.json": `{"a": "x"}`,
	"web/locales/.env.json":       `{"a": "x"}`,
	"mail/locales/en.json":        `{"booking": {"subject": "Your booking", "lines": ["Hello", "Bye"]}}`,
	"mail/locales/shell.json":     `{"footer": "x"}`,
	"web/data/en/home.json":       `{"a": "x"}`,
}

func samplePaths() []string {
	var out []string
	for p := range sample {
		out = append(out, p)
	}

	slices.Sort(out)

	return out
}

func TestDescribe(t *testing.T) {
	d := describe()
	if d.Kind != plugin.KindSBOM || len(d.Narrowings) != 1 || d.Narrowings[0].Name != "locale-keys" || d.Narrowings[0].OffAt != 3 {
		t.Errorf("describe: %+v", d)
	}
}

func TestDetectClaimsCatalogsOnly(t *testing.T) {
	root := tree(t, sample)

	var got []string

	for _, c := range detected(t, root, samplePaths()...) {
		if c.Role != roleCatalog || c.ByUnits != "locale-keys" {
			t.Errorf("claim %+v", c)
		}

		got = append(got, c.Path+" "+c.Anchor)
	}

	slices.Sort(got)

	want := []string{
		"mail/locales/en.json locale:mail/locales",
		"web/locales/de/home.json locale:web/locales",
		"web/locales/en/home.json locale:web/locales",
	}
	if !slices.Equal(got, want) {
		t.Errorf("claims %v, want %v", got, want)
	}
}

func TestDetectRefusesPathsOutsideTheTree(t *testing.T) {
	root := tree(t, sample)
	if got := detected(t, root, "../web/locales/en/home.json", "/etc/locales/en.json"); len(got) != 0 {
		t.Errorf("claims %v", got)
	}
}

func TestUnitsGolden(t *testing.T) {
	root := tree(t, sample)
	ans := unitsOf(t, root, detected(t, root, samplePaths()...))

	got, err := json.MarshalIndent(ans, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	golden := filepath.Join("testdata", "golden", "units.json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(golden, append(got, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(bytes.TrimSpace(want), got) {
		t.Errorf("units differ from %s (go test -update rewrites it):\n%s", golden, got)
	}
}

func TestUnitsAreTheSameBytesEveryTime(t *testing.T) {
	root := tree(t, sample)
	claims := detected(t, root, samplePaths()...)

	req := plugin.TreeRequest{Protocol: 1, Tree: root, Claimed: claims}

	_, a, _ := call(t, plugin.CallUnits, req)
	for range 5 {
		if _, b, _ := call(t, plugin.CallUnits, req); !bytes.Equal(a, b) {
			t.Fatal("two answers differ")
		}
	}
}

func TestEveryUnitIsInTheNamespace(t *testing.T) {
	root := tree(t, sample)
	for id := range ids(unitsOf(t, root, detected(t, root, samplePaths()...))) {
		if !strings.HasPrefix(id, "locale:") || strings.Contains(id, "secrets") || strings.Contains(id, ".env") || strings.Contains(id, "config") {
			t.Errorf("unit %s", id)
		}
	}
}

// The rows of the README's table of what each kind of change does.
func TestWhatEachChangeDoesToTheUnits(t *testing.T) {
	base := `{"title": "Welcome", "cart": {"items_one": "{{count}} ticket", "items_other": "{{count}} tickets"}}`
	cases := []struct {
		name    string
		path    string
		text    string // "" removes the file
		changed []string
		added   []string
		removed []string
	}{
		{name: "layout, key order, escapes, a trailing newline", path: "web/locales/en/home.json",
			text: "{\n  \"cart\": {\"items_other\": \"{{count}} tickets\", \"items_one\": \"{{count}} ticket\"},\n  \"title\": \"Welc\\u006fme\"\n}\n"},
		{name: "a changed value", path: "web/locales/en/home.json",
			text:    `{"title": "Hello", "cart": {"items_one": "{{count}} ticket", "items_other": "{{count}} tickets"}}`,
			changed: []string{"#en/home:title", "#en/home:"}},
		{name: "a key added", path: "web/locales/en/home.json",
			text:    `{"title": "Welcome", "intro": "Hi", "cart": {"items_one": "{{count}} ticket", "items_other": "{{count}} tickets"}}`,
			changed: []string{"#en/home:"}, added: []string{"#en/home:intro"}},
		{name: "a key removed", path: "web/locales/en/home.json",
			text:    `{"cart": {"items_one": "{{count}} ticket", "items_other": "{{count}} tickets"}}`,
			changed: []string{"#en/home:"}, removed: []string{"#en/home:title"}},
		{name: "a plural variant added", path: "web/locales/en/home.json",
			text:    `{"title": "Welcome", "cart": {"items_zero": "No tickets", "items_one": "{{count}} ticket", "items_other": "{{count}} tickets"}}`,
			changed: []string{"#en/home:cart", "#en/home:"}, added: []string{"#en/home:cart.items_zero"}},
		{name: "a whole language added", path: "web/locales/fr/home.json", text: `{"title": "Bienvenue"}`,
			added: []string{"#fr", "#fr/home", "#fr/home:", "#fr/home:title"}},
		{name: "a whole language removed", path: "web/locales/de/home.json",
			removed: []string{"#de", "#de/home", "#de/home:", "#de/home:title"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{"web/locales/en/home.json": base, "web/locales/de/home.json": `{"title": "Willkommen"}`}
			root := tree(t, files)
			paths := []string{"web/locales/en/home.json", "web/locales/de/home.json", "web/locales/fr/home.json"}
			before := ids(unitsOf(t, root, detected(t, root, paths...)))

			if c.text == "" {
				if err := os.Remove(filepath.Join(root, filepath.FromSlash(c.path))); err != nil {
					t.Fatal(err)
				}
			} else {
				write(t, root, c.path, c.text)
			}

			after := ids(unitsOf(t, root, detected(t, root, paths...)))

			var changed, added, removed []string

			for id, d := range after {
				short := strings.TrimPrefix(id, "locale:web/locales")
				if old, ok := before[id]; !ok {
					added = append(added, short)
				} else if old != d {
					changed = append(changed, short)
				}
			}

			for id := range before {
				if _, ok := after[id]; !ok {
					removed = append(removed, strings.TrimPrefix(id, "locale:web/locales"))
				}
			}

			for _, l := range [][]string{changed, added, removed, c.changed, c.added, c.removed} {
				slices.Sort(l)
			}

			if !slices.Equal(changed, c.changed) || !slices.Equal(added, c.added) || !slices.Equal(removed, c.removed) {
				t.Errorf("changed %v added %v removed %v, want %v %v %v", changed, added, removed, c.changed, c.added, c.removed)
			}
		})
	}
}

func TestAnswerForAPathHoldsItsFileAndCatalog(t *testing.T) {
	root := tree(t, sample)
	part := ids(unitsOf(t, root, detected(t, root, samplePaths()...), "web/locales/de/home.json"))

	for _, id := range []string{"locale:web/locales", "locale:web/locales#de", "locale:web/locales#en", "locale:web/locales#de/home", "locale:web/locales#de/home:title"} {
		if _, ok := part[id]; !ok {
			t.Errorf("no %s in %v", id, part)
		}
	}

	if _, ok := part["locale:web/locales#en/home:title"]; ok {
		t.Error("another file's keys in the answer")
	}
}

func TestDeletedFileAnswersAnEmptySet(t *testing.T) {
	root := tree(t, sample)
	ans := unitsOf(t, root, detected(t, root, samplePaths()...), "web/locales/fr/home.json")

	var empty bool

	for _, s := range ans.Sets {
		if slices.Contains(s.Absent, "web/locales/fr/home.json") && len(s.Units) == 0 {
			empty = true
		}
	}

	if !empty {
		t.Errorf("no empty set for the deleted file: %+v", ans.Sets)
	}
}

func TestUnreadableCatalogCantTell(t *testing.T) {
	root := tree(t, map[string]string{"web/locales/en/home.json": `{"title": `})

	claims := detected(t, root, "web/locales/en/home.json")
	if len(claims) != 1 {
		t.Fatalf("claims %v", claims)
	}

	code, _, stderr := call(t, plugin.CallUnits, plugin.TreeRequest{Protocol: 1, Tree: root, Claimed: claims})
	if code != plugin.ExitCantTell || !strings.Contains(stderr, "web/locales/en/home.json") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}

func TestLargeAnswerGoesToAFile(t *testing.T) {
	var b strings.Builder

	b.WriteString("{")

	for i := range 3000 {
		if i > 0 {
			b.WriteString(",")
		}

		b.WriteString(`"k` + strconv.Itoa(i) + `": "v"`)
	}

	b.WriteString("}")

	root := tree(t, map[string]string{"web/locales/en/home.json": b.String()})
	work := t.TempDir()
	claims := detected(t, root, "web/locales/en/home.json")

	code, out, stderr := call(t, plugin.CallUnits, plugin.TreeRequest{Protocol: 1, Work: work, Tree: root, Claimed: claims})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	var ans plugin.UnitsAnswer
	if err := json.Unmarshal(out, &ans); err != nil {
		t.Fatal(err)
	}

	sets, err := plugin.ReadLines(work, ans.Files, []plugin.Set(nil))
	if err != nil || len(sets) != 2 {
		t.Fatalf("sets %d, %v (files %v)", len(sets), err, ans.Files)
	}
}

func TestKit(t *testing.T) {
	kit := os.Getenv("PIPELINEGEN")
	if kit == "" {
		t.Skip("PIPELINEGEN names no pipelinegen binary")
	}

	bin := filepath.Join(t.TempDir(), "locale-sbom")

	build := exec.CommandContext(t.Context(), "go", "build", "-trimpath", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	tree, err := filepath.Abs(filepath.Join("..", "..", "testdata", "kit"))
	if err != nil {
		t.Fatal(err)
	}

	out, err := exec.CommandContext(t.Context(), kit, "plugin", "test", bin, "--tree", tree).CombinedOutput()
	if err != nil {
		t.Fatalf("the kit failed: %v\n%s", err, out)
	}

	t.Logf("%s", out)
}
