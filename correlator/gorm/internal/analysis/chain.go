package analysis

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/sideways-works/pipelinegen-correlator-gorm/internal/sqlrefs"
)

type ref struct {
	rel     Relation
	write   bool
	routine bool
}

// chain is what a *gorm.DB handle (or a generic one) carries into the
// statement that ends it: the model, an explicit table, SQL, associations
// to load, and what the code doesn't show.
type chain struct {
	models []*types.Named
	tables []Relation
	// tableAlways says every path to the handle set a Table(), so the
	// model or destination doesn't name the table.
	tableAlways bool
	refs        []ref
	raw         bool
	assocs      []string
	allAssocs   bool
	// omitAssocs is Omit(clause.Associations): saving skips associations.
	omitAssocs bool
	// selectAssocs is Select(clause.Associations): deleting takes the
	// associations along.
	selectAssocs bool
	// bare says the model value is a literal that sets no association,
	// so saving it saves no associated rows.
	bare    bool
	unknown []string
	// open says the handle comes from outside the declaration (a
	// parameter, a field, a call it can't see into), so it may carry a
	// table set elsewhere.
	open bool
}

func (ch *chain) clone() *chain {
	out := *ch
	out.models = append([]*types.Named(nil), ch.models...)
	out.tables = append([]Relation(nil), ch.tables...)
	out.refs = append([]ref(nil), ch.refs...)
	out.assocs = append([]string(nil), ch.assocs...)
	out.unknown = append([]string(nil), ch.unknown...)

	return &out
}

func union(chs []*chain) *chain {
	if len(chs) == 0 {
		return &chain{open: true}
	}

	out := chs[0].clone()

	for _, ch := range chs[1:] {
		out.models = append(out.models, ch.models...)
		out.tables = append(out.tables, ch.tables...)
		out.tableAlways = out.tableAlways && ch.tableAlways
		out.refs = append(out.refs, ch.refs...)
		out.raw = out.raw || ch.raw
		out.assocs = append(out.assocs, ch.assocs...)
		out.allAssocs = out.allAssocs || ch.allAssocs
		out.omitAssocs = out.omitAssocs && ch.omitAssocs
		out.bare = out.bare && ch.bare
		out.selectAssocs = out.selectAssocs || ch.selectAssocs
		out.unknown = append(out.unknown, ch.unknown...)
		out.open = out.open || ch.open
	}

	return out
}

func (ch *chain) merge(sub *chain, a *analyzer) {
	for _, t := range sub.tables {
		ch.refs = append(ch.refs, ref{rel: t})
	}

	if !sub.tableAlways {
		for _, n := range sub.models {
			m := a.model(n)
			if m.unknown != "" {
				ch.unknown = append(ch.unknown, m.unknown)

				continue
			}

			for _, t := range m.tables {
				ch.refs = append(ch.refs, ref{rel: t})
			}
		}
	}

	for _, r := range sub.refs {
		r.write = false
		ch.refs = append(ch.refs, r)
	}

	ch.unknown = append(ch.unknown, sub.unknown...)
}

const (
	recvDB       = "DB"
	recvAssoc    = "Association"
	recvMigrator = "Migrator"
	recvGeneric  = "generic"
)

var genericRecv = map[string]bool{
	"Interface": true, "CreateInterface": true, "ChainInterface": true, "ExecInterface": true,
	"SetUpdateOnlyInterface": true, "SetCreateOrUpdateInterface": true,
}

type gcall struct {
	recv  string
	name  string
	x     ast.Expr
	call  *ast.CallExpr
	model *types.Named
}

func (c *declCtx) gormCall(e ast.Expr) (gcall, bool) {
	call, ok := ast.Unparen(e).(*ast.CallExpr)
	if !ok {
		return gcall{}, false
	}

	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return gcall{}, false
	}

	s, ok := c.info.Selections[sel]
	if !ok || s.Kind() != types.MethodVal {
		return gcall{}, false
	}

	n := gormNamed(s.Recv())
	if n == nil {
		return gcall{}, false
	}

	g := gcall{name: sel.Sel.Name, x: sel.X, call: call}

	switch name := n.Origin().Obj().Name(); {
	case name == recvDB || name == recvAssoc || name == recvMigrator:
		g.recv = name
	case genericRecv[name]:
		g.recv = recvGeneric
		if args := n.TypeArgs(); args != nil && args.Len() == 1 {
			g.model, _ = named(args.At(0))
		}
	default:
		return gcall{}, false
	}

	return g, true
}

func gormNamed(t types.Type) *types.Named {
	if p, ok := types.Unalias(t).(*types.Pointer); ok {
		t = p.Elem()
	}

	n, ok := types.Unalias(t).(*types.Named)
	if !ok || n.Obj().Pkg() == nil || n.Obj().Pkg().Path() != gormPath {
		return nil
	}

	return n
}

