package npm

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const fixture = "testdata/workspace"

func tree(t *testing.T, edits ...[3]string) string {
	t.Helper()

	dir := t.TempDir()

	for _, f := range []string{"package.json", "package-lock.json", ".nvmrc"} {
		data, err := os.ReadFile(filepath.Join(fixture, f))
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

		if err := os.WriteFile(filepath.Join(dir, f), []byte(text), 0o600); err != nil { //nolint:gosec // the fixture's own names
			t.Fatal(err)
		}
	}

	return dir
}

func build(t *testing.T, dir string, cfg Config) *Model {
	t.Helper()

	m, err := Build(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}

	return m
}

func units(m *Model) map[string]Unit {
	out := map[string]Unit{}
	for _, u := range m.Units {
		out[u.ID] = u
	}

	return out
}

func changed(t *testing.T, edits ...[3]string) []string {
	t.Helper()

	before := units(build(t, tree(t), Config{}))
	after := units(build(t, tree(t, edits...), Config{}))

	var out []string

	for id, u := range after {
		if before[id].Digest != u.Digest {
			out = append(out, id)
		}
	}

	for id := range before {
		if _, ok := after[id]; !ok {
			out = append(out, id)
		}
	}

	slices.Sort(out)

	return out
}

func TestUnits(t *testing.T) {
	got := units(build(t, fixture, Config{Lockfiles: []string{"package-lock.json"}}))

	want := map[string]struct {
		kind        string
		direct, dev bool
		versions    string
	}{
		"dep:npm:react":          {KindPackage, true, false, "18.3.1"},
		"dep:npm:my-react":       {KindPackage, true, false, "17.0.2"},
		"dep:npm:loose-envify":   {KindPackage, false, false, "1.4.0"},
		"dep:npm:js-tokens":      {KindPackage, false, false, "2.0.0 4.0.0"},
		"dep:npm:object-assign":  {KindPackage, false, false, "4.1.1"},
		"dep:npm:left-pad":       {KindPackage, true, false, "1.3.0"},
		"dep:npm:postcss":        {KindPackage, true, false, "8.4.49"},
		"dep:npm:picocolors":     {KindPackage, false, false, "1.1.1"},
		"dep:npm:chart":          {KindPackage, true, false, "1.0.0"},
		"dep:npm:vite":           {KindToolchain, true, true, "6.0.3"},
		"dep:npm:picomatch":      {KindToolchain, false, true, "4.0.2"},
		"dep:npm:toolchain:node": {KindToolchain, true, false, "22.11.0 >=22"},
		"dep:npm:toolchain:npm":  {KindToolchain, true, false, "10.9.0"},
	}

	for id, w := range want {
		u, ok := got[id]
		if !ok {
			t.Errorf("no unit %s", id)

			continue
		}

		if u.Kind != w.kind || u.Attributes.Direct != w.direct || u.Attributes.Dev != w.dev || strings.Join(u.Attributes.Versions, " ") != w.versions {
			t.Errorf("%s = kind %s direct %v dev %v versions %v, want %+v", id, u.Kind, u.Attributes.Direct, u.Attributes.Dev, u.Attributes.Versions, w)
		}
	}

	if len(got) != len(want) {
		var extra []string

		for id := range got {
			if _, ok := want[id]; !ok {
				extra = append(extra, id)
			}
		}

		t.Errorf("unexpected units %q (workspaces and links get none)", extra)
	}
}

func TestLinks(t *testing.T) {
	m := build(t, fixture, Config{})

	var got []string
	for _, l := range m.Links {
		got = append(got, l.From+" -> "+l.To)
	}

	want := []string{
		"dep:npm:left-pad -> dep:npm:js-tokens",
		"dep:npm:loose-envify -> dep:npm:js-tokens",
		"dep:npm:my-react -> dep:npm:loose-envify",
		"dep:npm:my-react -> dep:npm:object-assign",
		"dep:npm:postcss -> dep:npm:picocolors",
		"dep:npm:react -> dep:npm:loose-envify",
		"dep:npm:vite -> dep:npm:picomatch",
		"dep:npm:vite -> dep:npm:postcss",
	}
	if !slices.Equal(got, want) {
		t.Errorf("links\n got %q\nwant %q", got, want)
	}
}

