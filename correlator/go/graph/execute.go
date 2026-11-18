package graph

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strconv"
)

// A template looks up by reflection every name its text holds after a dot.
// The names of text the scan reads are in N, so data handed to such a
// template binds through N alone; a template whose text the scan did not
// read binds its data's whole method set. A template counts as read only
// where the package's own code shows it comes from a parse of text the scan
// read and is handed nowhere the reading can't follow.
func templateMethod(fn *types.Func) bool {
	if fn == nil || fn.Pkg() == nil || (fn.Pkg().Path() != textTemplate && fn.Pkg().Path() != htmlTemplate) {
		return false
	}

	recv := fn.Signature().Recv()
	if recv == nil {
		return false
	}

	tt := recv.Type()
	if p, ok := tt.(*types.Pointer); ok {
		tt = p.Elem()
	}

	named, ok := types.Unalias(tt).(*types.Named)

	return ok && named.Obj().Name() == "Template"
}

func templateFunc(fn *types.Func) bool {
	return fn != nil && fn.Pkg() != nil && (fn.Pkg().Path() == textTemplate || fn.Pkg().Path() == htmlTemplate) &&
		fn.Signature().Recv() == nil
}

const (
	execute         = "Execute"
	executeTemplate = "ExecuteTemplate"
)

func isExecute(name string) bool { return name == execute || name == executeTemplate }

// executeSignature tells whether an interface's method has the signature
// of a template's Execute or ExecuteTemplate, so a template may be what
// the interface holds.
func executeSignature(fn *types.Func) bool {
	if !isExecute(fn.Name()) {
		return false
	}

	sig := fn.Signature()
	ps := sig.Params()

	want := 2
	if fn.Name() == executeTemplate {
		want = 3
	}

	if ps.Len() != want || sig.Results().Len() != 1 || types.TypeString(sig.Results().At(0).Type(), nil) != "error" {
		return false
	}

	return types.TypeString(ps.At(0).Type(), nil) == "io.Writer" && emptyInterface(ps.At(ps.Len()-1).Type()) &&
		(want == 2 || types.TypeString(ps.At(1).Type(), nil) == toString)
}

// unreadExecute tells, for a call, whether it executes a template whose
// text the scan did not read, and why: the data it hands over then binds
// whole.
func (h *handoff) unreadExecute(c *ast.CallExpr) (bool, string) {
	sel, ok := ast.Unparen(c.Fun).(*ast.SelectorExpr)
	if !ok {
		return false, ""
	}

	fn, ok := h.info.Uses[sel.Sel].(*types.Func)
	if !ok || !isExecute(fn.Name()) {
		return false, ""
	}

	if recv := fn.Signature().Recv(); recv != nil && types.IsInterface(recv.Type()) {
		if executeSignature(fn) {
			return true, "an interface's " + fn.Name() + ", which a template the scan did not read may implement"
		}

		return false, ""
	}

	if !templateMethod(fn) {
		return false, ""
	}

	if s := h.info.Selections[sel]; s == nil || s.Kind() != types.MethodVal {
		// template.(*Template).Execute(t, w, data): what t is isn't told
		return true, "a method expression"
	}

	why := h.templateOf(h.w, sel.X)

	return why != "", why
}

// templateOf says why the template x is not one whose text the scan read,
// or "" when it is.
func (h *handoff) templateOf(w work, x ast.Expr) string {
	tr := &templateReading{h: h, seen: map[string]bool{}}

	return tr.expr(h.t.resolver.index(w), x)
}

type templateReading struct {
	h    *handoff
	seen map[string]bool
}

func (tr *templateReading) at(n ast.Node, why string) string {
	p := tr.h.fset.Position(n.Pos())

	return why + " at " + filepath.Base(p.Filename) + ":" + strconv.Itoa(p.Line)
}

