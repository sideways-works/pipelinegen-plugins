package goid

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Decl is one top-level declaration of a file.
type Decl struct {
	ID ID
	// Node is the declaration: an *ast.FuncDecl, or one *ast.TypeSpec or
	// *ast.ValueSpec of Gen.
	Node ast.Node
	// Gen is the const, var or type declaration a spec sits in.
	Gen *ast.GenDecl
	// Recv is a method's receiver type name, without * and type parameters.
	Recv string
	// Pos and End bound the declaration, the spec's group included when it
	// is the group's only spec.
	Pos, End token.Pos
}

// File lists a parsed file's top-level declarations. dir is its package's
// directory from the repository's root, name the file's base name.
func File(dir, name string, f *ast.File) []Decl {
	suffix := ""

	if strings.HasSuffix(name, "_test.go") {
		suffix = TestSuffix
		if strings.HasSuffix(f.Name.Name, "_test") {
			suffix = XTestSuffix
		}
	}

	var (
		out          []Decl
		inits, blank int
	)

	for _, decl := range f.Decls {
		switch decl := decl.(type) {
		case *ast.FuncDecl:
			d := Decl{Node: decl, Pos: decl.Pos(), End: decl.End()}

			switch {
			case decl.Recv != nil && len(decl.Recv.List) > 0:
				d.Recv = RecvName(decl.Recv.List[0].Type)
				d.ID = New(dir, Method, d.Recv+"."+decl.Name.Name+suffix)
			case decl.Name.Name == "init":
				inits++
				d.ID = New(dir, Init, name+":"+strconv.Itoa(inits)+suffix)
			default:
				d.ID = New(dir, Func, decl.Name.Name+suffix)
			}

			out = append(out, d)
		case *ast.GenDecl:
			// imports belong to the file, not to a declaration
			if decl.Tok != token.IMPORT {
				out = append(out, specs(dir, name, suffix, decl, &blank)...)
			}
		}
	}

	return out
}

func specs(dir, file, suffix string, g *ast.GenDecl, blank *int) []Decl {
	out := make([]Decl, 0, len(g.Specs))

	for _, spec := range g.Specs {
		d := Decl{Node: spec, Gen: g, Pos: spec.Pos(), End: spec.End()}
		if len(g.Specs) == 1 {
			d.Pos, d.End = g.Pos(), g.End()
		}

		switch spec := spec.(type) {
		case *ast.TypeSpec:
			d.ID = New(dir, Type, spec.Name.Name+suffix)
		case *ast.ValueSpec:
			kind := Var
			if g.Tok == token.CONST {
				kind = Const
			}

			names := make([]string, len(spec.Names))
			for i, n := range spec.Names {
				names[i] = n.Name
			}

			name := strings.Join(names, ",")
			if name == "_" {
				*blank++
				name = "_:" + file + ":" + strconv.Itoa(*blank)
			}

			d.ID = New(dir, kind, name+suffix)
		}

		out = append(out, d)
	}

	return out
}

// RecvName is a receiver's type name without *, parentheses or type parameters.
func RecvName(e ast.Expr) string {
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.ParenExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.Ident:
			return t.Name
		default:
			return "?"
		}
	}
}

// Index finds the declaration around a position, across files.
type Index struct {
	byFile map[string][]entry
}

type entry struct {
	id         ID
	start, end token.Position
}

// NewIndex makes an empty index.
func NewIndex() *Index {
	return &Index{byFile: map[string][]entry{}}
}

// Add takes in a file's declarations.
func (x *Index) Add(fset *token.FileSet, decls []Decl) {
	touched := map[string]bool{}

	for _, d := range decls {
		e := entry{id: d.ID, start: fset.Position(d.Pos), end: fset.Position(d.End)}
		file := filepath.Clean(e.start.Filename)
		x.byFile[file] = append(x.byFile[file], e)
		touched[file] = true
	}

	// only the files this call added to need sorting again: a tree adds
	// one file at a time, thousands of times
	for f := range touched {
		slices.SortFunc(x.byFile[f], func(a, b entry) int { return compare(a.start, b.start) })
	}
}

// At is the declaration holding a position: by file name, line and
// column, so positions of one file read from several file sets meet.
func (x *Index) At(p token.Position) (ID, bool) {
	es := x.byFile[filepath.Clean(p.Filename)]

	i, _ := slices.BinarySearchFunc(es, p, func(e entry, p token.Position) int { return compare(e.start, p) })
	if i < len(es) && compare(es[i].start, p) == 0 {
		return es[i].id, true
	}

	if i > 0 && compare(p, es[i-1].end) <= 0 {
		return es[i-1].id, true
	}

	return "", false
}

// Object is the declaration of a type checker's object. A generic's instance
// maps to the generic declaration, and an object declared inside a function
// to that function.
func (x *Index) Object(fset *token.FileSet, obj types.Object) (ID, bool) {
	if obj == nil || !obj.Pos().IsValid() {
		return "", false
	}

	switch o := obj.(type) {
	case *types.Func:
		obj = o.Origin()
	case *types.Var:
		obj = o.Origin()
	}

	return x.At(fset.Position(obj.Pos()))
}

func compare(a, b token.Position) int {
	if a.Line != b.Line {
		return a.Line - b.Line
	}

	return a.Column - b.Column
}
