package analysis

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"github.com/sideways-works/pipelinegen-correlator-gorm/internal/sqlrefs"
	"github.com/sideways-works/pipelinegen-sbom-go/goid"
)

type declCtx struct {
	a    *analyzer
	d    goid.Decl
	id   string
	info *types.Info
	pkg  *types.Package

	chains      map[ast.Expr]*chain
	busy        map[types.Object]bool
	assign      map[types.Object][]assignment
	freshParams map[types.Object]bool
	gorm        bool
	at          token.Pos
}

func (c *declCtx) cantTell(reason string) {
	where := ""
	if c.at.IsValid() {
		where = c.a.pos(c.at)
	}

	c.a.cantTell(c.id, reason, where)
}

func (a *analyzer) ctxFor(src *funcSrc) *declCtx {
	return &declCtx{
		a: a, d: goid.Decl{ID: goid.ID(src.id), Node: src.decl}, id: src.id, info: src.info,
		chains: map[ast.Expr]*chain{}, busy: map[types.Object]bool{},
	}
}

// What a statement does, which decides its link kinds, hooks and
// callbacks.
const (
	opCreate        = "create"
	opSave          = "save"
	opFirstOrCreate = "first-or-create"
	opUpdate        = "update"
	opUpdateColumns = "update-columns"
	opDelete        = "delete"
	opQuery         = "query"
	opRow           = "row"
	opRaw           = "raw"
	opMigrate       = "migrate"
)

// The GORM methods several tables below name.
const (
	mCreate = "Create"
	mFind   = "Find"
	mUpdate = "Update"
	mDelete = "Delete"
	mCount  = "Count"
	mRow    = "Row"
)

type op struct {
	// does is create, save, first-or-create, update, update-columns,
	// delete, query, row or raw.
	does string
	// dest is the argument GORM takes the model from when none is set, or -1.
	dest int
	// conds is the first argument that may hold conditions, or -1.
	conds int
	// sql is the argument holding a whole statement, or -1.
	sql int
}

// The calls on *gorm.DB that run a statement.
var dbOps = map[string]op{
	mCreate: {opCreate, 0, -1, -1}, "CreateInBatches": {opCreate, 0, -1, -1},
	"Save":  {opSave, 0, -1, -1},
	"First": {opQuery, 0, 1, -1}, "Take": {opQuery, 0, 1, -1}, "Last": {opQuery, 0, 1, -1},
	mFind: {opQuery, 0, 1, -1}, "FindInBatches": {opQuery, 0, -1, -1}, "FirstOrInit": {opQuery, 0, 1, -1},
	"FirstOrCreate": {opFirstOrCreate, 0, 1, -1},
	mUpdate:         {opUpdate, -1, -1, -1}, "Updates": {opUpdate, 0, -1, -1},
	"UpdateColumn": {opUpdateColumns, -1, -1, -1}, "UpdateColumns": {opUpdateColumns, 0, -1, -1},
	mDelete: {opDelete, 0, 1, -1},
	mCount:  {opQuery, -1, -1, -1}, "Pluck": {opQuery, 1, -1, -1},
	mRow: {opRow, -1, -1, -1}, "Rows": {opRow, -1, -1, -1}, "Scan": {opRow, -1, -1, -1},
	"Exec":        {opRaw, -1, -1, 0},
	"AutoMigrate": {opMigrate, -1, -1, -1},
}

// The calls of the generic API that run a statement; the model is the
// handle's type argument.
var genericOps = map[string]op{
	mCreate: {opCreate, -1, -1, -1}, "CreateInBatches": {opCreate, -1, -1, -1},
	mDelete: {opDelete, -1, -1, -1}, mUpdate: {opUpdate, -1, -1, -1}, "Updates": {opUpdate, -1, -1, -1},
	mCount: {opQuery, -1, -1, -1}, "First": {opQuery, -1, -1, -1}, "Last": {opQuery, -1, -1, -1},
	"Take": {opQuery, -1, -1, -1}, mFind: {opQuery, -1, -1, -1}, "FindInBatches": {opQuery, -1, -1, -1},
	"Scan": {opRow, -1, -1, -1}, mRow: {opRow, -1, -1, -1}, "Rows": {opRow, -1, -1, -1},
	"Exec": {opRaw, -1, -1, 1},
}

