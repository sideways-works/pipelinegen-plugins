package npm

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const pnpmFixture = "testdata/pnpm"

func pnpmTree(t *testing.T, lock string, edits ...[2]string) string {
	t.Helper()

	dir := t.TempDir()

	for _, f := range []string{"package.json", ".nvmrc"} {
		data, err := os.ReadFile(filepath.Join(pnpmFixture, f))
		if err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(dir, f), data, 0o600); err != nil { //nolint:gosec // the fixture's own names
			t.Fatal(err)
		}
	}

	if lock == "" {
		data, err := os.ReadFile(filepath.Join(pnpmFixture, "pnpm-lock.yaml"))
		if err != nil {
			t.Fatal(err)
		}

		lock = string(data)
	}

	for _, e := range edits {
		if !strings.Contains(lock, e[0]) {
			t.Fatalf("edit: %q is not in the lockfile", e[0])
		}

		lock = strings.Replace(lock, e[0], e[1], 1)
	}

	if err := os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), []byte(lock), 0o600); err != nil { //nolint:gosec // a fixed name in the test's own directory
		t.Fatal(err)
	}

	return dir
}

// The alias my-react installs react 17 in the store, so react has both
// versions.
var pnpmWant = map[string]struct {
	kind        string
	direct, dev bool
	versions    string
}{
	"dep:npm:react":          {KindPackage, true, false, "17.0.2 18.3.1"},
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
	"dep:npm:toolchain:pnpm": {KindToolchain, true, false, "9.15.0"},
}

var pnpmLinks = []string{
	"dep:npm:chart -> dep:npm:react",
	"dep:npm:left-pad -> dep:npm:js-tokens",
	"dep:npm:loose-envify -> dep:npm:js-tokens",
	"dep:npm:my-react -> dep:npm:loose-envify",
	"dep:npm:my-react -> dep:npm:object-assign",
	"dep:npm:postcss -> dep:npm:picocolors",
	"dep:npm:react -> dep:npm:loose-envify",
	"dep:npm:react -> dep:npm:object-assign",
	"dep:npm:vite -> dep:npm:picomatch",
	"dep:npm:vite -> dep:npm:postcss",
}

func checkPnpm(t *testing.T, m *Model) {
	t.Helper()

	got := units(m)

	for id, w := range pnpmWant {
		u, ok := got[id]
		if !ok {
			t.Errorf("no unit %s", id)

			continue
		}

		if u.Kind != w.kind || u.Attributes.Direct != w.direct || u.Attributes.Dev != w.dev || strings.Join(u.Attributes.Versions, " ") != w.versions {
			t.Errorf("%s = kind %s direct %v dev %v versions %v, want %+v", id, u.Kind, u.Attributes.Direct, u.Attributes.Dev, u.Attributes.Versions, w)
		}

		if u.File != "pnpm-lock.yaml" && !strings.HasPrefix(id, "dep:npm:toolchain:") {
			t.Errorf("%s is in %s", id, u.File)
		}
	}

	if len(got) != len(pnpmWant) {
		t.Errorf("%d units, want %d: %v", len(got), len(pnpmWant), got)
	}

	var links []string
	for _, l := range m.Links {
		links = append(links, l.From+" -> "+l.To)
	}

	if !slices.Equal(links, pnpmLinks) {
		t.Errorf("links\n got %q\nwant %q", links, pnpmLinks)
	}
}

func TestPnpmLockfile(t *testing.T) {
	checkPnpm(t, build(t, pnpmFixture, Config{}))
}

