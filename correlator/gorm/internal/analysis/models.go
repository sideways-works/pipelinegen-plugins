package analysis

import (
	"go/ast"
	"go/constant"
	"go/types"
	"reflect"
	"sort"

	"gorm.io/gorm/schema"
)

const gormPath = "gorm.io/gorm"

type Model struct {
	key  string
	decl string
	// tables is where the model's rows live; one, unless TableName
	// returns one of several constants.
	tables        []Relation
	unknown       string
	tableNameDecl string
	assocs        map[string]*assoc
	columns       []Column
	hooks         map[string]string
	softDelete    bool
}

type assoc struct {
	related   *types.Named
	joinTable string
}

type Column struct {
	Field  string `json:"field"`
	Column string `json:"column"`
}

type ModelInfo struct {
	Type         string            `json:"type"`
	Decl         string            `json:"decl,omitempty"`
	Tables       []string          `json:"tables,omitempty"`
	Unknown      string            `json:"unknown,omitempty"`
	Columns      []Column          `json:"columns,omitempty"`
	Associations map[string]string `json:"associations,omitempty"`
	Hooks        []string          `json:"hooks,omitempty"`
	SoftDelete   bool              `json:"soft_delete,omitempty"`
}

func (m *Model) info() ModelInfo {
	mi := ModelInfo{Type: m.key, Decl: m.decl, Unknown: m.unknown, Columns: m.columns, SoftDelete: m.softDelete}

	for _, t := range m.tables {
		mi.Tables = append(mi.Tables, t.ID())
	}

	if len(m.assocs) > 0 {
		mi.Associations = map[string]string{}
		for name, as := range m.assocs {
			mi.Associations[name] = typeKey(as.related)
		}
	}

	for h := range m.hooks {
		mi.Hooks = append(mi.Hooks, h)
	}

	sort.Strings(mi.Hooks)

	return mi
}

// The hooks GORM calls on a model, by the kind of statement.
var hooksFor = map[string][]string{
	opCreate: {"BeforeSave", "BeforeCreate", "AfterCreate", "AfterSave"},
	opUpdate: {"BeforeSave", "BeforeUpdate", "AfterUpdate", "AfterSave"},
	opDelete: {"BeforeDelete", "AfterDelete"},
	opQuery:  {"AfterFind"},
}

const whyInterface = "interface"

// named finds the struct type a value of type t stands for as a model
// (through pointers, slices and arrays). It says "interface" when the type
// is an interface: then the model is chosen at run time.
func named(t types.Type) (*types.Named, string) {
	for {
		switch u := types.Unalias(t).(type) {
		case *types.Pointer:
			t = u.Elem()
		case *types.Slice:
			t = u.Elem()
		case *types.Array:
			t = u.Elem()
		case *types.Named:
			switch u.Underlying().(type) {
			case *types.Struct:
				return u, ""
			case *types.Interface:
				return nil, whyInterface
			case *types.Pointer, *types.Slice, *types.Array:
				t = u.Underlying()
			default:
				return nil, ""
			}
		case *types.Interface, *types.TypeParam:
			return nil, whyInterface
		default:
			return nil, ""
		}
	}
}

func typeKey(n *types.Named) string {
	o := n.Origin().Obj()
	if o.Pkg() == nil {
		return o.Name()
	}

	return o.Pkg().Path() + "." + o.Name()
}

// funcKey names a function or method across packages: a package loaded
// from source and the same package seen through export data give different
// objects, but the same key.
func funcKey(fn *types.Func) string {
	fn = fn.Origin()

	pkg := ""
	if fn.Pkg() != nil {
		pkg = fn.Pkg().Path()
	}

	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return pkg + "." + fn.Name()
	}

	t := sig.Recv().Type()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}

	if n, ok := types.Unalias(t).(*types.Named); ok {
		return pkg + "." + n.Origin().Obj().Name() + "." + fn.Name()
	}

	return pkg + ".?." + fn.Name()
}

func (a *analyzer) model(n *types.Named) *Model {
	key := typeKey(n)
	if m, ok := a.models[key]; ok {
		return m
	}

	m := &Model{key: key, decl: a.typeDecls[key], assocs: map[string]*assoc{}, hooks: map[string]string{}}
	a.models[key] = m
	a.namedOf[key] = n

	a.tableOf(m, n)

	st, _ := n.Underlying().(*types.Struct) //nolint:errcheck // named only returns struct types
	a.fields(m, st, "")

	ptr := types.NewPointer(n)

	for _, hs := range hooksFor {
		for _, h := range hs {
			if fn := method(ptr, n, h); fn != nil {
				if src := a.funcs[funcKey(fn)]; src != nil {
					m.hooks[h] = src.id
				}
			}
		}
	}

	return m
}

func method(t types.Type, n *types.Named, name string) *types.Func {
	obj, _, _ := types.LookupFieldOrMethod(t, true, n.Obj().Pkg(), name)
	fn, _ := obj.(*types.Func) //nolint:errcheck // a field or nothing is no method

	return fn
}