// The calls on *gorm.Association that run a statement.
var assocOps = map[string]op{
	mFind: {opQuery, -1, 1, -1}, mCount: {opQuery, -1, -1, -1},
	"Append": {opUpdate, -1, -1, -1}, "Replace": {opUpdate, -1, -1, -1},
	mDelete: {opUpdate, -1, -1, -1}, "Clear": {opUpdate, -1, -1, -1},
}

func (o op) writes() bool {
	switch o.does {
	case opCreate, opSave, opFirstOrCreate, opUpdate, opUpdateColumns, opDelete, opMigrate:
		return true
	}

	return false
}

// kinds are the statement kinds GORM runs for an operation: the hooks and
// callbacks registered for those run with it.
func (o op) kinds() []string {
	switch o.does {
	case opSave:
		return []string{opCreate, opUpdate}
	case opFirstOrCreate:
		return []string{opQuery, opCreate}
	case opUpdateColumns:
		return []string{opUpdate}
	case opMigrate:
		return nil
	}

	return []string{o.does}
}

func (o op) savesAssocs() bool {
	switch o.does {
	case opCreate, opSave, opFirstOrCreate, opUpdate, opUpdateColumns:
		return true
	}

	return false
}

// run reads the declaration: every GORM call that isn't itself the start
// of a longer chain is a statement, or a handle passed on.
func (c *declCtx) run() {
	inner := map[ast.Expr]bool{}

	ast.Inspect(c.d.Node, func(n ast.Node) bool {
		if e, ok := n.(ast.Expr); ok {
			if g, ok := c.gormCall(e); ok {
				inner[ast.Unparen(g.x)] = true
			}
		}

		return true
	})

	ast.Inspect(c.d.Node, func(n ast.Node) bool {
		if lit, ok := n.(*ast.CompositeLit); ok {
			c.clauseLit(lit)
		}

		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		if name, _ := c.gormFunc(call); name == nameExpr && len(call.Args) > 0 {
			c.gorm = true
			c.at = call.Pos()
			ch := &chain{}
			c.sqlArg(ch, call.Args[0], sqlrefs.Select)
			c.mention(ch, call.Pos())
		}

		g, ok := c.gormCall(call)
		if !ok || inner[call] {
			return true
		}

		c.gorm = true
		c.statement(g)

		return true
	})
}

func (c *declCtx) statement(g gcall) {
	c.at = g.call.Pos()
	ch := c.chainOf(g.call)

	var (
		o  op
		ok bool
	)

	switch g.recv {
	case recvDB:
		o, ok = dbOps[g.name]
	case recvGeneric:
		o, ok = genericOps[g.name]
	case recvAssoc:
		o, ok = assocOps[g.name]
	case recvMigrator:
		c.migrator(g)

		return
	}

	if !ok {
		c.mention(ch, g.call.Pos())

		return
	}

	c.a.stats.Calls++
	before := len(c.a.cannot[c.id])

	if g.recv == recvAssoc {
		c.association(g, ch, o)
	} else {
		c.finish(g, ch, o)
	}

	if len(c.a.cannot[c.id]) == before {
		c.a.stats.CallsResolved++
	}
}

// mention links what a handle the declaration passes on carries: whoever
// ends the statement reads at least these relations.
func (c *declCtx) mention(ch *chain, pos token.Pos) {
	where := c.a.pos(pos)

	for _, t := range ch.tables {
		c.link(t, KindReads, where)
	}

	if !ch.tableAlways {
		for _, n := range ch.models {
			c.modelTables(n, false, where)
		}
	}

	for _, r := range ch.refs {
		c.ref(r, where)
	}

	c.assocPaths(ch, ch.models, where)
	c.unknown(ch)
}

func (c *declCtx) unknown(ch *chain) {
	for _, u := range ch.unknown {
		c.cantTell(u)
	}
}

func (c *declCtx) link(t Relation, kind, where string) {
	c.wide(t, kind, "", where)
}

// wide links a relation only a wider rule than the narrowing's reaches; it
// stands while that narrowing is off.
func (c *declCtx) wide(t Relation, kind, narrowing, where string) {
	l := Link{From: c.id, To: t.ID(), Kind: KindReads, Narrowing: narrowing}
	if narrowing != "" {
		l.While = WhileOff
	}

	c.a.add(l, where)

	if kind == KindWrites {
		l.Kind = KindWrites
		c.a.add(l, where)
	}
}