func isHandle(t types.Type) bool {
	n := gormNamed(t)
	if n == nil {
		return false
	}

	name := n.Origin().Obj().Name()

	return name == recvDB || genericRecv[name]
}

func (c *declCtx) gormFunc(call *ast.CallExpr) (string, *types.Named) {
	fun := ast.Unparen(call.Fun)

	var typeArg *types.Named

	switch f := fun.(type) {
	case *ast.IndexExpr:
		fun = f.X
	case *ast.IndexListExpr:
		fun = f.X
	}

	var id *ast.Ident

	switch f := fun.(type) {
	case *ast.SelectorExpr:
		id = f.Sel
	case *ast.Ident:
		id = f
	default:
		return "", nil
	}

	fn, ok := c.info.Uses[id].(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != gormPath {
		return "", nil
	}

	if inst, ok := c.info.Instances[id]; ok && inst.TypeArgs != nil && inst.TypeArgs.Len() > 0 {
		typeArg, _ = named(inst.TypeArgs.At(0))
	}

	return fn.Name(), typeArg
}

func (c *declCtx) chainOf(e ast.Expr) *chain {
	e = ast.Unparen(e)
	if ch, ok := c.chains[e]; ok {
		return ch
	}

	ch := c.evalChain(e)
	c.chains[e] = ch

	return ch
}

func (c *declCtx) evalChain(e ast.Expr) *chain {
	switch e := e.(type) {
	case *ast.CallExpr:
		if g, ok := c.gormCall(e); ok {
			ch := c.chainOf(g.x).clone()
			if g.model != nil && len(ch.models) == 0 {
				ch.models = append(ch.models, g.model)
			}

			c.apply(ch, g)

			return ch
		}

		if name, t := c.gormFunc(e); name == "G" && len(e.Args) > 0 {
			ch := c.chainOf(e.Args[0]).clone()
			if t != nil {
				ch.models = append(ch.models, t)
			}

			return ch
		}

		return c.calleeChain(e)
	case *multiResult:
		if call, ok := ast.Unparen(e.call).(*ast.CallExpr); ok {
			return c.calleeChain(call)
		}
	case *ast.Ident:
		return c.identChain(e)
	case *ast.SelectorExpr:
		return c.selectorChain(e)
	case *ast.StarExpr:
		return c.chainOf(e.X)
	case *ast.UnaryExpr:
		return c.chainOf(e.X)
	}

	return &chain{open: true}
}

// calleeChain is what a helper function's returned handle carries: the
// union of its return statements, worked out once per function.
func (c *declCtx) calleeChain(call *ast.CallExpr) *chain {
	fn := staticCallee(c.info, call)
	if fn == nil {
		return &chain{open: true}
	}

	return c.a.funcSummary(fn)
}

func (a *analyzer) funcSummary(fn *types.Func) *chain {
	key := funcKey(fn)
	if ch, ok := a.summary[key]; ok {
		return ch
	}

	src := a.funcs[key]
	if src == nil || src.decl.Body == nil || a.inFlight[key] {
		return &chain{open: true}
	}

	a.inFlight[key] = true
	defer delete(a.inFlight, key)

	idx := handleResult(src.info, src.decl)
	if idx < 0 {
		return &chain{open: true}
	}

	cc := a.ctxFor(src)

	var chs []*chain

	for _, r := range returns(src.decl.Body) {
		if idx < len(r.Results) {
			chs = append(chs, cc.chainOf(r.Results[idx]))
		}
	}

	out := union(chs)
	// what the helper can't tell is reported for the helper itself
	out.unknown = nil
	a.summary[key] = out

	return out
}

func handleResult(info *types.Info, fd *ast.FuncDecl) int {
	fn, ok := info.Defs[fd.Name].(*types.Func)
	if !ok {
		return -1
	}

	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		return -1
	}

	for i := range sig.Results().Len() {
		if isHandle(sig.Results().At(i).Type()) {
			return i
		}
	}

	return -1
}

// returns lists a body's return statements, not those of function
// literals inside it.
func returns(body ast.Node) []*ast.ReturnStmt {
	var out []*ast.ReturnStmt

	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			out = append(out, n)
		}

		return true
	})

	return out
}

func staticCallee(info *types.Info, call *ast.CallExpr) *types.Func {
	var id *ast.Ident

	switch f := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		id = f
	case *ast.SelectorExpr:
		if s, ok := info.Selections[f]; ok {
			if s.Kind() != types.MethodVal {
				return nil
			}

			if _, iface := s.Recv().Underlying().(*types.Interface); iface {
				return nil
			}
		}

		id = f.Sel
	case *ast.IndexExpr:
		return staticCallee(info, &ast.CallExpr{Fun: f.X})
	default:
		return nil
	}

	fn, _ := info.Uses[id].(*types.Func) //nolint:errcheck // not a function is no callee

	return fn
}