// sameDir tells whether a package is the one being read: what another
// package's code does is no input of this package's answer.
func (tr *templateReading) sameDir(pkg *types.Package) bool {
	if pkg == nil {
		return false
	}

	dir, ok := tr.h.t.mods.dirOf(pkg.Path())

	return ok && dir == tr.h.dir
}

func (tr *templateReading) expr(fi *fileIndex, x ast.Expr) string {
	x = ast.Unparen(x)
	info := fi.w.info

	if tv, ok := info.Types[x]; ok && tv.IsNil() {
		return ""
	}

	switch x := x.(type) {
	case *ast.CallExpr:
		return tr.call(fi, x)
	case *ast.Ident:
		v, ok := info.Uses[x].(*types.Var)
		if !ok {
			return tr.at(x, "a template that is no variable")
		}

		return tr.variable(fi, v, x)
	case *ast.SelectorExpr:
		v, ok := info.Uses[x.Sel].(*types.Var)
		if !ok {
			return tr.at(x, "a template the reading can't follow")
		}

		if !v.IsField() {
			return tr.at(x, "a variable of another package")
		}

		return tr.field(v, x)
	}

	return tr.at(x, "a template the reading can't follow")
}

func (tr *templateReading) call(fi *fileIndex, c *ast.CallExpr) string {
	fn, ok := funcOf(fi.w.info, c.Fun).(*types.Func)
	if !ok {
		return tr.at(c, "a template a func value returns")
	}

	sel, _ := ast.Unparen(c.Fun).(*ast.SelectorExpr) //nolint:errcheck // a method's receiver, when it is one

	switch {
	case templateFunc(fn):
		switch fn.Name() {
		case "Must":
			if len(c.Args) == 0 {
				return tr.at(c, "a template the reading can't follow")
			}

			return tr.expr(fi, c.Args[0])
		case "New":
			return ""
		case parseFS:
			if len(c.Args) > 0 && tr.embedded(fi, c.Args[0]) {
				return ""
			}
		}

		return tr.at(c, "a template parsed from text the scan did not read")
	case templateMethod(fn) && sel != nil:
		if why := tr.adds(fi, fn.Name(), c); why != "" {
			return why
		}

		return tr.expr(fi, sel.X)
	}

	if !tr.sameDir(fn.Pkg()) {
		if _, inTree := tr.h.t.mods.dirOf(fn.Pkg().Path()); inTree {
			return tr.at(c, "a template another package of the tree returns")
		}

		return tr.at(c, "a template a dependency returns")
	}

	return tr.results(c, fn.Origin())
}

// adds says why a method of a template called on it adds text the scan
// did not read to it, or "" when it adds none or only text the scan read.
func (tr *templateReading) adds(fi *fileIndex, name string, c *ast.CallExpr) string {
	switch name {
	case parseText:
		if len(c.Args) == 1 && tr.text(fi, c.Args[0]) {
			return ""
		}

		return tr.at(c, "a template parsed from text the scan did not read")
	case parseFS:
		if len(c.Args) > 0 && tr.embedded(fi, c.Args[0]) {
			return ""
		}

		return tr.at(c, "a template parsed from files the scan did not read")
	case parseFiles, parseGlob, addParseTree:
		return tr.at(c, "a template parsed from text the scan did not read")
	}

	return ""
}

// text tells whether a template's text is text the scan read: string
// literals, a constant of the package made of them, or a file the package
// embeds.
func (tr *templateReading) text(fi *fileIndex, x ast.Expr) bool {
	if _, ok := literalParts(x); ok {
		return true
	}

	switch x := ast.Unparen(x).(type) {
	case *ast.Ident:
		k, ok := fi.w.info.Uses[x].(*types.Const)
		if !ok {
			return false
		}

		if !tr.sameDir(k.Pkg()) {
			return false
		}

		for _, w := range tr.h.t.resolver.byDir[tr.h.dir] {
			if _, ok := constsOf(w.file)[k.Name()]; ok && k.Pos() >= w.file.FileStart && k.Pos() < w.file.FileEnd {
				return true
			}
		}
	case *ast.CallExpr:
		if len(x.Args) == 1 && identOf(x.Fun) == toString {
			return tr.embedded(fi, x.Args[0])
		}
	}

	return false
}

