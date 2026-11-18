package analysis

import (
	"go/ast"
	"go/types"
	"strings"

	"github.com/sideways-works/pipelinegen-plugins/correlator/gorm/internal/sqlrefs"
)

// Names GORM uses for a method and a type alike.
const (
	nameTable = "Table"
	nameExpr  = "Expr"
)

// associations is the value of clause.Associations: Preload, Omit and
// Select take it to mean every association.
const associations = "~~~as~~~"

// apply adds what a builder call does to its handle's chain. Calls that
// change nothing a table depends on (Limit, Session, WithContext) leave
// it as it is.
func (c *declCtx) apply(ch *chain, g gcall) {
	args := g.call.Args
	rest := args[min(1, len(args)):]

	switch g.name {
	case "Model":
		if len(args) > 0 {
			c.modelArg(ch, args[0])
		}
	case nameTable:
		if len(args) > 0 {
			c.tableArg(ch, args[0], len(args) > 1)
		}

		c.subqueries(ch, rest)
	case "Joins", "InnerJoins":
		c.joinsArg(ch, args)
	case "Preload":
		c.preloadArg(ch, args)
	case recvAssoc:
		if len(args) > 0 {
			c.assocName(ch, args[0])
		}
	case "Where", "Or", "Not":
		c.condition(ch, args, sqlrefs.Where)
	case "Having":
		c.condition(ch, args, sqlrefs.Having)
	case "Select", "Distinct":
		c.selectArg(ch, args)
	case "Order":
		c.condition(ch, args, sqlrefs.Order)
	case "Group":
		c.condition(ch, args, sqlrefs.Group)
	case "Omit":
		ch.omitAssocs = ch.omitAssocs || c.namesAssociations(args)
	case "Raw":
		ch.raw = true

		if len(args) > 0 {
			c.rawArg(ch, args[0])
		}

		c.subqueries(ch, rest)
	default:
		c.applyMore(ch, g.name, args)
	}
}

func (c *declCtx) applyMore(ch *chain, name string, args []ast.Expr) {
	switch name {
	case "Scopes":
		for _, a := range args {
			c.scope(ch, a)
		}
	case "Clauses":
		for _, a := range args {
			c.clauseArg(ch, a)
		}
	case "Attrs", "Assign":
		c.subqueries(ch, args)
	}
}

func (c *declCtx) namesAssociations(args []ast.Expr) bool {
	for _, a := range args {
		if s, ok := constString(c.info, a); ok && s == associations {
			return true
		}
	}

	return false
}

func (c *declCtx) modelArg(ch *chain, e ast.Expr) {
	n, why := named(c.info.TypeOf(e))

	switch {
	case n != nil:
		ch.models = append(ch.models, n)
		ch.bare = c.bareLiteral(e, n)
	case why == whyInterface:
		ch.unknown = append(ch.unknown, "the model is an interface value, chosen at run time")
	}
}

// tableArg reads Table(name): a plain name GORM quotes as written; a name
// with a space (an alias, a subquery) is SQL.
func (c *declCtx) tableArg(ch *chain, e ast.Expr, withArgs bool) {
	ch.tableAlways = true

	vs, ok := c.str(e)
	if !ok {
		ch.unknown = append(ch.unknown, c.dynamicReason(e, sqlrefs.From))

		return
	}

	for _, s := range vs {
		if !strings.ContainsAny(s, " `(") && !withArgs {
			ch.tables = append(ch.tables, c.a.cfg.relation(s))

			continue
		}

		sub := &chain{}
		if !c.a.parse(sub, e.Pos(), s, sqlrefs.From) {
			ch.unknown = append(ch.unknown, "SQL the parser can't read")

			continue
		}

		for _, r := range sub.refs {
			ch.tables = append(ch.tables, r.rel)
		}
	}
}

// joinsArg reads Joins: an association name, or SQL; a generic handle's
// join target names an association or a table.
func (c *declCtx) joinsArg(ch *chain, args []ast.Expr) {
	if len(args) == 0 {
		return
	}

	if !isString(c.info.TypeOf(args[0])) {
		c.joinTarget(ch, args[0])

		return
	}

	vs, ok := c.str(args[0])
	if ok && len(vs) == 1 && isPath(vs[0]) {
		ch.assocs = append(ch.assocs, vs[0])
	} else {
		c.sqlArg(ch, args[0], sqlrefs.Join)
	}

	c.subqueries(ch, args[1:])
}