func (c *declCtx) identChain(id *ast.Ident) *chain {
	obj := c.info.Uses[id]
	if obj == nil {
		obj = c.info.Defs[id]
	}

	v, ok := obj.(*types.Var)
	if !ok {
		return &chain{open: true}
	}

	if v.Parent() == v.Pkg().Scope() {
		return c.a.storedChain(v.Pkg().Path() + "." + v.Name())
	}

	if c.fresh()[v] {
		return &chain{}
	}

	as, assigned := c.assigns()[v]
	if !assigned || c.busy[v] {
		return &chain{open: true}
	}

	c.busy[v] = true
	defer delete(c.busy, v)

	chs := make([]*chain, 0, len(as))

	for _, a := range as {
		switch {
		case a.multi != nil:
			chs = append(chs, c.chainOf(a.multi))
		case a.expr != nil && !a.add:
			chs = append(chs, c.chainOf(a.expr))
		default:
			chs = append(chs, &chain{open: true})
		}
	}

	return union(chs)
}

func (c *declCtx) selectorChain(sel *ast.SelectorExpr) *chain {
	s, ok := c.info.Selections[sel]
	if ok && s.Kind() == types.FieldVal {
		return c.a.storedChain(fieldKey(s.Recv(), sel.Sel.Name))
	}

	if v, ok := c.info.Uses[sel.Sel].(*types.Var); ok && v.Pkg() != nil && v.Parent() == v.Pkg().Scope() {
		return c.a.storedChain(v.Pkg().Path() + "." + v.Name())
	}

	return &chain{open: true}
}

func fieldKey(recv types.Type, field string) string {
	for {
		p, ok := types.Unalias(recv).(*types.Pointer)
		if !ok {
			break
		}

		recv = p.Elem()
	}

	if n, ok := types.Unalias(recv).(*types.Named); ok {
		return typeKey(n) + "." + field
	}

	return "?." + field
}

// storedChain is the union of what is stored into a field or package
// variable. A handle stored from outside (a constructor's parameter)
// stays open.
func (a *analyzer) storedChain(key string) *chain {
	if ch, ok := a.summary["store:"+key]; ok {
		return ch
	}

	if a.inFlight["store:"+key] {
		return &chain{open: true}
	}

	a.inFlight["store:"+key] = true
	defer delete(a.inFlight, "store:"+key)

	chs := []*chain{{open: true}}
	for _, s := range a.stores[key] {
		chs = append(chs, s.ctx.chainOf(s.expr))
	}

	out := union(chs)
	out.unknown = nil
	a.summary["store:"+key] = out

	return out
}

func (c *declCtx) sqlArg(ch *chain, e ast.Expr, place sqlrefs.Place) {
	c.a.stats.Fragments++

	if c.parseArg(ch, e, place) {
		c.a.stats.FragmentsResolved++
	}
}

func (c *declCtx) parseArg(ch *chain, e ast.Expr, place sqlrefs.Place) bool {
	vs, ok := c.str(e)
	if !ok {
		why := c.dynamicReason(e, place)
		c.a.trace(e.Pos(), why, types.ExprString(e))
		ch.unknown = append(ch.unknown, why)

		return false
	}

	// a combination that isn't SQL can't run: PostgreSQL's own parser
	// would refuse it; one that parses is enough to know the relations
	// of the others
	parsed := false

	for _, s := range vs {
		sub := &chain{}
		if c.a.parse(sub, e.Pos(), s, place) {
			parsed = true

			ch.refs = append(ch.refs, sub.refs...)
		}
	}

	if !parsed {
		ch.unknown = append(ch.unknown, "SQL the parser can't read")
	}

	return parsed
}

func (a *analyzer) parse(ch *chain, pos token.Pos, s string, place sqlrefs.Place) bool {
	r := a.parser.Parse(place, s)
	if r.Err != "" {
		a.trace(pos, r.Err, s)
		a.stats.ParseFail++

		return false
	}

	a.stats.Routines += len(r.Routines)

	for _, x := range r.Refs {
		ch.refs = append(ch.refs, ref{rel: a.cfg.sqlRelation(x), write: x.Write})
	}

	for _, x := range r.Routines {
		ch.refs = append(ch.refs, ref{rel: a.cfg.sqlRelation(x), routine: true})
	}

	return true
}

func (cfg Config) sqlRelation(x sqlrefs.Ref) Relation {
	if x.Schema == "" {
		return Relation{Schema: cfg.schema(), Name: x.Name}
	}

	return Relation{Schema: x.Schema, Name: x.Name}
}
