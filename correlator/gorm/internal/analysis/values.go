package analysis

import (
	"go/ast"
	"go/types"
	"sort"

	"github.com/sideways-works/pipelinegen-plugins/sbom/go/goid"
)

// fresh are the parameters of function literals GORM calls with a new
// session (Transaction, Connection, FindInBatches): they carry no table.
func (c *declCtx) fresh() map[types.Object]bool {
	if c.freshParams != nil {
		return c.freshParams
	}

	c.freshParams = map[types.Object]bool{}

	ast.Inspect(c.d.Node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		g, ok := c.gormCall(call)
		if !ok || g.name != "Transaction" && g.name != "Connection" {
			return true
		}

		for _, a := range call.Args {
			lit, ok := ast.Unparen(a).(*ast.FuncLit)
			if !ok {
				continue
			}

			for _, f := range lit.Type.Params.List {
				for _, id := range f.Names {
					if o := c.info.Defs[id]; o != nil {
						c.freshParams[o] = true
					}
				}
			}
		}

		return true
	})

	return c.freshParams
}

func (c *declCtx) findStores() {
	ast.Inspect(c.d.Node, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if len(n.Lhs) != len(n.Rhs) {
				return true
			}

			for i, l := range n.Lhs {
				c.store(l, n.Rhs[i])
			}
		case *ast.CompositeLit:
			c.storeFields(n)
		case *ast.ValueSpec:
			if len(n.Names) == len(n.Values) {
				for i, id := range n.Names {
					c.store(id, n.Values[i])
				}
			}
		}

		return true
	})
}

func (c *declCtx) storeFields(n *ast.CompositeLit) {
	t := c.info.TypeOf(n)
	if t == nil {
		return
	}

	if _, ok := t.Underlying().(*types.Struct); !ok {
		return
	}

	for _, el := range n.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}

		if key, ok := kv.Key.(*ast.Ident); ok && isHandle(c.info.TypeOf(kv.Value)) {
			k := fieldKey(t, key.Name)
			c.a.stores[k] = append(c.a.stores[k], stored{ctx: c, expr: kv.Value})
		}
	}
}

func (c *declCtx) store(l, r ast.Expr) {
	if !isHandle(c.info.TypeOf(r)) {
		return
	}

	switch l := ast.Unparen(l).(type) {
	case *ast.SelectorExpr:
		if s, ok := c.info.Selections[l]; ok && s.Kind() == types.FieldVal {
			k := fieldKey(s.Recv(), l.Sel.Name)
			c.a.stores[k] = append(c.a.stores[k], stored{ctx: c, expr: r})
		}
	case *ast.Ident:
		o := c.info.Defs[l]
		if o == nil {
			o = c.info.Uses[l]
		}

		if v, ok := o.(*types.Var); ok && v.Pkg() != nil && v.Parent() == v.Pkg().Scope() {
			k := v.Pkg().Path() + "." + v.Name()
			c.a.stores[k] = append(c.a.stores[k], stored{ctx: c, expr: r})
		}
	}
}

// findCallbacks records functions registered with GORM's callbacks
// (db.Callback().Create().Register("name", fn)): GORM runs them for every
// statement of that kind.
func (c *declCtx) findCallbacks() {
	ast.Inspect(c.d.Node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Register" && sel.Sel.Name != "Replace" {
			return true
		}

		s, ok := c.info.Selections[sel]
		if !ok || gormNamed(s.Recv()) == nil {
			return true
		}

		kind := callbackKind(sel.X)
		if kind == "" {
			return true
		}

		// a callback a test registers runs only in that test's process,
		// which its own slice covers
		if goid.ID(c.id).Test() {
			return true
		}

		target := c.id

		if id := identOf(ast.Unparen(call.Args[1])); id != nil {
			if fn, ok := c.info.Uses[id].(*types.Func); ok {
				if src := c.a.funcs[funcKey(fn)]; src != nil {
					target = src.id
				}
			}
		}

		c.a.callbacks[kind] = append(c.a.callbacks[kind], target)

		return true
	})
}

var processors = map[string]string{
	mCreate: opCreate, "Query": opQuery, mUpdate: opUpdate, mDelete: opDelete, mRow: opRow, "Raw": opRaw,
}

func callbackKind(e ast.Expr) string {
	for {
		call, ok := ast.Unparen(e).(*ast.CallExpr)
		if !ok {
			return ""
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return ""
		}

		if k, ok := processors[sel.Sel.Name]; ok {
			return k
		}

		e = sel.X
	}
}

func (a *analyzer) linkCallbacks() {
	for decl, kinds := range a.triggers {
		for kind := range kinds {
			for _, cb := range a.callbacks[kind] {
				if cb != decl {
					a.add(Link{From: decl, To: cb, Kind: KindCalls}, "callback registered for "+kind)
				}
			}
		}
	}
}

// linkModels links each model's type and TableName method to its tables:
// a change to the model reaches the table, and the dependents width can go
// from a table to the types that map it.
func (a *analyzer) linkModels() {
	keys := sortedKeys(a.models)
	sort.Strings(keys)

	for _, k := range keys {
		m := a.models[k]

		for _, t := range m.tables {
			if m.decl != "" {
				a.add(Link{From: m.decl, To: t.ID(), Kind: KindUses, Narrowing: NarrowModelTypes, While: WhileOff}, "model "+k)
			}

			if m.tableNameDecl != "" {
				a.add(Link{From: m.tableNameDecl, To: t.ID(), Kind: KindUses}, "TableName of "+k)
			}
		}

		if m.unknown != "" && m.tableNameDecl != "" {
			a.cantTell(m.tableNameDecl, m.unknown, "")
		}
	}
}
