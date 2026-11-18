package graph

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/sideways-works/pipelinegen-plugins/sbom/go/decls"
)

// Readers are the package directories whose initialisers' purity proofs read
// one of paths, besides those holding the paths. It reads syntax only and
// over-approximates: a package it can't follow (a dot import) is a reader
// whenever an initialiser of it calls anything.
func Readers(root string, o Options, paths []string) ([]string, error) {
	root, err := decls.Root(root)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLoad, err)
	}

	asked, gone := map[string]bool{}, map[string]bool{}

	for _, p := range paths {
		p = path.Clean(filepath.ToSlash(p))
		if (!strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go")) && path.Base(p) != "go.mod" {
			continue
		}

		asked[p] = true

		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(p))); err != nil {
			gone[path.Dir(p)] = true
		}
	}

	if len(asked) == 0 {
		return nil, nil
	}

	mods, err := findModules(root)
	if err != nil {
		return nil, err
	}

	t := newTree(root, mods, o)

	var out []string

	for _, m := range mods {
		if len(o.Modules) > 0 && !slices.Contains(o.Modules, m.Dir) {
			continue
		}

		dirs, err := decls.Dirs(root, m.Dir)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrLoad, err)
		}

		for _, dir := range dirs {
			if t.reads(dir, importPathOf(m, dir), asked, gone) {
				out = append(out, dir)
			}
		}
	}

	return out, nil
}

func importPathOf(m module, dir string) string {
	if dir == m.Dir {
		return m.Path
	}

	rel := strings.TrimPrefix(dir, m.Dir+"/")
	if m.Dir == "." {
		rel = dir
	}

	return m.Path + "/" + rel
}

func (t *Tree) reads(dir, importPath string, asked, gone map[string]bool) bool {
	abs := filepath.Join(t.Root, dir)
	ctx := t.build.Context()
	fset := token.NewFileSet()

	entries, err := os.ReadDir(abs)
	if err != nil {
		return false
	}

	for _, en := range entries {
		name := en.Name()
		if en.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}

		if ok, err := ctx.MatchFile(abs, name); err != nil || !ok {
			continue
		}

		f, err := parser.ParseFile(fset, filepath.Join(abs, name), nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}

		pkgPath := importPath
		if strings.HasSuffix(f.Name.Name, "_test") && strings.HasSuffix(name, "_test.go") {
			pkgPath += "_test"
		}

		if t.fileReads(f, pkgPath, asked, gone) {
			return true
		}
	}

	return false
}

func (t *Tree) fileReads(f *ast.File, pkgPath string, asked, gone map[string]bool) bool {
	imports := t.importNames(f)
	dot := slices.ContainsFunc(f.Imports, func(im *ast.ImportSpec) bool { return im.Name != nil && im.Name.Name == "." })

	for _, call := range initialiserCalls(f) {
		if dot {
			return true
		}

		read := map[string]bool{}

		switch fun := ast.Unparen(call.Fun).(type) {
		case *ast.Ident:
			t.purity.call(pkgPath, fun.Name, read)
		case *ast.SelectorExpr:
			if id, ok := fun.X.(*ast.Ident); ok && imports[id.Name] != "" {
				t.purity.call(imports[id.Name], fun.Sel.Name, read)
			}
		}

		for r := range read {
			if asked[r] || gone[path.Dir(r)] {
				return true
			}
		}
	}

	return false
}

// initialiserCalls are the calls in a file's package-level initialisers,
// function literals' bodies included, as the type-checked rule reads them.
func initialiserCalls(f *ast.File) []*ast.CallExpr {
	var out []*ast.CallExpr

	for _, decl := range f.Decls {
		g, ok := decl.(*ast.GenDecl)
		if !ok || g.Tok != token.VAR {
			continue
		}

		for _, sp := range g.Specs {
			vs, ok := sp.(*ast.ValueSpec)
			if !ok {
				continue
			}

			for _, v := range vs.Values {
				ast.Inspect(v, func(n ast.Node) bool {
					if call, ok := n.(*ast.CallExpr); ok {
						out = append(out, call)
					}

					return true
				})
			}
		}
	}

	return out
}

// importNames are a file's imports by the name they go by, a package of
// the tree's by the name its own files declare: the type checker resolves
// a call by that name, whatever the path's last element suggests.
func (t *Tree) importNames(f *ast.File) map[string]string {
	out := map[string]string{}

	for _, im := range f.Imports {
		p, err := strconv.Unquote(im.Path.Value)
		if err != nil {
			continue
		}

		n := guessName(p)
		if pkg := t.purity.pkg(p); pkg != nil && pkg.name != "" {
			n = pkg.name
		}

		if im.Name != nil {
			n = im.Name.Name
		}

		if n != "_" && n != "." {
			out[n] = p
		}
	}

	return out
}