// Version 6.0 (pnpm 8) keeps dependencies in packages, keyed with a
// leading slash, and marks dev packages itself.
func TestPnpmLockfileVersion6(t *testing.T) {
	lock := `lockfileVersion: '6.0'

importers:

  .:
    devDependencies:
      vite:
        specifier: ^6.0.0
        version: 6.0.3(postcss@8.4.49)

  apps/web:
    dependencies:
      '@demo/ui':
        specifier: workspace:*
        version: link:../../packages/ui
      chart:
        specifier: ^1.0.0
        version: 1.0.0(react@18.3.1)
      my-react:
        specifier: npm:react@^17.0.2
        version: /react@17.0.2
      react:
        specifier: ^18.3.1
        version: 18.3.1

  packages/ui:
    dependencies:
      left-pad:
        specifier: ^1.3.0
        version: 1.3.0
      postcss:
        specifier: ^8.4.49
        version: 8.4.49

packages:

  /chart@1.0.0(react@18.3.1):
    resolution: {integrity: sha512-chart}
    peerDependencies:
      react: '>=17'
    dependencies:
      react: 18.3.1
    dev: false

  /js-tokens@2.0.0:
    resolution: {integrity: sha512-jstokens2}
    dev: false

  /js-tokens@4.0.0:
    resolution: {integrity: sha512-jstokens4}
    dev: false

  /left-pad@1.3.0:
    resolution: {integrity: sha512-leftpad}
    dependencies:
      js-tokens: 2.0.0
    dev: false

  /loose-envify@1.4.0:
    resolution: {integrity: sha512-looseenvify}
    dependencies:
      js-tokens: 4.0.0
    dev: false

  /object-assign@4.1.1:
    resolution: {integrity: sha512-objectassign}
    dev: false

  /picocolors@1.1.1:
    resolution: {integrity: sha512-picocolors}
    dev: false

  /picomatch@4.0.2:
    resolution: {integrity: sha512-picomatch}
    dev: true

  /postcss@8.4.49:
    resolution: {integrity: sha512-postcss}
    dependencies:
      picocolors: 1.1.1
    dev: false

  /react@17.0.2:
    resolution: {integrity: sha512-react17}
    dependencies:
      loose-envify: 1.4.0
      object-assign: 4.1.1
    dev: false

  /react@18.3.1:
    resolution: {integrity: sha512-react18}
    dependencies:
      loose-envify: 1.4.0
    dev: false

  /vite@6.0.3(postcss@8.4.49):
    resolution: {integrity: sha512-vite}
    dependencies:
      picomatch: 4.0.2
      postcss: 8.4.49
    dev: true
`
	checkPnpm(t, build(t, pnpmTree(t, lock), Config{}))
}

func TestPnpmDigests(t *testing.T) {
	digests := func(dir string) map[string]string {
		out := map[string]string{}
		for _, u := range build(t, dir, Config{}).Units {
			out[u.ID] = u.Digest
		}

		return out
	}

	before := digests(pnpmTree(t, ""))

	for name, edits := range map[string][][2]string{
		"a version":  {{"    dependencies:\n      picocolors: 1.1.1", "    dependencies:\n      picocolors: 1.1.2"}, {"  picocolors@1.1.1:\n    resolution: {integrity: sha512-picocolors}", "  picocolors@1.1.2:\n    resolution: {integrity: sha512-picocolors}"}, {"  picocolors@1.1.1: {}", "  picocolors@1.1.2: {}"}},
		"a checksum": {{"sha512-picocolors", "sha512-other"}},
	} {
		t.Run(name, func(t *testing.T) {
			after := digests(pnpmTree(t, "", edits...))

			var got []string

			for id, d := range after {
				if before[id] != d {
					got = append(got, id)
				}
			}

			if !slices.Equal(got, []string{"dep:npm:picocolors"}) {
				t.Errorf("changed %q, want picocolors alone", got)
			}
		})
	}
}

func TestPnpmDetectAndErrors(t *testing.T) {
	got := Detect(pnpmFixture, []string{"pnpm-lock.yaml", "package.json"})
	if len(got) != 2 || got[0].Role != RoleLockfile || got[0].Path != "pnpm-lock.yaml" {
		t.Errorf("detected %+v", got)
	}

	if l := Lockfiles([]Detection{{Path: "pnpm-lock.yaml", Role: RoleLockfile}, {Path: "package-lock.json", Role: RoleLockfile}, {Path: "a/pnpm-lock.yaml", Role: RoleLockfile}}); !slices.Equal(l, []string{"a/pnpm-lock.yaml", "package-lock.json"}) {
		t.Errorf("lockfiles %q", l)
	}

	_, err := Build(pnpmTree(t, "lockfileVersion: 5.4\n\ndependencies:\n  react: 18.3.1\n"), Config{})
	if !errors.Is(err, ErrCannotTell) || !strings.Contains(err.Error(), "pnpm 8 or later") {
		t.Errorf("err = %v, want can't tell for version 5.4", err)
	}

	_, err = Build(pnpmTree(t, "", [2]string{"  react@18.3.1:\n    dependencies:\n      loose-envify: 1.4.0\n", ""}), Config{})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("err = %v, want an invalid lockfile for a key in no entry", err)
	}
}
