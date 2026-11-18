package model

import (
	pg "github.com/pganalyze/pg_query_go/v6"

	"github.com/sideways-works/pipelinegen-sbom-pg/pgid"
	"github.com/sideways-works/pipelinegen-sbom-pg/sqlref"
)

var serials = map[string]bool{
	"serial": true, "serial4": true, "bigserial": true, "serial8": true, "smallserial": true, "serial2": true,
}

func (m *Model) createTable(s *pg.CreateStmt, fp string) {
	if s.GetRelation().GetRelpersistence() == "t" {
		return
	}

	schema, name := rangeVarName(s.GetRelation())

	if have := m.relation(schema, name); have != nil {
		// CREATE TABLE IF NOT EXISTS of a table that exists does nothing,
		// but its text is still the table's history
		m.touch(have, fp)

		return
	}

	t := m.add(pgid.Table, m.creationSchema(schema), name, fp)
	t.constraints = map[string]*object{}
	t.triggers = map[string][]*object{}
	t.indexes = map[string]bool{}

	for _, e := range s.GetTableElts() {
		switch x := e.GetNode().(type) {
		case *pg.Node_ColumnDef:
			m.column(t, x.ColumnDef, fp)
		case *pg.Node_Constraint:
			m.constraint(t, x.Constraint, nil, fp)
		case *pg.Node_TableLikeClause:
			ls, ln := rangeVarName(x.TableLikeClause.GetRelation())
			t.use(m.relation(ls, ln))
		}
	}

	for _, c := range s.GetConstraints() {
		m.constraint(t, c.GetConstraint(), nil, fp)
	}

	for _, inh := range s.GetInhRelations() {
		ps, pn := rangeVarName(inh.GetRangeVar())
		if parent := m.relation(ps, pn); parent != nil {
			// a query on the parent reads its children, and a child takes
			// its columns from the parent
			parent.use(t)
			t.use(parent)
			m.touch(parent, fp)
		}
	}

	if of := s.GetOfTypename(); of != nil {
		t.use(m.typeObject(of))
	}
}

func (m *Model) column(t *object, c *pg.ColumnDef, fp string) {
	tn := c.GetTypeName()
	if tn != nil && len(tn.GetNames()) == 1 && serials[sqlref.Strings(tn.GetNames())[0]] {
		m.implicitSequence(t, c.GetColname(), fp)
	} else {
		t.use(m.typeObject(tn))
	}

	if c.GetRawDefault() != nil {
		m.expression(t, c.GetRawDefault())
	}

	for _, n := range c.GetConstraints() {
		m.constraint(t, n.GetConstraint(), c, fp)
	}
}

func (m *Model) implicitSequence(t *object, column, fp string) {
	seq := m.add(pgid.Sequence, t.schema, chooseName(t.name, column, "seq"), fp)
	t.use(seq)
	t.owned = append(t.owned, seq)
}

func (m *Model) expression(t *object, e *pg.Node) {
	for _, r := range sqlref.Of(e) {
		t.refs = append(t.refs, pendingRef{ref: r, path: m.path, kind: Uses})
	}

	// nextval('name') names its sequence in a string
	for _, seq := range nextvalSequences(e) {
		s, n := splitName(splitQualified(seq))
		t.use(m.find([]string{pgid.Sequence}, s, n))
	}
}

func (m *Model) constraint(t *object, c *pg.Constraint, col *pg.ColumnDef, fp string) {
	if c == nil {
		return
	}

	name := c.GetConname()

	switch c.GetContype() {
	case pg.ConstrType_CONSTR_FOREIGN:
		ps, pn := rangeVarName(c.GetPktable())
		parent := m.find([]string{pgid.Table}, ps, pn)

		if name == "" {
			cols := sqlref.Strings(c.GetFkAttrs())
			if col != nil {
				cols = []string{col.GetColname()}
			}

			name = m.constraintName(t, cols, "fkey")
		}

		t.constraints[name] = parent

		// the key changes what a delete or update on the parent does, so
		// it is the parent's history as well
		m.touch(parent, fp)
	case pg.ConstrType_CONSTR_IDENTITY:
		if col != nil {
			m.implicitSequence(t, col.GetColname(), fp)
		}
	case pg.ConstrType_CONSTR_DEFAULT, pg.ConstrType_CONSTR_CHECK, pg.ConstrType_CONSTR_GENERATED:
		if c.GetRawExpr() != nil {
			m.expression(t, c.GetRawExpr())
		}
	case pg.ConstrType_CONSTR_EXCLUSION, pg.ConstrType_CONSTR_PRIMARY, pg.ConstrType_CONSTR_UNIQUE:
		if c.GetWhereClause() != nil {
			m.expression(t, c.GetWhereClause())
		}
	default:
	}

	if name == "" {
		name = m.generatedConstraintName(t, c, col)
	}

	if name != "" {
		if _, ok := t.constraints[name]; !ok {
			t.constraints[name] = nil
		}
	}

	// a primary key, unique or exclusion constraint makes an index of the
	// same name
	switch c.GetContype() {
	case pg.ConstrType_CONSTR_PRIMARY, pg.ConstrType_CONSTR_UNIQUE, pg.ConstrType_CONSTR_EXCLUSION:
		if c.GetIndexname() != "" {
			name = c.GetIndexname()
		}

		t.indexes[t.schema+"."+name] = true
	default:
	}
}

