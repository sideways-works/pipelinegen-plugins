package correlate

import (
	"go/ast"
	"go/token"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/sideways-works/pipelinegen-plugins/sbom/go/goid"
)

// minMapKeys is how many keys a map literal needs before its keys say it
// is keyed by operations: one operation's id as a key is common in code
// about that operation alone.
const minMapKeys = 2

// MapReaders finds the declarations outside the generated packages that hold
// a map literal keyed by at least two operations of a document. Code that
// walks such a map depends on every operation, so each is linked as a reader
// of all of it.
func (m *Model) MapReaders(tree string) ([]Link, error) {
	byDoc := map[string]map[string]bool{}

	var generated []string

	for _, d := range m.Config.Documents {
		names := map[string]bool{}

		for _, op := range m.OperationsOf(d.Path) {
			names[op.OperationID] = true
			names[op.GoName] = true
		}

		byDoc[d.Path] = names

		if d.Go != nil {
			generated = append(generated, path.Clean(d.Go.Package))
		}
	}

	files, err := m.parse(tree, m.Config.Scan, true)
	if err != nil {
		return nil, err
	}

	var out []Link

	for _, f := range files {
		if slices.Contains(generated, f.dir) {
			continue
		}

		for _, decl := range goid.File(f.dir, f.name, f.file) {
			for _, doc := range keyedDocuments(decl.Node, byDoc) {
				for _, t := range m.DocumentUnits(doc) {
					out = append(out, Link{
						From: string(decl.ID), To: t, Kind: LinkReads,
						Note: "holds a map keyed by the operations of " + doc,
					})
				}
			}
		}
	}

	slices.SortFunc(out, func(a, b Link) int { return strings.Compare(a.From+" "+a.To, b.From+" "+b.To) })

	return slices.CompactFunc(out, func(a, b Link) bool { return a.From == b.From && a.To == b.To }), nil
}

func keyedDocuments(n ast.Node, byDoc map[string]map[string]bool) []string {
	var out []string

	ast.Inspect(n, func(x ast.Node) bool {
		lit, ok := x.(*ast.CompositeLit)
		if !ok {
			return true
		}

		keys, ok := stringKeys(lit)
		if !ok || len(keys) < minMapKeys {
			return true
		}

		for doc, names := range byDoc {
			if !slices.Contains(out, doc) && !slices.ContainsFunc(keys, func(k string) bool { return !names[k] }) {
				out = append(out, doc)
			}
		}

		return true
	})

	slices.Sort(out)

	return out
}

// stringKeys returns a composite literal's keys when every element is a
// key and every key a string literal: only a map literal looks like that,
// whatever its type is called, so no type checking is needed.
func stringKeys(lit *ast.CompositeLit) ([]string, bool) {
	keys := make([]string, 0, len(lit.Elts))

	for _, e := range lit.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			return nil, false
		}

		b, ok := kv.Key.(*ast.BasicLit)
		if !ok || b.Kind != token.STRING {
			return nil, false
		}

		k, err := strconv.Unquote(b.Value)
		if err != nil {
			return nil, false
		}

		keys = append(keys, k)
	}

	return keys, true
}