func (c *declCtx) joinTarget(ch *chain, e ast.Expr) {
	switch e := ast.Unparen(e).(type) {
	case *ast.CallExpr:
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok && len(e.Args) > 0 {
			switch sel.Sel.Name {
			case "Has", recvAssoc, "AssociationFrom":
				if s, ok := constString(c.info, e.Args[0]); ok {
					ch.assocs = append(ch.assocs, s)

					return
				}
			case "As":
				c.joinTarget(ch, sel.X)

				return
			}
		}
	case *ast.CompositeLit:
		for _, el := range e.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				continue
			}

			key, _ := kv.Key.(*ast.Ident) //nolint:errcheck // a positional element has no key
			s, isConst := constString(c.info, kv.Value)

			switch {
			case key == nil:
			case key.Name == recvAssoc && isConst:
				ch.assocs = append(ch.assocs, s)
			case key.Name == nameTable && isConst:
				ch.refs = append(ch.refs, ref{rel: c.a.cfg.relation(s)})
			case key.Name == "Subquery":
				ch.unknown = append(ch.unknown, "a join on a subquery expression")
			}
		}

		return
	}

	ch.unknown = append(ch.unknown, "a join target built at run time")
}

func isPath(s string) bool {
	if s == "" {
		return false
	}

	for part := range strings.SplitSeq(s, ".") {
		if part == "" || !isIdent(part) {
			return false
		}
	}

	return true
}

func isIdent(s string) bool {
	for i, r := range s {
		ok := r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9'
		if !ok {
			return false
		}
	}

	return true
}

func (c *declCtx) preloadArg(ch *chain, args []ast.Expr) {
	if len(args) == 0 {
		return
	}

	vs, ok := c.str(args[0])
	if !ok {
		ch.unknown = append(ch.unknown, "an association named at run time")
	}

	for _, s := range vs {
		if s == associations {
			ch.allAssocs = true
		} else {
			ch.assocs = append(ch.assocs, s)
		}
	}

	c.subqueries(ch, args[1:])
}

func (c *declCtx) assocName(ch *chain, e ast.Expr) {
	vs, ok := c.str(e)
	if !ok {
		ch.unknown = append(ch.unknown, "an association named at run time")

		return
	}

	ch.assocs = append(ch.assocs, vs...)
}

func (c *declCtx) condition(ch *chain, args []ast.Expr, place sqlrefs.Place) {
	if len(args) == 0 {
		return
	}

	switch t := c.info.TypeOf(args[0]); {
	case isString(t):
		c.sqlArg(ch, args[0], place)
	case isHandle(t):
		ch.merge(c.chainOf(args[0]), c.a)
	case isInterface(t):
		ch.unknown = append(ch.unknown, "a condition passed as an interface value")
	}

	c.subqueries(ch, args[1:])
}

func (c *declCtx) selectArg(ch *chain, args []ast.Expr) {
	if len(args) == 0 {
		return
	}

	if s, ok := constString(c.info, args[0]); ok && s == associations {
		ch.selectAssocs = true

		return
	}

	switch t := c.info.TypeOf(args[0]); {
	case isString(t):
		c.sqlArg(ch, args[0], sqlrefs.Select)
	case isInterface(t):
		ch.unknown = append(ch.unknown, "a select list passed as an interface value")
	}

	c.subqueries(ch, args[1:])
}

func (c *declCtx) subqueries(ch *chain, args []ast.Expr) {
	for _, a := range args {
		if isHandle(c.info.TypeOf(a)) {
			ch.merge(c.chainOf(a), c.a)
		}
	}
}

func (c *declCtx) rawArg(ch *chain, e ast.Expr) {
	c.a.stats.Raw++

	if c.parseArg(ch, e, sqlrefs.Statement) {
		c.a.stats.RawResolved++
	}
}

func (c *declCtx) scope(ch *chain, e ast.Expr) {
	switch e := ast.Unparen(e).(type) {
	case *ast.FuncLit:
		c.scopeBody(ch, c, e.Body)
	case *ast.Ident, *ast.SelectorExpr:
		if fn, ok := c.info.Uses[identOf(e)].(*types.Func); ok {
			c.scopeChain(ch, c.a.funcSummary(fn))
		}
	case *ast.CallExpr:
		fn := staticCallee(c.info, e)
		if fn == nil {
			return
		}

		src := c.a.funcs[funcKey(fn)]
		if src == nil || src.decl.Body == nil {
			return
		}

		cc := c.a.ctxFor(src)

		for _, r := range returns(src.decl.Body) {
			for _, res := range r.Results {
				if lit, ok := ast.Unparen(res).(*ast.FuncLit); ok {
					cc.scopeBody(ch, cc, lit.Body)
				}
			}
		}
	}
}

