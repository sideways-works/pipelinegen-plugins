package correlate

import (
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen-sbom-go/goid"
)

func TestDetect(t *testing.T) {
	candidates, err := goCandidates(repo)
	if err != nil {
		t.Fatal(err)
	}

	var gen, dir Detection

	roles := map[string]string{}

	for _, d := range Detect(repo, candidates, Config{}) {
		roles[d.Path] = d.Role

		switch d.Role {
		case RoleGenerated:
			gen = d
		case RoleDirective:
			dir = d
		}
	}

	for p, want := range map[string]string{
		"go.mod": RoleManifest, "internal/server/server.go": RoleSource, "internal/access/access_test.go": RoleSource,
	} {
		if roles[p] != want {
			t.Errorf("%s: role %q, want %q", p, roles[p], want)
		}
	}

	if gen.Path != "gen/notesgen/notes.gen.go" || gen.Generator != "github.com/oapi-codegen/oapi-codegen/v2" || gen.Version != "v2.8.0" {
		t.Errorf("generated = %+v", gen)
	}

	want := []string{
		"Handler", "HandlerFromMux", "HandlerFromMuxWithBaseURL", "HandlerWithOptions",
		"NewStrictHandler", "NewStrictHandlerWithOptions", "ServerInterface", "ServerInterfaceWrapper",
		"StrictServerInterface", "rawSpec", "strictHandler", "swaggerSpec",
	}
	if !slices.Equal(gen.Declarations, want) {
		t.Errorf("declarations %q, want %q", gen.Declarations, want)
	}

	if dir.Path != "gen/notesgen/generate.go" || dir.Contract != "api/notes.yaml" || dir.Config != "gen/notesgen/codegen.yaml" {
		t.Errorf("directive = %+v", dir)
	}
}

func TestDirectiveArgs(t *testing.T) {
	for line, want := range map[string][2]string{
		"go tool oapi-codegen -config cfg.yaml ../api.yaml":                                                  {"../api.yaml", "cfg.yaml"},
		"go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 --config=cfg.yaml spec.json": {"spec.json", "cfg.yaml"},
		"oapi-codegen -package p -generate types -o types.gen.go spec.yaml":                                  {"spec.yaml", ""},
		"go tool oapi-codegen -config cfg.yaml":                                                              {"", "cfg.yaml"},
	} {
		contract, config := directiveArgs(strings.Fields(line))
		if contract != want[0] || config != want[1] {
			t.Errorf("%s: %q %q, want %q", line, contract, config, want)
		}
	}
}

func TestLoadByDetection(t *testing.T) {
	m, err := Load(repo, Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// The contract no generated package serves is read too, for its routes.
	if len(m.Config.Documents) != 2 || m.Config.Documents[1].Path != "api/tags.yaml" || m.Config.Documents[1].Go != nil {
		t.Fatalf("documents %+v", m.Config.Documents)
	}

	d := m.Config.Documents[0]
	if d.Path != "api/notes.yaml" || d.Go.Package != "gen/notesgen" || !d.Go.Strict || d.Go.NoWrapper ||
		!slices.Equal(d.Go.SpecVars, []string{"rawSpec", "swaggerSpec"}) {
		t.Errorf("document %+v, go %+v", d, d.Go)
	}

	if !has(m.CodeLinks(), "go:gen/notesgen#method:strictHandler.GetNote", "api:getNote", LinkServes) {
		t.Error("no strict handler link")
	}

	links, err := m.HandlerLinks(repo)
	if err != nil {
		t.Fatal(err)
	}

	if len(links) != 2 {
		t.Errorf("handler links found by scanning the tree: %v", links)
	}
}

func TestDigestFrom(t *testing.T) {
	m, err := Load(repo, Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(repo, "gen/notesgen/notes.gen.go"), nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	ids := map[string]bool{}

	for _, d := range goid.File("gen/notesgen", "notes.gen.go", f) {
		ids[string(d.ID)] = true
	}

	narrow := m.DigestFrom()
	if len(narrow) != 11 {
		t.Errorf("%d relations, want 11", len(narrow))
	}

	// The route registration takes the routing table of every document:
	// a route anywhere can take requests from an operation of this one.
	wantFrom := []string{
		"api:api/notes.yaml#/servers", "api:api/notes.yaml#/openapi", "api:api/notes.yaml#/paths",
		"api:api/tags.yaml#/paths",
		"dep:go:github.com/oapi-codegen/oapi-codegen/v2", "file:gen/notesgen/codegen.yaml",
	}

	for _, r := range narrow {
		if !ids[r.Unit] {
			t.Errorf("relation for %s: no such declaration", r.Unit)
		}

		if !slices.Equal(r.From, wantFrom) || r.Narrowing != NarrowGenerated {
			t.Errorf("%s takes its digest from %q under %q, want %q under %s", r.Unit, r.From, r.Narrowing, wantFrom, NarrowGenerated)
		}
	}
}

func TestUnknownContract(t *testing.T) {
	docs, unknown := fromDetections([]Detection{{Path: "gen/x/x.gen.go", Role: RoleGenerated, Declarations: []string{"swaggerSpec"}}})
	if len(docs) != 0 || len(unknown) != 1 {
		t.Errorf("docs %v, unknown %v", docs, unknown)
	}
}

func TestDetectClaimsContracts(t *testing.T) {
	got := Detect(repo, []string{"api/notes.yaml", "api/tags.yaml", "gen/notesgen/codegen.yaml"}, Config{})

	var claims []string
	for _, d := range got {
		claims = append(claims, d.Path+" "+d.Role)
	}

	if want := []string{"api/notes.yaml contract", "api/tags.yaml contract"}; !slices.Equal(claims, want) {
		t.Errorf("claims %q, want %q", claims, want)
	}
}

func TestDetectSources(t *testing.T) {
	cfg := Config{Scan: []string{"internal/rules"}, Documents: []Document{{Go: &GoConfig{Handlers: []string{"internal/handlers"}}}}}

	got := map[string]string{}
	for _, d := range Detect(repo, []string{"internal/rules/rules.go", "internal/handlers/notes.go", "internal/server/server.go", "gen/notesgen/notes.gen.go"}, cfg) {
		got[d.Path] = d.Role
	}

	want := map[string]string{
		"internal/rules/rules.go": RoleSource, "internal/handlers/notes.go": RoleSource, "gen/notesgen/notes.gen.go": RoleGenerated,
	}
	if !maps.Equal(got, want) {
		t.Errorf("claims %v, want %v", got, want)
	}

	for _, p := range []string{"vendor/x/x.go", "a/testdata/b.go", ".hidden/c.go", "_old/d.go"} {
		if !unbuilt(p) {
			t.Errorf("%s: read", p)
		}
	}

	if unbuilt("main.go") || unbuilt("cmd/x/main.go") {
		t.Error("a file of the build is left out")
	}
}
