package graph

import (
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
)

// The names of the template packages and of reflection's method lookups.
const (
	textTemplate = "text/template"
	htmlTemplate = "html/template"
	methodByName = "MethodByName"
	methodSel    = "Method"

	// the template methods and functions that add text to a template.
	parseText    = "Parse"
	parseFS      = "ParseFS"
	parseFiles   = "ParseFiles"
	parseGlob    = "ParseGlob"
	addParseTree = "AddParseTree"

	// the conversion string(v) of an embedded variable's bytes.
	toString = "string"
)

// fileFacts are what one file says about N, read from syntax alone. Syntax
// can't tell reflection from another API with a method of the same name, so
// such calls count unless a shape below proves otherwise: more than
// reflection, never less.
type fileFacts struct {
	// names are the method names of every interface type the file writes
	// and every literal name it looks a method up by.
	names []string
	// literals are the names after a dot in its string literals: template
	// text, when the package parses templates.
	literals []string
	consts   map[string][]string
	// sites are the places that enumerate methods (Method(i), NumMethod)
	// or look one up by a name no literal gives; parses, the template
	// parses of text that is no literal.
	sites, parses []site
	templates     bool
	embeds        map[string]bool
	// others are the names of the packages it imports other than the
	// template packages, which nothing of the file declares again: a
	// Parse called on one of them (url.Parse) parses no template.
	others map[string]bool
}

// site is one place; ident, when set, is the name of the constant its
// argument names, which the package's constants may resolve.
type site struct {
	pos   token.Pos
	ident string
	// embed is the variable a template's text comes from, string(v) or
	// ParseFS(v, ...): text a //go:embed of the package holds, whose names
	// count, when v is such a variable.
	embed string
}

func scanFile(f *ast.File) fileFacts {
	out := fileFacts{consts: constsOf(f), embeds: embedVars(f), others: otherImports(f)}

	for _, im := range f.Imports {
		if p, err := strconv.Unquote(im.Path.Value); err == nil && (p == textTemplate || p == htmlTemplate) {
			out.templates = true
		}
	}

	var stack []ast.Node

	ast.Inspect(f, func(x ast.Node) bool {
		if x == nil {
			stack = stack[:len(stack)-1]

			return true
		}

		switch x := x.(type) {
		case *ast.InterfaceType:
			for _, m := range x.Methods.List {
				for _, name := range m.Names {
					out.names = append(out.names, name.Name)
				}
			}
		case *ast.BasicLit:
			if s, ok := stringLit(x); ok {
				out.literals = append(out.literals, dotNames(s)...)
			}
		case *ast.CallExpr:
			out.call(x, stack)
		}

		stack = append(stack, x)

		return true
	})

	return out
}

func constsOf(f *ast.File) map[string][]string {
	out := map[string][]string{}

	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}

		for _, spec := range g.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Values) != len(vs.Names) {
				continue
			}

			for i, name := range vs.Names {
				if parts, ok := literalParts(vs.Values[i]); ok {
					out[name.Name] = parts
				}
			}
		}
	}

	return out
}

func embedVars(f *ast.File) map[string]bool {
	out := map[string]bool{}

	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.VAR {
			continue
		}

		for _, spec := range g.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || (!hasEmbed(g.Doc) && !hasEmbed(vs.Doc)) {
				continue
			}

			for _, n := range vs.Names {
				out[n.Name] = true
			}
		}
	}

	return out
}

func hasEmbed(g *ast.CommentGroup) bool {
	if g == nil {
		return false
	}

	for _, c := range g.List {
		if strings.HasPrefix(c.Text, "//go:embed ") {
			return true
		}
	}

	return false
}

func literalParts(x ast.Expr) ([]string, bool) {
	switch x := ast.Unparen(x).(type) {
	case *ast.BasicLit:
		s, ok := stringLit(x)

		return []string{s}, ok
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return nil, false
		}

		a, ok := literalParts(x.X)
		if !ok {
			return nil, false
		}

		b, ok := literalParts(x.Y)

		return append(a, b...), ok
	}

	return nil, false
}

func stringLit(x ast.Expr) (string, bool) {
	lit, ok := ast.Unparen(x).(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}

	s, err := strconv.Unquote(lit.Value)

	return s, err == nil
}

// otherImports are the names a file imports packages other than the template
// packages by, less every name the file declares (a variable may shadow an
// import).
func otherImports(f *ast.File) map[string]bool {
	out := map[string]bool{}

	for _, im := range f.Imports {
		p, err := strconv.Unquote(im.Path.Value)
		if err != nil || p == textTemplate || p == htmlTemplate {
			continue
		}

		name := path.Base(p)
		if im.Name != nil {
			name = im.Name.Name
		}

		if token.IsIdentifier(name) && name != "_" {
			out[name] = true
		}
	}

	for _, name := range declared(f) {
		delete(out, name)
	}

	return out
}