func (m *Model) generatedConstraintName(t *object, c *pg.Constraint, col *pg.ColumnDef) string {
	var cols []string
	if col != nil {
		cols = []string{col.GetColname()}
	}

	switch c.GetContype() {
	case pg.ConstrType_CONSTR_PRIMARY:
		return chooseName(t.name, "", "pkey")
	case pg.ConstrType_CONSTR_UNIQUE:
		if len(cols) == 0 {
			cols = sqlref.Strings(c.GetKeys())
		}

		return m.constraintName(t, cols, "key")
	case pg.ConstrType_CONSTR_CHECK:
		// a table check takes its column's name only when it refers to
		// exactly one column
		if len(cols) == 0 {
			cols = distinct(columnRefs(c.GetRawExpr()))
			if len(cols) != 1 {
				cols = nil
			}
		}

		return m.constraintName(t, cols, "check")
	case pg.ConstrType_CONSTR_EXCLUSION:
		for _, e := range c.GetExclusions() {
			if l := e.GetList().GetItems(); len(l) > 0 {
				if ie := l[0].GetIndexElem(); ie != nil && ie.GetName() != "" {
					cols = append(cols, ie.GetName())
				}
			}
		}

		return m.constraintName(t, cols, "excl")
	default:
		return ""
	}
}

// constraintName picks a free name the way PostgreSQL does:
// table_col1_col2_suffix, then with 1, 2, ... appended.
func (m *Model) constraintName(t *object, cols []string, suffix string) string {
	base := chooseName(t.name, joinCols(cols), suffix)
	name := base

	for i := 1; ; i++ {
		_, taken := t.constraints[name]
		if !taken && !m.indexTaken(t.schema, name) && m.relation(t.schema, name) == nil {
			return name
		}

		name = chooseNameNumbered(t.name, joinCols(cols), suffix, i)
	}
}

func (m *Model) indexTaken(schema, name string) bool {
	return m.indexOwner(schema, name) != nil
}

func (m *Model) alterTable(s *pg.AlterTableStmt, fp string) {
	schema, name := rangeVarName(s.GetRelation())

	var t *object

	if s.GetObjtype() == pg.ObjectType_OBJECT_INDEX {
		t = m.indexOwner(schema, name)
	} else {
		t = m.relation(schema, name)
	}

	if t == nil {
		if !s.GetMissingOk() {
			m.unresolved("ALTER of " + name)
		}

		return
	}

	m.touch(t, fp)

	for _, c := range s.GetCmds() {
		m.alterCmd(t, c.GetAlterTableCmd(), fp)
	}
}