func (c *declCtx) scopeBody(ch *chain, in *declCtx, body *ast.BlockStmt) {
	var chs []*chain

	for _, r := range returns(body) {
		if len(r.Results) > 0 {
			chs = append(chs, in.chainOf(r.Results[0]))
		}
	}

	if len(chs) > 0 {
		c.scopeChain(ch, union(chs))
	}
}

// scopeChain adds a scope's tables, models and SQL to the handle, without
// what it can't tell (that is the scope's own declaration's to report).
func (c *declCtx) scopeChain(ch *chain, s *chain) {
	ch.tables = append(ch.tables, s.tables...)
	ch.tableAlways = ch.tableAlways || s.tableAlways
	ch.models = append(ch.models, s.models...)
	ch.refs = append(ch.refs, s.refs...)
	ch.assocs = append(ch.assocs, s.assocs...)
	ch.raw = ch.raw || s.raw
}

// sqlClauses are the clause types that carry SQL text or a table name.
// The others (Locking, OnConflict, Returning, OrderBy...) name columns of
// the statement's own tables.
var sqlClauses = map[string]bool{nameExpr: true, "NamedExpr": true, "From": true, nameTable: true, "Join": true}

// clauseArg reads Clauses(...): a literal is read where it is written (see
// clauseLit); a clause carrying SQL that was built elsewhere can't be told.
func (c *declCtx) clauseArg(ch *chain, e ast.Expr) {
	if _, lit := ast.Unparen(e).(*ast.CompositeLit); lit {
		return
	}

	if u, ok := ast.Unparen(e).(*ast.UnaryExpr); ok {
		if _, lit := ast.Unparen(u.X).(*ast.CompositeLit); lit {
			return
		}
	}

	if n := clauseNamed(c.info.TypeOf(e)); n != "" && sqlClauses[n] || isInterface(c.info.TypeOf(e)) {
		ch.unknown = append(ch.unknown, "a clause with SQL built elsewhere")
	}
}

func clauseNamed(t types.Type) string {
	if t == nil {
		return ""
	}

	if p, ok := types.Unalias(t).(*types.Pointer); ok {
		t = p.Elem()
	}

	n, ok := types.Unalias(t).(*types.Named)
	if !ok || n.Obj().Pkg() == nil || n.Obj().Pkg().Path() != gormPath+"/clause" {
		return ""
	}

	return n.Obj().Name()
}

func (c *declCtx) clauseLit(lit *ast.CompositeLit) {
	name := clauseNamed(c.info.TypeOf(lit))
	if name != nameExpr && name != "NamedExpr" && name != nameTable {
		return
	}

	c.gorm = true
	c.at = lit.Pos()

	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			c.cantTell("a clause written without field names")

			return
		}

		key, _ := kv.Key.(*ast.Ident) //nolint:errcheck // a struct literal's keys are field names
		ch := &chain{}

		switch {
		case key == nil:
		case key.Name == "SQL":
			c.sqlArg(ch, kv.Value, sqlrefs.Select)
		case key.Name == "Name" && name == nameTable:
			c.tableArg(ch, kv.Value, false)
		}

		c.mention(ch, lit.Pos())
	}
}

func (c *declCtx) bareLiteral(e ast.Expr, n *types.Named) bool {
	e = ast.Unparen(e)
	if u, ok := e.(*ast.UnaryExpr); ok {
		e = ast.Unparen(u.X)
	}

	lit, ok := e.(*ast.CompositeLit)
	if !ok {
		return false
	}

	m := c.a.model(n)

	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			return false
		}

		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			return false
		}

		if _, isAssoc := m.assocs[key.Name]; isAssoc {
			return false
		}
	}

	return true
}

func identOf(e ast.Expr) *ast.Ident {
	switch e := e.(type) {
	case *ast.Ident:
		return e
	case *ast.SelectorExpr:
		return e.Sel
	}

	return nil
}

func isInterface(t types.Type) bool {
	if t == nil {
		return false
	}

	_, ok := t.Underlying().(*types.Interface)

	return ok
}
