package correlate

import (
	"slices"
	"strings"
)

// A Relation says that a unit of another inventory takes its digest from
// these units instead of its own content.
type Relation struct {
	Unit      string   `json:"unit"`
	From      []string `json:"from"`
	Narrowing string   `json:"narrowing"`
	Reason    string   `json:"reason"`
}

// NarrowGenerated lets the document-wide generated declarations take their
// digest from the contract, the generator's version and its config instead
// of their own content. A hand edit to generated code is then not seen,
// which a check that regenerates and compares makes safe.
const NarrowGenerated = "generated-from-source"

// documentWide are the generated declarations whose content spans every
// operation: the embedded document, the server interfaces, the strict handler
// type and the route registration functions.
var documentWide = []string{
	varSwaggerSpec, varRawSpec,
	"ServerInterface", "StrictServerInterface", typeStrictHandler,
	"Handler", "HandlerFromMux", "HandlerFromMuxWithBaseURL", "HandlerWithOptions",
	"NewStrictHandler", "NewStrictHandlerWithOptions",
	"RegisterHandlers", "RegisterHandlersWithBaseURL",
}

// typeStrictHandler is the strict server's handler type: one method per
// operation, so its digest moves with any operation's parameters.
const typeStrictHandler = "strictHandler"

// DigestFrom relates the document-wide generated declarations to the
// contract's top-level parts, the generator's version and its config. An
// operation's own change is bound through its wrapper and handler; letting
// the whole content count would select every test that starts the server.
// The relations belong to NarrowGenerated: while it is off, the core ignores
// them.
func (m *Model) DigestFrom() []Relation {
	var out []Relation

	for _, d := range m.Config.Documents {
		g := d.Go
		if g == nil {
			continue
		}

		// What every operation shares, and the routing table of every
		// document: several servers may register on one router, and a
		// route anywhere can take requests from an operation here.
		from := slices.Clone(m.TopUnits(d.Path))
		for _, r := range m.RouteUnits() {
			if !slices.Contains(from, r) {
				from = append(from, r)
			}
		}

		if g.Generator != "" {
			from = append(from, "dep:go:"+g.Generator)
		}

		if g.GeneratorConfig != "" {
			from = append(from, "file:"+g.GeneratorConfig)
		}

		for _, n := range present(g) {
			kind := "func"

			switch {
			case n == varSwaggerSpec || n == varRawSpec:
				kind = "var"
			case strings.HasSuffix(n, "Interface") || n == typeStrictHandler:
				kind = "type"
			}

			out = append(out, Relation{
				Unit: GoUnit(g.Package, kind, n), From: from, Narrowing: NarrowGenerated,
				Reason: "generated from all of " + d.Path + "; each operation is bound through its own wrapper",
			})
		}
	}

	return out
}

func present(g *GoConfig) []string {
	if len(g.Declarations) == 0 {
		if g.SpecVars == nil {
			return DefaultSpecVars
		}

		return g.SpecVars
	}

	var out []string

	for _, n := range documentWide {
		if slices.Contains(g.Declarations, n) {
			out = append(out, n)
		}
	}

	return out
}