// embedded tells whether x is a variable of the package a //go:embed
// directive fills, whose files' names the scan reads.
func (tr *templateReading) embedded(fi *fileIndex, x ast.Expr) bool {
	id, ok := ast.Unparen(x).(*ast.Ident)
	if !ok {
		return false
	}

	v, ok := fi.w.info.Uses[id].(*types.Var)
	if !ok || !packageVar(v) {
		return false
	}

	if !tr.sameDir(v.Pkg()) {
		return false
	}

	for _, w := range tr.h.t.resolver.byDir[tr.h.dir] {
		if v.Pos() >= w.file.FileStart && v.Pos() < w.file.FileEnd && embedVars(w.file)[v.Name()] {
			return true
		}
	}

	return false
}

func (tr *templateReading) results(c *ast.CallExpr, fn *types.Func) string {
	key := "func:" + strconv.Itoa(int(fn.Pos()))
	if tr.seen[key] {
		return ""
	}

	tr.seen[key] = true

	if fn.Signature().Results().Len() == 0 {
		return tr.at(c, "a template the reading can't follow")
	}

	w, fd := tr.h.t.resolver.funcDecl(tr.h.dir, fn)
	if fd == nil || fd.Body == nil {
		return tr.at(c, "a template returned by a function whose body the reading can't follow")
	}

	fi := tr.h.t.resolver.index(w)
	n := fn.Signature().Results().Len()
	why := ""

	ast.Inspect(fd.Body, func(x ast.Node) bool {
		if why != "" {
			return false
		}

		switch x := x.(type) {
		case *ast.FuncLit:
			return false // its returns are its own
		case *ast.ReturnStmt:
			if len(x.Results) != n {
				why = tr.at(x, "a template returned in a way the reading can't follow")

				return false
			}

			why = tr.expr(fi, x.Results[0])
		}

		return true
	})

	return why
}

// variable reads a template variable: what each write puts in it, when
// every use of it is one the reading follows. A local variable is read in
// its function; an unexported package variable in every file of its
// package.
func (tr *templateReading) variable(fi *fileIndex, v *types.Var, at ast.Node) string {
	key := varKey(v)
	if tr.seen[key] {
		return ""
	}

	tr.seen[key] = true

	if !packageVar(v) {
		return tr.local(fi, v, at)
	}

	return tr.packageVar(v, at)
}

func (tr *templateReading) local(fi *fileIndex, v *types.Var, at ast.Node) string {
	def := fi.defs[v]
	if def == nil {
		return tr.at(at, "a template variable whose declaration the reading can't follow")
	}

	if why := tr.declared(fi, def); why != "" {
		return why
	}

	for _, u := range fi.uses[v] {
		if why := tr.use(fi, u, true); why != "" {
			return why
		}
	}

	return ""
}

func (tr *templateReading) packageVar(v *types.Var, at ast.Node) string {
	if v.Exported() {
		return tr.at(at, "a template in an exported variable, which other packages may write")
	}

	if !tr.sameDir(v.Pkg()) {
		return tr.at(at, "a template in a variable of another package")
	}

	same := func(o types.Object) bool {
		return o != nil && o.Pkg() != nil && o.Pkg().Path() == v.Pkg().Path() && o.Pos() == v.Pos()
	}

	found := false

	for _, w := range tr.h.t.resolver.byDir[tr.h.dir] {
		other := tr.h.t.resolver.index(w)

		if def := other.pkgDefs[v.Name()]; def != nil && same(w.info.Defs[def]) {
			found = true

			if why := tr.declared(other, def); why != "" {
				return why
			}
		}

		for _, u := range other.pkgUses[v.Name()] {
			if !same(w.info.Uses[u]) {
				continue
			}

			if why := tr.use(other, u, false); why != "" {
				return why
			}
		}
	}

	if !found {
		return tr.at(at, "a template variable whose declaration the reading can't follow")
	}

	return ""
}