func TestDigests(t *testing.T) {
	t.Run("a bump changes the package and nothing else", func(t *testing.T) {
		got := changed(t, [3]string{"package-lock.json", `"version": "1.4.0"`, `"version": "1.4.1"`})
		if !slices.Equal(got, []string{"dep:npm:loose-envify"}) {
			t.Errorf("changed %q", got)
		}
	})

	t.Run("a bump of one copy changes the package", func(t *testing.T) {
		got := changed(t, [3]string{"package-lock.json", `"version": "2.0.0"`, `"version": "2.0.1"`})
		if !slices.Equal(got, []string{"dep:npm:js-tokens"}) {
			t.Errorf("changed %q", got)
		}
	})

	t.Run("another registry for the same content changes nothing", func(t *testing.T) {
		got := changed(t, [3]string{"package-lock.json", "https://registry.npmjs.org/react/-/react-18.3.1.tgz", "https://mirror.example/react/-/react-18.3.1.tgz"})
		if len(got) != 0 {
			t.Errorf("changed %q", got)
		}
	})

	t.Run("a git dependency changes with its commit", func(t *testing.T) {
		got := changed(t, [3]string{"package-lock.json", "#0a1b2c3d", "#ffffffff"})
		if !slices.Equal(got, []string{"dep:npm:chart"}) {
			t.Errorf("changed %q", got)
		}
	})

	t.Run("a workspace's own version changes nothing", func(t *testing.T) {
		got := changed(t, [3]string{"package-lock.json", `"version": "1.0.0",
      "dependencies": {"left-pad"`, `"version": "1.1.0",
      "dependencies": {"left-pad"`})
		if len(got) != 0 {
			t.Errorf("changed %q", got)
		}
	})

	t.Run("the declared Node version", func(t *testing.T) {
		got := changed(t, [3]string{".nvmrc", "22.11.0", "22.12.0"})
		if !slices.Equal(got, []string{"dep:npm:toolchain:node"}) {
			t.Errorf("changed %q", got)
		}
	})
}

func TestToolchainsConfig(t *testing.T) {
	got := units(build(t, fixture, Config{Toolchains: []string{}}))
	if got["dep:npm:vite"].Kind != KindPackage || got["dep:npm:picomatch"].Kind != KindPackage {
		t.Errorf("with no toolchains listed: vite %s, picomatch %s", got["dep:npm:vite"].Kind, got["dep:npm:picomatch"].Kind)
	}

	got = units(build(t, fixture, Config{Toolchains: []string{"left-pad"}}))
	if got["dep:npm:left-pad"].Kind != KindToolchain || got["dep:npm:js-tokens"].Kind != KindPackage {
		t.Errorf("left-pad %s; js-tokens %s (loose-envify still needs it)", got["dep:npm:left-pad"].Kind, got["dep:npm:js-tokens"].Kind)
	}
}

func TestResolve(t *testing.T) {
	l, err := ReadLockfile(fixture, "package-lock.json")
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ from, name, want string }{
		{"node_modules/left-pad", "js-tokens", "node_modules/left-pad/node_modules/js-tokens"},
		{"node_modules/loose-envify", "js-tokens", "node_modules/js-tokens"},
		{"apps/web", "react", "node_modules/react"},
		{"apps/web", "@demo/ui", "node_modules/@demo/ui"},
		{"node_modules/vite", "fsevents", ""},
	} {
		got, _ := l.Resolve(c.from, c.name)
		if got != c.want {
			t.Errorf("Resolve(%s, %s) = %q, want %q", c.from, c.name, got, c.want)
		}
	}
}

