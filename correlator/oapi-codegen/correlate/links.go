package correlate

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"path"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen-sbom-openapi/openapi"
)

// NarrowDocument binds the document embedded in generated code to its
// top-level parts only, instead of every unit of it.
const NarrowDocument = "document-units"

const (
	LinkServes     = "serves"     // generated code that runs for one operation
	LinkImplements = "implements" // a handler method of the repository
	LinkEmbeds     = "embeds"     // a variable holding the document
	LinkReads      = "reads"      // code that reads the document beyond one operation
)

type Config struct {
	Documents []Document `json:"documents"`
	// Scan lists the directories searched for code that reaches the
	// embedded document through its accessors; the whole tree when empty.
	Scan []string `json:"scan,omitempty"`
	// RequestLog is the file a test stack appends one line per request to
	// ("GET /notes/n1"); collect cuts it into one file per slice.
	RequestLog string `json:"request_log,omitempty"`
	// RequestLogLines says what the log's lines hold: LinesURLs (the
	// default) or LinesPatterns.
	RequestLogLines string `json:"request_log_lines,omitempty"`
	// OtherRoutes are route patterns ("GET /healthz") the server logs that
	// no contract has; with LinesPatterns they touch nothing.
	OtherRoutes []string `json:"other_routes,omitempty"`
}

// Document is one contract and the code generated from it.
type Document struct {
	openapi.Document

	// Base is the path prefix requests carry before the templates
	// ("/api"); without it, the path of the first server's URL.
	Base string    `json:"base,omitempty"`
	Go   *GoConfig `json:"go,omitempty"`
}

type GoConfig struct {
	// Package is the generated package's directory in the tree.
	Package string `json:"package"`
	// Strict is set when the strict server was generated: its
	// strictHandler runs for every request to an operation, too.
	Strict bool `json:"strict,omitempty"`
	// NoWrapper is set when no server was generated (models or a client
	// only).
	NoWrapper bool `json:"no_wrapper,omitempty"`
	// SpecVars are the generated variables that hold the whole document;
	// DefaultSpecVars when unset, none when set empty.
	SpecVars []string `json:"spec_vars,omitempty"`
	// Generator is the generator's module, as the generated header names
	// it; GeneratorConfig the config file the go:generate line passes.
	Generator       string   `json:"generator,omitempty"`
	GeneratorConfig string   `json:"generator_config,omitempty"`
	Declarations    []string `json:"declarations,omitempty"`
	// Handlers are directories to scan for the repository's methods that
	// implement an operation: a method named after the operation that
	// takes its RequestObject.
	Handlers []string `json:"handlers,omitempty"`
	// Readers are Go unit ids of code that reads the document beyond the
	// operation being served (every path, a component by name, the served
	// document itself): each depends on all of it.
	Readers []string `json:"readers,omitempty"`
	// PerOperation are Go unit ids that use the document only for the
	// operation a request was routed to (validation, access checks).
	PerOperation []string `json:"per_operation,omitempty"`

	set map[string]bool
}

const (
	varSwaggerSpec = "swaggerSpec"
	varRawSpec     = "rawSpec"
)

// DefaultSpecVars are the variables oapi-codegen's embedded-spec option
// writes: the compressed document and the decoded, cached copy.
var DefaultSpecVars = []string{varSwaggerSpec, varRawSpec}

// GoUnit is the id go-sbom gives a declaration.
func GoUnit(dir, kind, name string) string {
	return "go:" + path.Clean(dir) + "#" + kind + ":" + name
}

// Model holds the documents' operations as openapi-sbom lists them.
type Model struct {
	*openapi.Model

	Config Config
	// Unknown are generated files whose contract the tree doesn't name.
	Unknown []Detection
	// Inputs are the files the answer was read from, those a document read along
	// included.
	Inputs []string

	goFiles []string
	modules map[string]string
	parsed  map[string]*ast.File
}

// Load finds the generated packages and their contracts, merges the config's
// documents over them, and reads the contracts with openapi-sbom's library,
// so both name every operation alike.
func Load(tree string, cfg Config, paths []string) (*Model, error) {
	if paths == nil {
		all, err := goCandidates(tree)
		if err != nil {
			return nil, err
		}

		docs, err := openapi.Candidates(tree)
		if err != nil {
			return nil, fmt.Errorf("load: %w", err)
		}

		paths = slices.Concat(all, docs)
	}

	detected := Detect(tree, paths, cfg)
	found, unknown := fromDetections(detected)

	for _, c := range cfg.Documents {
		i := slices.IndexFunc(found, func(d Document) bool { return path.Clean(d.Path) == path.Clean(c.Path) })
		if i < 0 {
			found = append(found, c)

			continue
		}

		c.Go = mergeGo(c.Go, found[i].Go)
		found[i] = c
	}

	cfg.Documents = found
	docs := make([]openapi.Document, 0, len(found))

	for _, d := range found {
		docs = append(docs, d.Document)
	}

	m, err := openapi.Build(tree, docs, openapi.Options{})
	if err != nil {
		return nil, fmt.Errorf("load: %w", err)
	}

	out := &Model{Model: m, Config: cfg, Unknown: unknown, modules: map[string]string{}, parsed: map[string]*ast.File{}}

	for _, d := range detected {
		out.Inputs = append(out.Inputs, d.Path)

		switch d.Role {
		case RoleManifest:
			out.modules[path.Dir(d.Path)] = d.Module
		case RoleGenerated, RoleDirective, RoleSource:
			out.goFiles = append(out.goFiles, d.Path)
		}
	}

	// A contract's operations may be written in other files its paths
	// refer to; the answer depends on those too, so they are inputs.
	for _, d := range cfg.Documents {
		out.Inputs = append(out.Inputs, path.Clean(d.Path))
		out.Inputs = append(out.Inputs, m.ReadFiles(path.Clean(d.Path))...)
	}

	slices.Sort(out.Inputs)
	out.Inputs = slices.Compact(out.Inputs)
	slices.Sort(out.goFiles)

	return out, nil
}