func (m *Model) alterCmd(t *object, c *pg.AlterTableCmd, fp string) {
	if t.constraints == nil {
		t.constraints = map[string]*object{}
		t.indexes = map[string]bool{}
		t.triggers = map[string][]*object{}
	}

	switch c.GetSubtype() {
	case pg.AlterTableType_AT_AddColumn:
		m.column(t, c.GetDef().GetColumnDef(), fp)
	case pg.AlterTableType_AT_AlterColumnType:
		if cd := c.GetDef().GetColumnDef(); cd != nil {
			t.use(m.typeObject(cd.GetTypeName()))

			if cd.GetRawDefault() != nil {
				m.expression(t, cd.GetRawDefault())
			}
		}
	case pg.AlterTableType_AT_ColumnDefault:
		if c.GetDef() != nil {
			m.expression(t, c.GetDef())
		}
	case pg.AlterTableType_AT_AddConstraint:
		m.constraint(t, c.GetDef().GetConstraint(), nil, fp)
	case pg.AlterTableType_AT_DropConstraint:
		parent, known := t.constraints[c.GetName()]
		if !known && !c.GetMissingOk() {
			m.unresolved("constraint " + c.GetName() + " of " + t.name)
		}

		// a dropped foreign key changes its parent too
		m.touch(parent, fp)
		delete(t.constraints, c.GetName())
		delete(t.indexes, t.schema+"."+c.GetName())
	case pg.AlterTableType_AT_AttachPartition:
		if pc := c.GetDef().GetPartitionCmd(); pc != nil {
			ps, pn := rangeVarName(pc.GetName())
			if child := m.relation(ps, pn); child != nil {
				t.use(child)
				child.use(t)
				m.touch(child, fp)
			}
		}
	case pg.AlterTableType_AT_DetachPartition:
		if pc := c.GetDef().GetPartitionCmd(); pc != nil {
			ps, pn := rangeVarName(pc.GetName())
			m.touch(m.relation(ps, pn), fp)
		}
	default:
		// every other subcommand (not null, triggers on or off, options,
		// row security, validation) is in the table's history already
	}
}

func (m *Model) createIndex(s *pg.IndexStmt, fp string) {
	schema, name := rangeVarName(s.GetRelation())

	t := m.relation(schema, name)
	if t == nil {
		m.unresolved("index on " + name)

		return
	}

	if t.indexes == nil {
		t.indexes = map[string]bool{}
	}

	idx := s.GetIdxname()
	if idx == "" {
		idx = m.indexName(t, s)
	}

	t.indexes[t.schema+"."+idx] = true
	m.touch(t, fp)

	for _, p := range s.GetIndexParams() {
		if e := p.GetIndexElem(); e != nil && e.GetExpr() != nil {
			m.expression(t, e.GetExpr())
		}

		m.opclassExtension(t, p.GetIndexElem())
	}

	if s.GetWhereClause() != nil {
		m.expression(t, s.GetWhereClause())
	}
}

// indexName picks an unnamed index's name the way PostgreSQL does:
// table_col1_col2_idx (or _key for a unique index), expressions as "expr".
func (m *Model) indexName(t *object, s *pg.IndexStmt) string {
	var cols []string

	for _, p := range s.GetIndexParams() {
		e := p.GetIndexElem()
		if e.GetName() != "" {
			cols = append(cols, e.GetName())
		} else {
			cols = append(cols, "expr")
		}
	}

	suffix := "idx"
	if s.GetUnique() {
		suffix = "key"
	}

	base := chooseName(t.name, joinCols(cols), suffix)
	name := base

	for i := 1; m.indexTaken(t.schema, name) || m.relation(t.schema, name) != nil; i++ {
		name = chooseNameNumbered(t.name, joinCols(cols), suffix, i)
	}

	return name
}

// opclassExtension links a table to the extension of an index's operator
// class, such as gin_trgm_ops from pg_trgm.
func (m *Model) opclassExtension(t *object, e *pg.IndexElem) {
	if e == nil {
		return
	}

	for _, op := range sqlref.Strings(e.GetOpclass()) {
		for _, ext := range m.providers("opclass", op) {
			t.use(ext)
		}
	}
}

func (m *Model) createTableAs(s *pg.CreateTableAsStmt, fp string) {
	// a temporary table lives for the migration's session only
	if s.GetInto().GetRel().GetRelpersistence() == "t" {
		return
	}

	schema, name := rangeVarName(s.GetInto().GetRel())
	kind := pgid.Table

	if s.GetObjtype() == pg.ObjectType_OBJECT_MATVIEW {
		kind = pgid.View
	}

	if have := m.relation(schema, name); have != nil {
		m.touch(have, fp)

		return
	}

	o := m.add(kind, m.creationSchema(schema), name, fp)
	o.constraints = map[string]*object{}
	o.indexes = map[string]bool{}
	o.triggers = map[string][]*object{}

	for _, r := range sqlref.Of(s.GetQuery()) {
		if kind == pgid.View {
			o.refs = append(o.refs, pendingRef{ref: r, path: m.path, kind: Reads})
		} else {
			o.dataFrom = append(o.dataFrom, pendingRef{ref: r, path: m.path, kind: Reads})
		}
	}

	if kind == pgid.Table && !s.GetInto().GetSkipData() {
		o.data = append(o.data, fp)
	}
}