func (c *declCtx) ref(r ref, where string) {
	if r.routine {
		c.a.add(Link{From: c.id, To: r.rel.RoutineID(), Kind: KindUses}, where)

		return
	}

	kind := KindReads
	if r.write {
		kind = KindWrites
	}

	c.link(r.rel, kind, where)
}

func (c *declCtx) modelTables(n *types.Named, write bool, where string) *Model {
	m := c.a.model(n)
	if m.unknown != "" {
		c.cantTell(m.unknown)

		return m
	}

	kind := KindReads
	if write {
		kind = KindWrites
	}

	for _, t := range m.tables {
		c.link(t, kind, where)
	}

	return m
}

func (c *declCtx) finish(g gcall, ch *chain, o op) {
	where := c.a.pos(g.call.Pos())
	args := g.call.Args

	if o.sql >= 0 && o.sql < len(args) {
		ch = ch.clone()
		ch.raw = true
		c.rawArg(ch, args[o.sql])
	}

	if o.does == opMigrate {
		for _, arg := range args {
			if n, _ := named(c.info.TypeOf(arg)); n != nil {
				c.modelTables(n, true, where)
			}
		}

		return
	}

	for _, t := range ch.tables {
		c.link(t, kindOf(o), where)
	}

	var models []*types.Named
	if ch.tableAlways {
		models = c.overridden(ch, o, args, where)
	} else {
		models = c.models(ch, o, args, where)
	}

	for _, r := range ch.refs {
		c.ref(r, where)
	}

	c.assocPaths(ch, models, where)
	c.unknown(ch)

	if o.conds >= 0 && o.conds < len(args) {
		c.condArg(args[o.conds], where)
	}

	for _, k := range o.kinds() {
		c.trigger(k)
	}
}

// models links the statement's model: its tables, the hooks GORM calls and
// the associations it saves.
func (c *declCtx) models(ch *chain, o op, args []ast.Expr, where string) []*types.Named {
	models, bare := ch.models, ch.bare
	if len(models) == 0 {
		models = c.destModel(ch, o, args)
		bare = len(models) == 1 && c.bareLiteral(args[o.dest], models[0])
	}

	for _, n := range models {
		m := c.modelTables(n, o.writes(), where)
		c.hooks(m, o, where)

		switch {
		case o.does == opDelete && ch.selectAssocs:
			c.savedAssocs(m, "", where)
		case !o.savesAssocs() || ch.omitAssocs:
		case bare:
			// a literal that sets no association saves none; the wide
			// rule saves them as for any value
			c.savedAssocs(m, NarrowAssociations, where)
		default:
			c.savedAssocs(m, "", where)
		}
	}

	return models
}

// overridden handles a statement whose Table() replaces the model's table:
// GORM then reads and writes that table only, and the wide rule links the
// model's too.
func (c *declCtx) overridden(ch *chain, o op, args []ast.Expr, where string) []*types.Named {
	models := ch.models
	if len(models) == 0 && o.dest >= 0 && o.dest < len(args) {
		if n, _ := named(c.info.TypeOf(args[o.dest])); n != nil {
			models = []*types.Named{n}
		}
	}

	for _, n := range models {
		m := c.a.model(n)
		for _, t := range m.tables {
			c.wide(t, kindOf(o), NarrowModels, where)
		}

		for _, id := range sortedValues(m.hooks) {
			c.a.add(Link{From: c.id, To: id, Kind: KindCalls, Narrowing: NarrowModels, While: WhileOff}, where)
		}
	}

	return models
}

func sortedValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, k := range sortedKeys(m) {
		out = append(out, m[k])
	}

	return out
}

func kindOf(o op) string {
	if o.writes() {
		return KindWrites
	}

	return KindReads
}

func (c *declCtx) destModel(ch *chain, o op, args []ast.Expr) []*types.Named {
	if o.dest >= 0 && o.dest < len(args) {
		n, why := named(c.info.TypeOf(args[o.dest]))
		if n != nil {
			return []*types.Named{n}
		}

		if why == whyInterface {
			c.cantTell("the model is an interface value, chosen at run time")

			return nil
		}
	}

	if len(ch.tables) == 0 && !ch.raw && ch.open {
		c.cantTell("the statement's table is set outside this declaration")
	}

	return nil
}