// CodeLinks returns the links from generated code and configured readers
// to the documents' units. Links of a narrowing come in both variants,
// tagged, and the core keeps the ones the level allows.
func (m *Model) CodeLinks() []Link {
	var out []Link

	for _, d := range m.Config.Documents {
		ops := m.OperationsOf(d.Path)

		if d.Go != nil {
			out = append(out, m.goLinks(d, ops)...)
		}
	}

	return out
}

// A Link says that a test bound to From is bound to To too. Note says why,
// for the why call; it isn't part of the links answer.
type Link struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Kind      string `json:"kind"`
	Narrowing string `json:"narrowing,omitempty"`
	// While says when a link of a narrowing holds: "off" for the wide
	// link, "on" for the narrow one that replaces it.
	While string `json:"while,omitempty"`
	Note  string `json:"-"`
}

func routeOf(op openapi.Operation) string {
	return op.Method + " " + op.Path
}

func (m *Model) goLinks(d Document, ops []openapi.Operation) []Link {
	g := d.Go

	var out []Link

	for _, op := range ops {
		if strings.HasPrefix(op.Path, "webhook:") {
			// oapi-codegen serves no webhooks.
			continue
		}

		if !g.NoWrapper {
			out = append(out, Link{
				From: GoUnit(g.Package, "method", "ServerInterfaceWrapper."+op.GoName), To: op.ID, Kind: LinkServes,
				Note: "the generated server wrapper runs for every request routed to " + routeOf(op) + ", before any middleware",
			})
		}

		if g.Strict {
			out = append(out, Link{
				From: GoUnit(g.Package, "method", "strictHandler."+op.GoName), To: op.ID, Kind: LinkServes,
				Note: "the generated strict handler runs for every request to " + routeOf(op) + " the middleware lets through",
			})
		}
	}

	vars := g.SpecVars
	if vars == nil {
		vars = DefaultSpecVars
	}

	// The embedded document binds what every operation shares; each
	// operation is bound through the code that serves it. With the
	// narrowing off it binds the whole document, as the bytes it holds do.
	for _, v := range vars {
		for _, t := range m.TopUnits(d.Path) {
			out = append(out, Link{
				From: GoUnit(g.Package, "var", v), To: t, Kind: LinkEmbeds, Narrowing: NarrowDocument, While: "on",
				Note: "the embedded document; each operation is bound through the code that serves it",
			})
		}

		for _, t := range m.DocumentUnits(d.Path) {
			out = append(out, Link{
				From: GoUnit(g.Package, "var", v), To: t, Kind: LinkEmbeds, Narrowing: NarrowDocument, While: "off",
				Note: "the embedded document holds all of it",
			})
		}
	}

	for _, r := range g.Readers {
		for _, t := range m.DocumentUnits(d.Path) {
			out = append(out, Link{From: r, To: t, Kind: LinkReads, Note: "listed as reading the document beyond one operation"})
		}
	}

	return out
}

// mergeGo lays the config's settings for a generated package over what
// detection found: a key the config sets wins, every other key keeps what
// detection found.
func mergeGo(c, found *GoConfig) *GoConfig {
	if c == nil {
		return found
	}

	if found == nil {
		return c
	}

	merged := *found
	merged.set = nil

	if c.sets("package", c.Package != "") {
		merged.Package = c.Package
	}

	if c.sets("strict", c.Strict) {
		merged.Strict = c.Strict
	}

	if c.sets("no_wrapper", c.NoWrapper) {
		merged.NoWrapper = c.NoWrapper
	}

	if c.sets("spec_vars", c.SpecVars != nil) {
		merged.SpecVars = c.SpecVars
	}

	if c.sets("generator", c.Generator != "") {
		merged.Generator = c.Generator
	}

	if c.sets("generator_config", c.GeneratorConfig != "") {
		merged.GeneratorConfig = c.GeneratorConfig
	}

	if c.sets("declarations", c.Declarations != nil) {
		merged.Declarations = c.Declarations
	}

	if c.sets("handlers", c.Handlers != nil) {
		merged.Handlers = c.Handlers
	}

	if c.sets("readers", c.Readers != nil) {
		merged.Readers = c.Readers
	}

	if c.sets("per_operation", c.PerOperation != nil) {
		merged.PerOperation = c.PerOperation
	}

	return &merged
}

// UnmarshalJSON reads a go block and remembers which keys it sets, so a
// key set to its zero value (strict: false, spec_vars: []) still
// overrides detection, and a key left out doesn't.
func (g *GoConfig) UnmarshalJSON(data []byte) error {
	type plain GoConfig

	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err //nolint:wrapcheck // the decoder's own error names the field
	}

	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return err //nolint:wrapcheck // as above
	}

	*g = GoConfig(p)
	g.set = map[string]bool{}

	for k, v := range keys {
		if string(v) != "null" {
			// encoding/json matches keys without regard to case.
			g.set[strings.ToLower(k)] = true
		}
	}

	return nil
}

// sets tells whether the config sets key. A GoConfig built in code rather
// than read from JSON sets what it gives a value other than the zero one.
func (g *GoConfig) sets(key string, nonZero bool) bool {
	if g.set == nil {
		return nonZero
	}

	return g.set[key]
}