func (m *Model) createView(s *pg.ViewStmt, fp string) {
	schema, name := rangeVarName(s.GetView())

	v := m.find([]string{pgid.View}, schema, name)
	if v == nil {
		v = m.add(pgid.View, m.creationSchema(schema), name, fp)
	} else {
		m.touch(v, fp)
	}

	v.refs = nil

	for _, r := range sqlref.Of(s.GetQuery()) {
		v.refs = append(v.refs, pendingRef{ref: r, path: m.path, kind: Reads})
	}
}

func (m *Model) policy(rel *pg.RangeVar, fp string, exprs ...*pg.Node) {
	t := m.relation(rangeVarName(rel))
	if t == nil {
		m.unresolved("policy or rule on " + rel.GetRelname())

		return
	}

	m.touch(t, fp)

	for _, e := range exprs {
		if e != nil {
			m.expression(t, e)
		}
	}
}

func (m *Model) grant(s *pg.GrantStmt, fp string) {
	if s.GetTargtype() != pg.GrantTargetType_ACL_TARGET_OBJECT {
		return
	}

	switch s.GetObjtype() {
	case pg.ObjectType_OBJECT_TABLE, pg.ObjectType_OBJECT_SEQUENCE:
		for _, o := range s.GetObjects() {
			m.touch(m.relation(rangeVarName(o.GetRangeVar())), fp)
		}
	case pg.ObjectType_OBJECT_FUNCTION, pg.ObjectType_OBJECT_PROCEDURE, pg.ObjectType_OBJECT_ROUTINE:
		for _, o := range s.GetObjects() {
			for _, f := range m.functionsOf(o.GetObjectWithArgs()) {
				m.touch(f, fp)
			}
		}
	default:
	}
}

func (m *Model) data(n *pg.Node, fp string) {
	refs := sqlref.Of(n)

	var wrote bool

	for _, r := range refs {
		if r.Function {
			for _, f := range m.functions(r.Schema, r.Name, nil) {
				// a migration that calls the repository's own function
				// writes whatever that function writes
				m.calls(f, fp)

				wrote = true
			}

			continue
		}

		if !r.Write {
			continue
		}

		t := m.relation(r.Schema, r.Name)
		if t == nil {
			continue
		}

		m.writeData(t, fp)

		wrote = true

		for _, from := range refs {
			if !from.Function && !from.Write {
				t.dataFrom = append(t.dataFrom, pendingRef{ref: from, path: m.path, kind: Reads})
			}
		}
	}

	if !wrote {
		if _, isSelect := n.GetNode().(*pg.Node_SelectStmt); isSelect {
			// a SELECT that calls only PostgreSQL's own functions (a
			// check, setval excepted below) changes nothing
			m.setvals(n, fp)

			return
		}

		m.unresolved("a " + nodeKind(n) + " that writes no table the model knows")
	}
}

func (m *Model) writeData(t *object, fp string) {
	if t == nil {
		return
	}

	t.data = append(t.data, fp)

	if m.stepIDs[m.file] == nil {
		m.stepIDs[m.file] = map[string]bool{}
	}

	m.stepIDs[m.file][pgid.Of(pgid.Data, t.schema, t.name)] = true
}

// calls records a migration's call of a function. The tables the function
// writes are known only at the end, so the call waits on the function.
func (m *Model) calls(f *object, fp string) {
	for _, r := range f.refs {
		if r.kind != Writes {
			continue
		}

		saved := m.path
		m.path = r.path

		m.writeData(m.relation(r.ref.Schema, r.ref.Name), fp)
		m.path = saved
	}

	if f.dynamic {
		m.opaqueStatement(fp, "a call of "+f.name+", which builds SQL at run time")
	}
}

// setvals finds setval('seq', n) in a SELECT: the sequence's next value
// is part of what the tests start from.
func (m *Model) setvals(n *pg.Node, fp string) {
	for _, seq := range functionStringArgs(n, "setval") {
		s, name := splitName(splitQualified(seq))
		m.touch(m.find([]string{pgid.Sequence}, s, name), fp)
	}
}

func distinct(s []string) []string {
	seen := map[string]bool{}
	out := s[:0]

	for _, x := range s {
		if !seen[x] {
			seen[x] = true

			out = append(out, x)
		}
	}

	return out
}