func (c *declCtx) condArg(e ast.Expr, where string) {
	if !isString(c.info.TypeOf(e)) {
		return
	}

	ch := &chain{}
	c.sqlArg(ch, e, sqlrefs.Where)

	for _, r := range ch.refs {
		c.ref(r, where)
	}

	c.unknown(ch)
}

// hooks links the hook methods GORM calls on the model for this
// operation; the model's other hooks stand while gorm-models is off.
func (c *declCtx) hooks(m *Model, o op, where string) {
	called := map[string]bool{}

	for _, k := range o.kinds() {
		for _, h := range hooksFor[k] {
			called[h] = true
		}
	}

	for _, h := range sortedKeys(m.hooks) {
		l := Link{From: c.id, To: m.hooks[h], Kind: KindCalls}
		if !called[h] {
			l.Narrowing, l.While = NarrowModels, WhileOff
		}

		c.a.add(l, where)
	}
}

func (c *declCtx) savedAssocs(m *Model, narrowing, where string) {
	ts, unknown := c.a.saved(m)

	for _, t := range ts {
		c.wide(t, KindWrites, narrowing, where)
	}

	for _, u := range unknown {
		c.cantTell(u)
	}
}

func (c *declCtx) association(g gcall, ch *chain, o op) {
	where := c.a.pos(g.call.Pos())

	// the chain of an Association call carries the field name as its
	// only association path
	for _, n := range ch.models {
		c.modelTables(n, o.writes(), where)
	}

	for _, t := range ch.tables {
		c.link(t, kindOf(o), where)
	}

	saved := *ch
	saved.assocs = nil

	for _, p := range ch.assocs {
		c.assocPath(p, ch.models, kindOf(o), where)
	}

	c.unknown(&saved)

	if o.conds >= 0 && o.conds < len(g.call.Args) {
		c.condArg(g.call.Args[o.conds], where)
	}

	c.trigger(o.does)
}

func (c *declCtx) assocPaths(ch *chain, models []*types.Named, where string) {
	for _, p := range ch.assocs {
		c.assocPath(p, models, KindReads, where)
	}

	if ch.allAssocs {
		for _, n := range models {
			m := c.a.model(n)
			for _, name := range sortedKeys(m.assocs) {
				c.assocPath(name, []*types.Named{n}, KindReads, where)
			}
		}
	}
}

// assocPath follows "Orders.Items" from the models through their fields.
// Without a model it takes every model with a field of that name: the
// handle's model is set elsewhere, and any of them may be it.
func (c *declCtx) assocPath(path string, models []*types.Named, kind, where string) {
	cur := models
	if len(cur) == 0 {
		cur = c.a.modelsWith(strings.Split(path, ".")[0])
		if len(cur) == 0 {
			c.cantTell("an association no model has")

			return
		}
	}

	for step := range strings.SplitSeq(path, ".") {
		var next []*types.Named

		for _, n := range cur {
			as, ok := c.a.model(n).assocs[step]
			if !ok {
				continue
			}

			ts, why := c.a.assocTables(as)
			if why != "" {
				c.cantTell(why)

				continue
			}

			for _, t := range ts {
				c.link(t, kind, where)
			}

			next = append(next, as.related)
		}

		if len(next) == 0 {
			c.cantTell("an association its model doesn't have")

			return
		}

		cur = next
	}
}

func (a *analyzer) modelsWith(field string) []*types.Named {
	var out []*types.Named

	for _, k := range sortedKeys(a.models) {
		if as, ok := a.models[k].assocs[field]; ok && as != nil {
			out = append(out, a.namedOf[k])
		}
	}

	return out
}

// migrator handles db.Migrator().CreateTable(&T{}) and the like: models
// and constant table names given to it are written.
func (c *declCtx) migrator(g gcall) {
	where := c.a.pos(g.call.Pos())

	for _, arg := range g.call.Args {
		if n, _ := named(c.info.TypeOf(arg)); n != nil {
			c.modelTables(n, true, where)

			continue
		}

		if s, ok := constString(c.info, arg); ok {
			c.link(c.a.cfg.relation(s), KindWrites, where)
		}
	}
}

func (c *declCtx) trigger(kind string) {
	if c.a.triggers[c.id] == nil {
		c.a.triggers[c.id] = map[string]bool{}
	}

	c.a.triggers[c.id][kind] = true
}

func isString(t types.Type) bool {
	b, ok := t.Underlying().(*types.Basic)

	return ok && b.Info()&types.IsString != 0
}
