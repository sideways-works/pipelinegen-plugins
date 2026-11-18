package analysis

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"github.com/sideways-works/pipelinegen-plugins/correlator/gorm/internal/sqlrefs"
)

// maxVariants caps how many strings an expression may stand for; past it
// the code is treated as building its SQL at run time.
const maxVariants = 64

// maxDepth caps how many calls a parameter's values are followed through.
const maxDepth = 3

type assignment struct {
	// expr is the value; nil when the code can't say (a range variable).
	expr  ast.Expr
	add   bool
	multi *multiResult
}

type multiResult struct {
	ast.Expr

	call  ast.Expr
	index int
}

func (c *declCtx) assigns() map[types.Object][]assignment {
	if c.assign != nil {
		return c.assign
	}

	c.assign = map[types.Object][]assignment{}

	ast.Inspect(c.d.Node, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			c.assignStmt(n)
		case *ast.ValueSpec:
			// a declaration without values starts from the zero value,
			// which names no table and runs no SQL
			lhs := make([]ast.Expr, len(n.Names))
			for i, id := range n.Names {
				lhs[i] = id
			}

			c.assignAll(lhs, n.Values)
		case *ast.RangeStmt:
			for _, x := range []ast.Expr{n.Key, n.Value} {
				c.assignTo(x, assignment{})
			}
		}

		return true
	})

	return c.assign
}

func (c *declCtx) assignStmt(n *ast.AssignStmt) {
	switch n.Tok {
	case token.ASSIGN, token.DEFINE:
		c.assignAll(n.Lhs, n.Rhs)
	case token.ADD_ASSIGN:
		if len(n.Lhs) == 1 && len(n.Rhs) == 1 {
			c.assignTo(n.Lhs[0], assignment{expr: n.Rhs[0], add: true})
		}
	default:
		for _, l := range n.Lhs {
			c.assignTo(l, assignment{})
		}
	}
}

func (c *declCtx) assignAll(lhs, rhs []ast.Expr) {
	if len(rhs) == 0 {
		return
	}

	for i, l := range lhs {
		if len(lhs) == len(rhs) {
			c.assignTo(l, assignment{expr: rhs[i]})
		} else {
			c.assignTo(l, assignment{multi: &multiResult{call: rhs[0], index: i}})
		}
	}
}

func (c *declCtx) assignTo(l ast.Expr, a assignment) {
	id, ok := ast.Unparen(l).(*ast.Ident)
	if !ok || id.Name == "_" {
		return
	}

	o := c.info.Defs[id]
	if o == nil {
		o = c.info.Uses[id]
	}

	if o != nil {
		c.assign[o] = append(c.assign[o], a)
	}
}

func (c *declCtx) str(e ast.Expr) ([]string, bool) {
	return c.values(e, 0)
}

// values are the strings an expression may hold, from constants through
// concatenation, local variables, fmt.Sprintf, strings.Join and callers'
// arguments. Combinations that aren't SQL are dropped when parsing, since
// PostgreSQL would refuse them too.
func (c *declCtx) values(e ast.Expr, depth int) ([]string, bool) {
	if s, ok := constString(c.info, e); ok {
		return []string{s}, true
	}

	switch e := ast.Unparen(e).(type) {
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return nil, false
		}

		return c.product(depth, e.X, e.Y)
	case *ast.Ident:
		return c.varValues(e, depth)
	case *ast.CallExpr:
		return c.callValues(e, depth)
	}

	return nil, false
}

func (c *declCtx) product(depth int, es ...ast.Expr) ([]string, bool) {
	return c.concat(depth, false, es)
}

// concat is every concatenation of the expressions' values; with optional
// set, each expression may also be left out (a piece appended in a branch
// or a loop).
func (c *declCtx) concat(depth int, optional bool, es []ast.Expr) ([]string, bool) {
	out := []string{""}

	for _, e := range es {
		vs, ok := c.values(e, depth)
		if !ok {
			return nil, false
		}

		if optional {
			vs = append(vs, "")
		}

		next := make([]string, 0, len(out)*len(vs))

		for _, a := range out {
			for _, b := range vs {
				next = append(next, a+b)
			}
		}

		if len(next) > maxVariants {
			return nil, false
		}

		out = dedupe(next)
	}

	return out, true
}

func dedupe(vs []string) []string {
	seen := make(map[string]bool, len(vs))
	out := vs[:0]

	for _, v := range vs {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}

	return out
}

func (c *declCtx) varValues(id *ast.Ident, depth int) ([]string, bool) {
	v, ok := c.info.Uses[id].(*types.Var)
	if !ok || c.busy[v] {
		return nil, false
	}

	as, assigned := c.assigns()[v]
	if !assigned {
		if c.isParam(v) {
			return c.a.paramValues(c, v, depth)
		}

		return nil, false
	}

	c.busy[v] = true
	defer delete(c.busy, v)

	var (
		bases  []string
		pieces []ast.Expr
	)

	for _, a := range as {
		switch {
		case a.expr == nil:
			return nil, false
		case a.add:
			pieces = append(pieces, a.expr)
		default:
			vs, ok := c.values(a.expr, depth)
			if !ok {
				return nil, false
			}

			bases = append(bases, vs...)
		}
	}

	if len(bases) == 0 {
		bases = []string{""}
	}

	tails, ok := c.concat(depth, true, pieces)
	if !ok || len(bases)*len(tails) > maxVariants {
		return nil, false
	}

	var out []string

	for _, b := range bases {
		for _, t := range tails {
			out = append(out, b+t)
		}
	}

	return out, true
}