func declared(f *ast.File) []string {
	var out []string

	ast.Inspect(f, func(x ast.Node) bool {
		var ids []*ast.Ident

		switch x := x.(type) {
		case *ast.AssignStmt:
			if x.Tok == token.DEFINE {
				for _, l := range x.Lhs {
					if id, ok := l.(*ast.Ident); ok {
						ids = append(ids, id)
					}
				}
			}
		case *ast.ValueSpec:
			ids = x.Names
		case *ast.TypeSpec:
			ids = []*ast.Ident{x.Name}
		case *ast.FuncDecl:
			ids = []*ast.Ident{x.Name}
		case *ast.Field:
			ids = x.Names
		case *ast.RangeStmt:
			for _, e := range []ast.Expr{x.Key, x.Value} {
				if id, ok := e.(*ast.Ident); ok {
					ids = append(ids, id)
				}
			}
		}

		for _, id := range ids {
			out = append(out, id.Name)
		}

		return true
	})

	return out
}

// call notes a call that looks methods up or parses a template; stack
// holds its ancestors, the innermost last.
func (ff *fileFacts) call(c *ast.CallExpr, stack []ast.Node) {
	sel, ok := ast.Unparen(c.Fun).(*ast.SelectorExpr)
	if !ok {
		return
	}

	// a function of another package: url.Parse parses no template
	if id, ok := sel.X.(*ast.Ident); ok && ff.others[id.Name] && strings.HasPrefix(sel.Sel.Name, "Parse") {
		return
	}

	switch sel.Sel.Name {
	case methodByName:
		if len(c.Args) != 1 {
			return
		}

		if s, ok := stringLit(c.Args[0]); ok {
			ff.names = append(ff.names, s)

			return
		}

		ff.sites = append(ff.sites, site{pos: c.Pos(), ident: identOf(c.Args[0])})
	case "NumMethod":
		if len(c.Args) == 0 && !interfaceOnly(sel.X, stack) && !gateOnly(c, stack) {
			ff.sites = append(ff.sites, site{pos: c.Pos()})
		}
	case methodSel:
		if len(c.Args) == 1 && !interfaceOnly(sel.X, stack) {
			ff.sites = append(ff.sites, site{pos: c.Pos()})
		}
	case parseText:
		if len(c.Args) != 1 {
			return
		}

		if _, ok := literalParts(c.Args[0]); ok {
			return
		}

		s := site{pos: c.Pos(), ident: identOf(c.Args[0])}
		if conv, ok := ast.Unparen(c.Args[0]).(*ast.CallExpr); ok && len(conv.Args) == 1 && identOf(conv.Fun) == toString {
			s.embed = identOf(conv.Args[0])
		}

		ff.parses = append(ff.parses, s)
	case parseFS:
		s := site{pos: c.Pos()}
		if len(c.Args) > 0 {
			s.embed = identOf(c.Args[0])
		}

		ff.parses = append(ff.parses, s)
	case parseFiles, parseGlob, addParseTree:
		ff.parses = append(ff.parses, site{pos: c.Pos()})
	}
}

func identOf(x ast.Expr) string {
	if id, ok := ast.Unparen(x).(*ast.Ident); ok {
		return id.Name
	}

	return ""
}

// interfaceOnly tells whether a call on recv can only ever read an
// interface type: its && chain checks recv.Kind() == reflect.Interface, or
// it sits in the reflect.Interface case of a switch on recv.Kind(). An
// interface's methods are its own names, which N holds.
func interfaceOnly(recv ast.Expr, stack []ast.Node) bool {
	for i, n := range slices.Backward(stack) {
		switch n := n.(type) {
		case *ast.BinaryExpr:
			if n.Op != token.LAND {
				continue
			}

			for _, term := range andTerms(n) {
				if kindIsInterface(term, recv) {
					return true
				}
			}
		case *ast.CaseClause:
			if i < 2 || len(n.List) != 1 || !isReflect(n.List[0], "Interface") {
				continue
			}

			if sw, ok := stack[i-2].(*ast.SwitchStmt); ok && sw.Init == nil && kindOf(sw.Tag, recv) {
				return true
			}
		}
	}

	return false
}

func andTerms(x ast.Expr) []ast.Expr {
	if b, ok := ast.Unparen(x).(*ast.BinaryExpr); ok && b.Op == token.LAND {
		return append(andTerms(b.X), andTerms(b.Y)...)
	}

	return []ast.Expr{x}
}

func kindIsInterface(term, recv ast.Expr) bool {
	b, ok := ast.Unparen(term).(*ast.BinaryExpr)
	if !ok || b.Op != token.EQL {
		return false
	}

	return (kindOf(b.X, recv) && isReflect(b.Y, "Interface")) || (kindOf(b.Y, recv) && isReflect(b.X, "Interface"))
}