func (tr *templateReading) declared(fi *fileIndex, def *ast.Ident) string {
	switch p := fi.parent[def].(type) {
	case *ast.ValueSpec:
		i := identIndex(p.Names, def)

		switch {
		case len(p.Values) == 0:
			return ""
		case len(p.Values) == len(p.Names):
			return tr.expr(fi, p.Values[i])
		case len(p.Values) == 1 && i == 0:
			// t, err = template.New(...).Parse(...)
			return tr.expr(fi, p.Values[0])
		}
	case *ast.AssignStmt:
		return tr.assigned(fi, p, exprIndex(p.Lhs, def), def)
	}

	return tr.at(def, "a template the reading can't follow")
}

func (tr *templateReading) assigned(fi *fileIndex, as *ast.AssignStmt, i int, at ast.Node) string {
	switch {
	case as.Tok != token.ASSIGN && as.Tok != token.DEFINE:
	case len(as.Lhs) == len(as.Rhs):
		return tr.expr(fi, as.Rhs[i])
	case len(as.Rhs) == 1 && i == 0:
		// t, err := template.New(...).Parse(...)
		return tr.expr(fi, as.Rhs[0])
	}

	return tr.at(at, "a template assigned in a way the reading can't follow")
}

// use reads one use of a template variable or field, n: a write reads
// what it writes; a method called on it must add no text the scan did not
// read, and what returns the same template, or one of its set, is read on;
// a local variable may be returned, a fresh template each time. Any other
// use could hand it to code that parses into it.
func (tr *templateReading) use(fi *fileIndex, n ast.Expr, local bool) string {
	switch p := fi.parent[n].(type) {
	case *ast.ParenExpr:
		return tr.use(fi, p, local)
	case *ast.AssignStmt:
		if i := exprIndex(p.Lhs, n); i >= 0 {
			return tr.assigned(fi, p, i, n)
		}

		if i := exprIndex(p.Rhs, n); i >= 0 && (p.Tok == token.ASSIGN || p.Tok == token.DEFINE) &&
			(len(p.Lhs) == len(p.Rhs) || i == 0) {
			return tr.alias(fi, p.Lhs[i])
		}
	case *ast.ValueSpec:
		if i := exprIndex(p.Values, n); i >= 0 && (len(p.Names) == len(p.Values) || i == 0) {
			return tr.alias(fi, p.Names[i])
		}
	case *ast.ExprStmt:
		return ""
	case *ast.BinaryExpr:
		if p.Op == token.EQL || p.Op == token.NEQ {
			return ""
		}
	case *ast.ReturnStmt:
		if local {
			return ""
		}
	case *ast.SelectorExpr:
		if p.X == n {
			return tr.method(fi, p, local)
		}
	case *ast.CallExpr:
		// template.Must(t, err): the same template
		if fn, ok := funcOf(fi.w.info, p.Fun).(*types.Func); ok && templateFunc(fn) && fn.Name() == "Must" &&
			len(p.Args) > 0 && p.Args[0] == n {
			return tr.use(fi, p, local)
		}
	}

	return tr.at(n, "a template handed where the reading can't follow")
}

// alias reads where a template is stored: another variable or a field,
// whose own writes and uses then count as the template's.
func (tr *templateReading) alias(fi *fileIndex, lhs ast.Expr) string {
	switch x := ast.Unparen(lhs).(type) {
	case *ast.Ident:
		if x.Name == "_" {
			return ""
		}

		o := fi.w.info.Uses[x]
		if o == nil {
			o = fi.w.info.Defs[x]
		}

		if v, ok := o.(*types.Var); ok {
			return tr.variable(fi, v, x)
		}
	case *ast.SelectorExpr:
		if v, ok := fi.w.info.Uses[x.Sel].(*types.Var); ok && v.IsField() {
			return tr.field(v, x)
		}
	}

	return tr.at(lhs, "a template stored where the reading can't follow")
}