func (c *declCtx) callValues(call *ast.CallExpr, depth int) ([]string, bool) {
	fn := staticCallee(c.info, call)
	if fn == nil || fn.Pkg() == nil {
		return nil, false
	}

	switch fn.Pkg().Path() + "." + fn.Name() {
	case "fmt.Sprintf":
		return c.sprintf(call, depth)
	case "strings.Join":
		return c.join(call, depth)
	case "strconv.Itoa", "strconv.FormatInt", "strconv.FormatUint":
		return []string{"0"}, true
	case "strings.Repeat":
		if len(call.Args) == 2 {
			return c.values(call.Args[0], depth)
		}
	}

	return nil, false
}

// sprintf formats every combination of the arguments' values; a number,
// a boolean or a time formats as its zero value, which names no relation.
func (c *declCtx) sprintf(call *ast.CallExpr, depth int) ([]string, bool) {
	if len(call.Args) == 0 || call.Ellipsis.IsValid() {
		return nil, false
	}

	formats, ok := c.values(call.Args[0], depth)
	if !ok {
		return nil, false
	}

	combos := [][]any{nil}

	for _, a := range call.Args[1:] {
		alts, ok := c.argValues(a, depth)
		if !ok {
			return nil, false
		}

		var next [][]any

		for _, combo := range combos {
			for _, alt := range alts {
				next = append(next, append(append([]any(nil), combo...), alt))
			}
		}

		if len(next)*len(formats) > maxVariants {
			return nil, false
		}

		combos = next
	}

	var out []string

	for _, f := range formats {
		for _, combo := range combos {
			out = append(out, fmt.Sprintf(f, combo...))
		}
	}

	return out, true
}

func (c *declCtx) argValues(e ast.Expr, depth int) ([]any, bool) {
	t := c.info.TypeOf(e)
	if t == nil {
		return nil, false
	}

	if b, ok := t.Underlying().(*types.Basic); ok {
		switch {
		case b.Info()&types.IsString != 0:
			vs, ok := c.values(e, depth)
			if !ok {
				return nil, false
			}

			out := make([]any, len(vs))
			for i, v := range vs {
				out[i] = v
			}

			return out, true
		case b.Info()&types.IsInteger != 0:
			return []any{0}, true
		case b.Info()&types.IsFloat != 0:
			return []any{0.0}, true
		case b.Info()&types.IsBoolean != 0:
			return []any{false}, true
		}
	}

	return nil, false
}

func (c *declCtx) join(call *ast.CallExpr, depth int) ([]string, bool) {
	if len(call.Args) != 2 {
		return nil, false
	}

	elems, optional, ok := c.sliceElems(call.Args[0])
	if !ok {
		return nil, false
	}

	seps, ok := c.values(call.Args[1], depth)
	if !ok || len(seps) != 1 {
		return nil, false
	}

	alts := make([][]string, 0, len(elems))

	for i, e := range elems {
		vs, ok := c.values(e, depth)
		if !ok {
			return nil, false
		}

		alts = append(alts, vs)
		if optional[i] {
			alts[i] = append(alts[i], skipElem)
		}
	}

	// variants as lists of elements, joined at the end so a left-out
	// element leaves no separator behind
	lists := [][]string{nil}

	for _, vs := range alts {
		var next [][]string

		for _, l := range lists {
			for _, v := range vs {
				if v == skipElem {
					next = append(next, l)
				} else {
					next = append(next, append(append([]string(nil), l...), v))
				}
			}
		}

		if len(next) > maxVariants {
			return nil, false
		}

		lists = next
	}

	out := make([]string, 0, len(lists))
	for _, l := range lists {
		out = append(out, strings.Join(l, seps[0]))
	}

	return dedupe(out), true
}

const skipElem = "\x00skip"

// sliceElems lists what a local slice of strings is built from: composite
// literals and appends of elements, in source order. Appended elements are
// optional: the append may sit in a branch.
func (c *declCtx) sliceElems(e ast.Expr) ([]ast.Expr, []bool, bool) {
	switch e := ast.Unparen(e).(type) {
	case *ast.CompositeLit:
		return e.Elts, make([]bool, len(e.Elts)), true
	case *ast.Ident:
		v, ok := c.info.Uses[e].(*types.Var)
		if !ok || c.busy[v] {
			return nil, nil, false
		}

		c.busy[v] = true
		defer delete(c.busy, v)

		var (
			out      []ast.Expr
			optional []bool
		)

		for _, a := range c.assigns()[v] {
			if a.expr == nil || a.add {
				return nil, nil, false
			}

			elems, appended, ok := c.appendElems(v, a.expr)
			if !ok {
				return nil, nil, false
			}

			for _, el := range elems {
				out = append(out, el)
				optional = append(optional, appended)
			}
		}

		return out, optional, true
	}

	return nil, nil, false
}