func TestLockfileErrors(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte(`{"lockfileVersion": 1, "dependencies": {}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Build(dir, Config{}); !errors.Is(err, ErrCannotTell) {
		t.Errorf("version 1: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Build(dir, Config{Lockfiles: []string{"package-lock.json"}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("broken: %v", err)
	}

	if _, err := Build(dir, Config{}); !errors.Is(err, ErrNoLockfile) {
		t.Errorf("broken, detected: %v", err)
	}

	if _, err := Build(t.TempDir(), Config{}); !errors.Is(err, ErrCannotTell) {
		t.Errorf("no lockfile: %v, want can't tell", err)
	}

	if _, err := BuildFiles(tree(t), Config{}); !errors.Is(err, ErrNoLockfile) {
		t.Errorf("nothing named: %v, want can't tell without walking the tree", err)
	}
}

func TestDocumentsIn(t *testing.T) {
	cdx := `{"bomFormat":"CycloneDX","specVersion":"1.6","components":[
	  {"name":"vendored","version":"2.0.0","purl":"pkg:npm/vendored@2.0.0","hashes":[{"alg":"SHA-256","content":"abc"}]},
	  {"name":"react","version":"0.0.1","purl":"pkg:npm/react@0.0.1"},
	  {"name":"lib","version":"1","purl":"pkg:golang/example.com/lib@v1"}]}`
	spdx := `{"spdxVersion":"SPDX-2.3","packages":[{"name":"@scope/thing","versionInfo":"3.1.0",
	  "externalRefs":[{"referenceType":"purl","referenceLocator":"pkg:npm/%40scope/thing@3.1.0"}],
	  "checksums":[{"algorithm":"SHA1","checksumValue":"def"}]}]}`

	dir := tree(t)
	for f, s := range map[string]string{"bom.json": cdx, "sbom.spdx.json": spdx} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got := units(build(t, dir, Config{Documents: []string{"bom.json", "sbom.spdx.json"}}))

	if u := got["dep:npm:vendored"]; u.File != "bom.json" || u.Attributes.Versions[0] != "2.0.0" {
		t.Errorf("vendored = %+v", u)
	}

	if u := got["dep:npm:@scope/thing"]; u.File != "sbom.spdx.json" {
		t.Errorf("@scope/thing = %+v", u)
	}

	if v := got["dep:npm:react"].Attributes.Versions; !slices.Equal(v, []string{"18.3.1"}) {
		t.Errorf("the lockfile must win: react %v", v)
	}

	if _, ok := got["dep:npm:lib"]; ok {
		t.Error("a Go module became an npm unit")
	}
}

func TestDocumentOut(t *testing.T) {
	m := build(t, fixture, Config{})

	data, err := json.Marshal(m.Document("npm-sbom", "v0"))
	if err != nil {
		t.Fatal(err)
	}

	var b cdxBOM
	if err := json.Unmarshal(data, &b); err != nil {
		t.Fatal(err)
	}

	var react *cdxComponent

	for i := range b.Components {
		if b.Components[i].PURL == "pkg:npm/react@18.3.1" {
			react = &b.Components[i]
		}
	}

	if react == nil || len(react.Hashes) != 1 || react.Hashes[0].Alg != "SHA-512" || len(react.Hashes[0].Content) != 128 {
		t.Fatalf("react = %+v", react)
	}

	if !slices.ContainsFunc(b.Components, func(c cdxComponent) bool { return c.PURL == "pkg:npm/react@17.0.2" }) {
		t.Error("the alias's real package is missing")
	}

	for _, d := range b.Dependencies {
		if d.Ref == "pkg:npm/react@18.3.1" && !slices.Equal(d.DependsOn, []string{"pkg:npm/loose-envify@1.4.0"}) {
			t.Errorf("react depends on %v", d.DependsOn)
		}
	}
}

func TestPURL(t *testing.T) {
	if got := PURL("@scope/thing", "1.0.0"); got != "pkg:npm/%40scope/thing@1.0.0" {
		t.Errorf("PURL = %s", got)
	}

	name, version, ok := npmPURL("pkg:npm/%40scope/thing@1.0.0?vcs_url=x")
	if !ok || name != "@scope/thing" || version != "1.0.0" {
		t.Errorf("npmPURL = %s %s %v", name, version, ok)
	}
}

func TestDetect(t *testing.T) {
	candidates, err := Candidates(fixture)
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, d := range Detect(fixture, append(candidates, "node_modules/x/package.json", "not/a/package.json")) {
		got = append(got, d.Path+" "+d.Role)
	}

	want := []string{
		".nvmrc node-version", "package-lock.json lockfile", "package.json manifest",
		"tools/lint/package-lock.json lockfile", "tools/lint/package.json manifest",
	}
	if !slices.Equal(got, want) {
		t.Errorf("detected %q, want %q", got, want)
	}

	if l := Lockfiles([]Detection{
		{Path: "a/package-lock.json", Role: RoleLockfile}, {Path: "a/npm-shrinkwrap.json", Role: RoleLockfile},
		{Path: "package-lock.json", Role: RoleLockfile},
	}); !slices.Equal(l, []string{"a/npm-shrinkwrap.json", "package-lock.json"}) {
		t.Errorf("lockfiles %q", l)
	}
}

func TestTwoLockfiles(t *testing.T) {
	m := build(t, fixture, Config{Lockfiles: []string{"package-lock.json", "tools/lint/package-lock.json"}})

	u := units(m)["dep:npm:left-pad"]
	if !slices.Equal(u.Attributes.Versions, []string{"1.3.0", "1.3.1"}) || u.Attributes.Dev {
		t.Errorf("left-pad = %+v", u)
	}

	detected := build(t, fixture, Config{})
	if units(detected)["dep:npm:left-pad"].Digest != u.Digest {
		t.Error("detection found other lockfiles than the two named")
	}
}

func TestLabels(t *testing.T) {
	m := build(t, fixture, Config{})

	got := units(m)
	for id, want := range map[string]string{ //nolint:gosec // a unit id, not a credential
		"dep:npm:react":          "the package react@18.3.1",
		"dep:npm:js-tokens":      "the package js-tokens@2.0.0, 4.0.0",
		"dep:npm:toolchain:node": "the toolchain node 22.11.0, >=22",
	} {
		if got[id].Label != want {
			t.Errorf("%s: label %q, want %q", id, got[id].Label, want)
		}
	}

	for _, u := range m.Units {
		if u.Label == "" || len([]rune(u.Label)) > MaxLabel {
			t.Errorf("%s: label %q", u.ID, u.Label)
		}
	}

	defer func(f func(Unit) string) { labelOf = f }(labelOf)

	labelOf = func(Unit) string { return "x" }
	other := build(t, fixture, Config{})

	for i := range m.Units {
		a, b := m.Units[i], other.Units[i]
		a.Label, b.Label = "", ""

		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s changes with its label", a.ID)
		}
	}
}