func (tr *templateReading) method(fi *fileIndex, sel *ast.SelectorExpr, local bool) string {
	fn, ok := fi.w.info.Uses[sel.Sel].(*types.Func)
	if !ok || !templateMethod(fn) {
		return tr.at(sel, "a template handed where the reading can't follow")
	}

	c, ok := fi.parent[sel].(*ast.CallExpr)
	if !ok || c.Fun != sel {
		if isExecute(fn.Name()) {
			// a method value, called where the reading can't follow; the
			// call that takes it says what it executes
			return ""
		}

		return tr.at(sel, "a template's method taken as a value")
	}

	switch fn.Name() {
	case execute, executeTemplate, "Name", "DefinedTemplates", "Clone":
		return ""
	case parseText, parseFS, parseFiles, parseGlob, addParseTree:
		if why := tr.adds(fi, fn.Name(), c); why != "" {
			return why
		}

		return tr.use(fi, c, local)
	case "Funcs", "Option", "Delims", "New", "Lookup":
		// the same template, or one of its set
		return tr.use(fi, c, local)
	}

	return tr.at(sel, "a template's "+fn.Name()+", which the reading doesn't follow")
}

// field reads a template an unexported field of a type of the package
// holds: what each write of it in the package puts there, when every use
// is one the reading follows.
func (tr *templateReading) field(f *types.Var, at ast.Node) string {
	key := "field:" + strconv.Itoa(int(f.Pos()))
	if tr.seen[key] {
		return ""
	}

	tr.seen[key] = true

	if f.Exported() {
		return tr.at(at, "a template in an exported field, which other packages may write")
	}

	if !tr.sameDir(f.Pkg()) {
		return tr.at(at, "a template in a field of another package")
	}

	same := func(o types.Object) bool {
		v, ok := o.(*types.Var)

		return ok && v.IsField() && v.Name() == f.Name() && v.Pos() == f.Pos()
	}

	for _, w := range tr.h.t.resolver.byDir[tr.h.dir] {
		other := tr.h.t.resolver.index(w)

		for o, ids := range other.uses {
			if !same(o) {
				continue
			}

			for _, id := range ids {
				if why := tr.fieldUse(other, id); why != "" {
					return why
				}
			}
		}

		if why := tr.positional(other, same); why != "" {
			return why
		}
	}

	return ""
}

// fieldUse reads one use of a template field: a key of a composite
// literal writes it, a selection of it is a use of the template.
func (tr *templateReading) fieldUse(fi *fileIndex, id *ast.Ident) string {
	switch p := fi.parent[id].(type) {
	case *ast.KeyValueExpr:
		if p.Key == id {
			return tr.expr(fi, p.Value)
		}
	case *ast.SelectorExpr:
		if p.Sel == id {
			return tr.use(fi, p, false)
		}
	}

	return tr.at(id, "a template field used where the reading can't follow")
}

func (tr *templateReading) positional(fi *fileIndex, same func(types.Object) bool) string {
	why := ""

	ast.Inspect(fi.w.file, func(n ast.Node) bool {
		c, ok := n.(*ast.CompositeLit)
		if why != "" || !ok || len(c.Elts) == 0 {
			return why == ""
		}

		if _, keyed := c.Elts[0].(*ast.KeyValueExpr); keyed {
			return true
		}

		tt := fi.w.info.TypeOf(c)
		if tt == nil {
			return true
		}

		st, ok := tt.Underlying().(*types.Struct)
		if !ok {
			return true
		}

		for i := range st.NumFields() {
			if same(st.Field(i)) && i < len(c.Elts) {
				why = tr.expr(fi, c.Elts[i])
			}
		}

		return why == ""
	})

	return why
}