func (c *declCtx) appendElems(v *types.Var, e ast.Expr) (elems []ast.Expr, appended, ok bool) {
	switch e := ast.Unparen(e).(type) {
	case *ast.CompositeLit:
		return e.Elts, false, true
	case *ast.Ident:
		return nil, false, e.Name == "nil"
	case *ast.CallExpr:
		id, isIdent := ast.Unparen(e.Fun).(*ast.Ident)
		if !isIdent || len(e.Args) == 0 {
			return nil, false, false
		}

		switch {
		case id.Name == "make":
			return nil, false, true
		case id.Name == "append" && !e.Ellipsis.IsValid():
			if first, isIdent := ast.Unparen(e.Args[0]).(*ast.Ident); !isIdent || c.info.Uses[first] != v {
				return nil, false, false
			}

			return e.Args[1:], true, true
		}
	}

	return nil, false, false
}

// isParam says a variable is a parameter of the declaration's function
// (not of a function literal inside it, whose callers the code doesn't
// show).
func (c *declCtx) isParam(v *types.Var) bool {
	return c.paramIndex(v) >= 0
}

func (c *declCtx) paramIndex(v *types.Var) int {
	fd, ok := c.d.Node.(*ast.FuncDecl)
	if !ok {
		return -1
	}

	i := 0

	for _, f := range fd.Type.Params.List {
		if len(f.Names) == 0 {
			i++

			continue
		}

		for _, id := range f.Names {
			if c.info.Defs[id] == v {
				return i
			}

			i++
		}
	}

	return -1
}

// paramValues are the values every caller passes for a parameter of a
// plain function. A function used other than by calling it (passed as a
// value), a method (an interface may call it), and a variadic parameter
// keep their values unknown.
func (a *analyzer) paramValues(c *declCtx, v *types.Var, depth int) ([]string, bool) {
	fd, ok := c.d.Node.(*ast.FuncDecl)
	if !ok || fd.Recv != nil || depth >= maxDepth {
		return nil, false
	}

	fn, ok := c.info.Defs[fd.Name].(*types.Func)
	if !ok {
		return nil, false
	}

	key := funcKey(fn)
	if a.escapes[key] {
		return nil, false
	}

	idx := c.paramIndex(v)

	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Variadic() && idx == sig.Params().Len()-1 {
		return nil, false
	}

	var out []string

	for _, site := range a.callers[key] {
		if idx >= len(site.call.Args) {
			return nil, false
		}

		vs, ok := site.ctx.values(site.call.Args[idx], depth+1)
		if !ok {
			return nil, false
		}

		out = append(out, vs...)
		if len(out) > maxVariants {
			return nil, false
		}
	}

	return out, len(out) > 0
}

type callSite struct {
	ctx  *declCtx
	call *ast.CallExpr
}

func (c *declCtx) findCalls() {
	called := map[*ast.Ident]bool{}

	ast.Inspect(c.d.Node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		if fn := staticCallee(c.info, call); fn != nil {
			key := funcKey(fn)
			c.a.callers[key] = append(c.a.callers[key], callSite{ctx: c, call: call})

			if id := identOf(ast.Unparen(call.Fun)); id != nil {
				called[id] = true
			}
		}

		return true
	})

	ast.Inspect(c.d.Node, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok || called[id] {
			return true
		}

		if fn, ok := c.info.Uses[id].(*types.Func); ok {
			c.a.escapes[funcKey(fn)] = true
		}

		return true
	})
}

func (c *declCtx) dynamicReason(e ast.Expr, place sqlrefs.Place) string {
	what := "SQL"

	switch place {
	case sqlrefs.Statement:
	case sqlrefs.From:
		what = "a table name"
	default:
		what = "an SQL fragment"
	}

	switch e := ast.Unparen(e).(type) {
	case *ast.BinaryExpr:
		return what + " concatenated at run time"
	case *ast.CallExpr:
		if fn := staticCallee(c.info, e); fn != nil && fn.Pkg() != nil {
			return what + " returned by " + fn.Pkg().Name() + "." + fn.Name()
		}

		return what + " returned by a call"
	case *ast.Ident:
		return what + c.varReason(e)
	case *ast.SelectorExpr:
		return what + " in a field"
	case *ast.IndexExpr:
		return what + " taken from a map or slice"
	}

	return what + " built at run time"
}

func (c *declCtx) varReason(id *ast.Ident) string {
	v, ok := c.info.Uses[id].(*types.Var)

	switch {
	case !ok:
		return " built at run time"
	case c.isParam(v):
		return " passed in as a parameter"
	case v.Pkg() != nil && v.Parent() == v.Pkg().Scope():
		return " in a package variable"
	}

	return " in a variable assigned at run time"
}
