package correlate

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen-sbom-go/decls"
	"github.com/sideways-works/pipelinegen-sbom-go/deps"
)

// changed is a copy of the fixture with getNote given a query parameter,
// the contract and the code oapi-codegen generated from it both changed.
const changed = "testdata/getnote-full"

// moduleCache is where the fixture's modules are: `go mod download` in
// testdata/repo fills it once, and the tests fetch nothing.
func moduleCache(t *testing.T) string {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), "go", "env", "GOMODCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}

	return strings.TrimSpace(string(out))
}

func copyTree(t *testing.T, overlay string) string {
	t.Helper()

	dst := filepath.Join(t.TempDir(), "repo")
	if err := os.CopyFS(dst, os.DirFS(repo)); err != nil {
		t.Fatal(err)
	}

	if overlay == "" {
		return dst
	}

	for _, f := range []string{"api/notes.yaml", "gen/notesgen/notes.gen.go"} {
		data, err := os.ReadFile(filepath.Join(overlay, f))
		if err != nil {
			t.Fatal(err)
		}

		write(t, filepath.Join(dst, f), string(data))
	}

	return dst
}

// digests resolves a tree's units the way pipelinegen does, a relation's
// sources' digests standing in for the unit's own. It also returns the
// relations whose sources aren't all units.
func digests(t *testing.T, tree string, relations bool) (map[string]string, []string) {
	t.Helper()

	m, err := Load(tree, Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	units := map[string]string{}

	pkg, err := decls.Read(tree, "gen/notesgen")
	if err != nil {
		t.Fatal(err)
	}

	for _, u := range pkg.Units {
		units[string(u.ID)] = u.Digest
	}

	graph, err := deps.Read(tree, []string{"."}, moduleCache(t))
	if err != nil {
		t.Fatal(err)
	}

	if len(graph.CannotTell) > 0 {
		t.Fatalf("modules missing from the module cache (run go mod download in %s): %+v", repo, graph.CannotTell)
	}

	for _, mod := range graph.Modules {
		units[mod.ID] = mod.Digest
	}

	for _, u := range m.Units {
		units[u.ID] = u.Digest
	}

	cfg := "gen/notesgen/codegen.yaml"

	data, err := os.ReadFile(filepath.Join(tree, cfg))
	if err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256(data)
	units["file:"+cfg] = hex.EncodeToString(sum[:])

	if !relations {
		return units, nil
	}

	var missing []string

	out := maps.Clone(units)

	for _, r := range m.DigestFrom() {
		h := sha256.New()

		for _, f := range slices.Sorted(slices.Values(r.From)) {
			d, ok := units[f]
			if !ok {
				missing = append(missing, r.Unit+" takes its digest from "+f+", which is missing")

				continue
			}

			h.Write([]byte(f + " " + d + "\n"))
		}

		out[r.Unit] = hex.EncodeToString(h.Sum(nil))
	}

	return out, missing
}

func changedUnits(before, after map[string]string, prefix string) []string {
	var out []string

	for id, d := range after {
		if strings.HasPrefix(id, prefix) && before[id] != d {
			out = append(out, id)
		}
	}

	slices.Sort(out)

	return out
}

func TestGeneratedCodeTakesItsDigestFromTheContract(t *testing.T) {
	before, missing := digests(t, copyTree(t, ""), true)
	if len(missing) > 0 {
		t.Fatalf("relations whose sources are missing: %q", missing)
	}

	after, missing := digests(t, copyTree(t, changed), true)
	if len(missing) > 0 {
		t.Fatalf("relations whose sources are missing: %q", missing)
	}

	// The api unit keys importers' answer caches and is no code a slice binds,
	// so it must change, and nothing else of the package but getNote's own code.
	api := "go:gen/notesgen#api:notesgen"

	got := changedUnits(before, after, "go:gen/notesgen#")
	if !slices.Contains(got, api) {
		t.Errorf("%s didn't change with getNote's signature; changed: %q", api, got)
	}

	for _, id := range got {
		if id != api && !strings.Contains(id, "GetNote") {
			t.Errorf("%s changed, but it isn't getNote's own code", id)
		}
	}

	for _, want := range []string{
		"go:gen/notesgen#method:ServerInterfaceWrapper.GetNote",
		"go:gen/notesgen#method:strictHandler.GetNote",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("%s didn't change; changed: %q", want, got)
		}
	}

	m, err := Load(copyTree(t, ""), Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range m.DigestFrom() {
		for _, f := range r.From {
			if before[f] != after[f] {
				t.Errorf("%s, a source of %s, changed with one operation", f, r.Unit)
			}
		}
	}

	ownBefore, _ := digests(t, copyTree(t, ""), false)
	ownAfter, _ := digests(t, copyTree(t, changed), false)

	own := changedUnits(ownBefore, ownAfter, "go:gen/notesgen#")
	for _, wide := range []string{"go:gen/notesgen#var:swaggerSpec", "go:gen/notesgen#type:ServerInterface", "go:gen/notesgen#type:strictHandler"} {
		if !slices.Contains(own, wide) {
			t.Errorf("without the relations %s should change with the operation; changed: %q", wide, own)
		}
	}
}