// tableOf finds a model's table: TableName on the model or a struct it
// embeds (GORM asks a new value of the pointer type), else the naming
// strategy on the type's name.
func (a *analyzer) tableOf(m *Model, n *types.Named) {
	fn := method(types.NewPointer(n), n, "TableName")
	if fn == nil {
		m.tables = []Relation{a.cfg.relation(a.cfg.Naming.strategy().TableName(n.Obj().Name()))}

		return
	}

	a.stats.TableNames++

	src := a.funcs[funcKey(fn)]
	if src == nil {
		m.unknown = "TableName is declared outside the loaded modules"

		return
	}

	m.tableNameDecl = src.id

	names, ok := a.tableNames(src)
	if !ok || len(names) == 0 {
		m.unknown = "TableName returns a name built at run time"

		return
	}

	for _, name := range names {
		m.tables = append(m.tables, a.cfg.relation(name))
	}
}

func (a *analyzer) tableNames(src *funcSrc) ([]string, bool) {
	if src.decl.Body == nil {
		return nil, false
	}

	var (
		names []string
		ok    = true
	)

	ast.Inspect(src.decl.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			if len(n.Results) != 1 {
				ok = false

				return false
			}

			if s, isConst := constString(src.info, n.Results[0]); isConst {
				names = append(names, s)

				return false
			}

			if s, isNamer := a.namerCall(src.info, n.Results[0]); isNamer {
				names = append(names, s)

				return false
			}

			ok = false

			return false
		}

		return true
	})

	return names, ok
}

func (a *analyzer) namerCall(info *types.Info, e ast.Expr) (string, bool) {
	call, ok := ast.Unparen(e).(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return "", false
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "TableName" {
		return "", false
	}

	s, ok := constString(info, call.Args[0])
	if !ok {
		return "", false
	}

	return a.cfg.Naming.strategy().TableName(s), true
}

// fields reads a struct's columns and associations the way GORM's schema
// parser does: embedded structs flatten into the model, a struct-typed
// field that isn't a scanner or valuer is an association.
func (a *analyzer) fields(m *Model, st *types.Struct, prefix string) {
	ns := a.cfg.Naming.strategy()

	for i := range st.NumFields() {
		f := st.Field(i)
		if !f.Exported() && !f.Embedded() {
			continue
		}

		tag := schema.ParseTagSetting(reflect.StructTag(st.Tag(i)).Get("gorm"), ";")
		if _, ignored := tag["-"]; ignored {
			continue
		}

		if isDeletedAt(f.Type()) {
			m.softDelete = true
		}

		rel, _ := named(f.Type())
		_, embedded := tag["EMBEDDED"]

		switch {
		case (f.Embedded() || embedded) && rel != nil && !valueType(rel):
			sub, _ := rel.Underlying().(*types.Struct) //nolint:errcheck // named only returns struct types
			a.fields(m, sub, prefix+tag["EMBEDDEDPREFIX"])
		case rel != nil && !valueType(rel) && tag["SERIALIZER"] == "" && tag["TYPE"] == "":
			as := &assoc{related: rel}
			if jt := tag["MANY2MANY"]; jt != "" {
				as.joinTable = ns.JoinTableName(jt)
			}

			m.assocs[f.Name()] = as
		default:
			col := tag["COLUMN"]
			if col == "" {
				col = ns.ColumnName("", f.Name())
			}

			m.columns = append(m.columns, Column{Field: f.Name(), Column: prefix + col})
		}
	}
}

// valueType says GORM stores a struct in a column rather than as an
// association: time.Time, and anything with Scan or Value.
func valueType(n *types.Named) bool {
	if o := n.Obj(); o.Pkg() != nil && o.Pkg().Path() == "time" {
		return true
	}

	ptr := types.NewPointer(n)

	return method(ptr, n, "Scan") != nil || method(ptr, n, "Value") != nil
}

func isDeletedAt(t types.Type) bool {
	n, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}

	o := n.Obj()

	return o.Pkg() != nil && (o.Pkg().Path() == gormPath && o.Name() == "DeletedAt" ||
		o.Pkg().Path() == "gorm.io/plugin/soft_delete" && o.Name() == "DeletedAt")
}

func (a *analyzer) assocTables(as *assoc) ([]Relation, string) {
	rm := a.model(as.related)
	if rm.unknown != "" {
		return nil, rm.unknown
	}

	out := append([]Relation(nil), rm.tables...)
	if as.joinTable != "" {
		out = append(out, a.cfg.relation(as.joinTable))
	}

	return out, ""
}

func (a *analyzer) saved(m *Model) ([]Relation, []string) {
	var (
		out     []Relation
		unknown []string
	)

	seen := map[string]bool{m.key: true}
	queue := []*Model{m}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		for _, name := range sortedKeys(cur.assocs) {
			as := cur.assocs[name]

			ts, why := a.assocTables(as)
			if why != "" {
				unknown = append(unknown, why)

				continue
			}

			out = append(out, ts...)

			if rk := typeKey(as.related); !seen[rk] {
				seen[rk] = true

				queue = append(queue, a.model(as.related))
			}
		}
	}

	return out, unknown
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	sort.Strings(out)

	return out
}

func constString(info *types.Info, e ast.Expr) (string, bool) {
	tv, ok := info.Types[e]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
		return "", false
	}

	return constant.StringVal(tv.Value), true
}