// kindOf tells whether x is y.Kind() for a y that recv is, or whose
// Type() recv is: a reflect.Value of kind Interface has an interface type.
func kindOf(x, recv ast.Expr) bool {
	c, ok := ast.Unparen(x).(*ast.CallExpr)
	if !ok || len(c.Args) != 0 {
		return false
	}

	sel, ok := ast.Unparen(c.Fun).(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Kind" {
		return false
	}

	y := types.ExprString(sel.X)
	if types.ExprString(recv) == y {
		return true
	}

	if rc, ok := ast.Unparen(recv).(*ast.CallExpr); ok && len(rc.Args) == 0 {
		if rs, ok := ast.Unparen(rc.Fun).(*ast.SelectorExpr); ok && rs.Sel.Name == "Type" {
			return types.ExprString(rs.X) == y
		}
	}

	return false
}

func isReflect(x ast.Expr, name string) bool {
	sel, ok := ast.Unparen(x).(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}

	id, ok := sel.X.(*ast.Ident)

	return ok && id.Name == reflectPkg
}

// gateOnly tells whether a NumMethod call only gates type assertions:
//
//	if v.Type().NumMethod() > 0 && v.CanInterface() {
//		if u, ok := v.Interface().(Binder); ok { ... }
//	}
//
// The gate is false only for a type without exported methods, and then
// every assertion to an interface with methods fails too, so the code
// runs the same with or without the gate: what it reads of a type's
// methods are the asserted interfaces' names, which N holds.
func gateOnly(c *ast.CallExpr, stack []ast.Node) bool {
	var cond ast.Expr

	for i, n := range slices.Backward(stack) {
		switch n := n.(type) {
		case *ast.BinaryExpr, *ast.ParenExpr:
			continue
		case *ast.IfStmt:
			if i+1 < len(stack) && stack[i+1] == n.Cond {
				cond = n.Cond
				if n.Init != nil || n.Else != nil || !assertionsOnly(n.Body) {
					return false
				}
			}
		}

		break
	}

	if cond == nil {
		return false
	}

	for _, term := range andTerms(cond) {
		switch {
		case positiveCount(term, c):
		case pureCheck(term):
		default:
			return false
		}
	}

	return true
}

func positiveCount(term ast.Expr, c *ast.CallExpr) bool {
	b, ok := ast.Unparen(term).(*ast.BinaryExpr)
	if !ok || (b.Op != token.GTR && b.Op != token.NEQ) || ast.Unparen(b.X) != c {
		return false
	}

	lit, ok := ast.Unparen(b.Y).(*ast.BasicLit)

	return ok && lit.Kind == token.INT && lit.Value == "0"
}

func pureCheck(term ast.Expr) bool {
	c, ok := ast.Unparen(term).(*ast.CallExpr)
	if !ok || len(c.Args) != 0 {
		return false
	}

	sel, ok := ast.Unparen(c.Fun).(*ast.SelectorExpr)

	return ok && (sel.Sel.Name == "CanInterface" || sel.Sel.Name == "IsValid")
}

func assertionsOnly(b *ast.BlockStmt) bool {
	if len(b.List) == 0 {
		return false
	}

	for _, s := range b.List {
		is, ok := s.(*ast.IfStmt)
		if !ok || is.Else != nil {
			return false
		}

		as, ok := is.Init.(*ast.AssignStmt)
		if !ok || as.Tok != token.DEFINE || len(as.Lhs) != 2 || len(as.Rhs) != 1 {
			return false
		}

		ta, ok := ast.Unparen(as.Rhs[0]).(*ast.TypeAssertExpr)
		if !ok || ta.Type == nil {
			return false
		}

		okName, ok := as.Lhs[1].(*ast.Ident)
		if !ok {
			return false
		}

		if id, ok := ast.Unparen(is.Cond).(*ast.Ident); !ok || id.Name != okName.Name {
			return false
		}
	}

	return true
}

type packageFacts struct {
	names, literals []string
	sites           []token.Pos
	templates       bool
}

func joinFiles(files []fileFacts) packageFacts {
	var out packageFacts

	consts := map[string][]string{}
	embeds := map[string]bool{}

	for _, f := range files {
		maps.Copy(consts, f.consts)
		maps.Copy(embeds, f.embeds)

		out.templates = out.templates || f.templates
	}

	for _, f := range files {
		out.names = append(out.names, f.names...)
		out.literals = append(out.literals, f.literals...)

		for _, s := range f.sites {
			if v, ok := consts[s.ident]; ok && s.ident != "" {
				// MethodByName(name) of a constant: its value is a name
				out.names = append(out.names, v...)

				continue
			}

			out.sites = append(out.sites, s.pos)
		}

		if !f.templates {
			continue
		}

		for _, s := range f.parses {
			if _, ok := consts[s.ident]; ok && s.ident != "" {
				// a constant's text is literals of the package, whose names
				// count when the package parses templates
				continue
			}

			if embeds[s.embed] && s.embed != "" {
				// text the package embeds, whose names count too
				continue
			}

			out.sites = append(out.sites, s.pos)
		}
	}

	return out
}
