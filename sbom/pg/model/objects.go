package model

import (
	pg "github.com/pganalyze/pg_query_go/v6"

	"github.com/sideways-works/pipelinegen-sbom-pg/pgid"
	"github.com/sideways-works/pipelinegen-sbom-pg/sqlref"
)

func (m *Model) rename(s *pg.RenameStmt, fp string) {
	switch s.GetRenameType() {
	case pg.ObjectType_OBJECT_TABLE, pg.ObjectType_OBJECT_VIEW, pg.ObjectType_OBJECT_MATVIEW,
		pg.ObjectType_OBJECT_FOREIGN_TABLE, pg.ObjectType_OBJECT_SEQUENCE:
		m.renameRelation(s, fp)
	case pg.ObjectType_OBJECT_INDEX:
		m.renameIndex(s, fp)
	case pg.ObjectType_OBJECT_COLUMN, pg.ObjectType_OBJECT_TABCONSTRAINT, pg.ObjectType_OBJECT_TRIGGER,
		pg.ObjectType_OBJECT_POLICY, pg.ObjectType_OBJECT_RULE:
		m.renameInside(s, fp)
	case pg.ObjectType_OBJECT_FUNCTION, pg.ObjectType_OBJECT_PROCEDURE, pg.ObjectType_OBJECT_ROUTINE:
		for _, f := range m.functionsOf(s.GetObject().GetObjectWithArgs()) {
			m.touch(f, fp)
			f.name = s.GetNewname()
			m.mark(f)
		}
	case pg.ObjectType_OBJECT_TYPE, pg.ObjectType_OBJECT_DOMAIN:
		schema, name := splitName(sqlref.Strings(s.GetObject().GetList().GetItems()))

		t := m.find([]string{pgid.Type}, schema, name)
		if t == nil {
			m.missing(s.GetMissingOk(), "type "+name)

			return
		}

		m.touch(t, fp)
		t.name = s.GetNewname()
		m.mark(t)
	default:
		m.unknown(&pg.Node{Node: &pg.Node_RenameStmt{RenameStmt: s}}, fp)
	}
}

func (m *Model) renameRelation(s *pg.RenameStmt, fp string) {
	o := m.relation(rangeVarName(s.GetRelation()))
	if o == nil {
		m.missing(s.GetMissingOk(), "rename of "+s.GetRelation().GetRelname())

		return
	}

	m.touch(o, fp)
	o.name = s.GetNewname()
	m.mark(o)
}

func (m *Model) renameIndex(s *pg.RenameStmt, fp string) {
	schema, name := rangeVarName(s.GetRelation())

	t := m.indexOwner(schema, name)
	if t == nil {
		m.missing(s.GetMissingOk(), "index "+name)

		return
	}

	delete(t.indexes, t.schema+"."+name)
	t.indexes[t.schema+"."+s.GetNewname()] = true
	m.touch(t, fp)
}

func (m *Model) renameInside(s *pg.RenameStmt, fp string) {
	t := m.relation(rangeVarName(s.GetRelation()))
	if t == nil {
		m.missing(s.GetMissingOk(), "rename in "+s.GetRelation().GetRelname())

		return
	}

	m.touch(t, fp)

	if s.GetRenameType() != pg.ObjectType_OBJECT_TABCONSTRAINT {
		return
	}

	if parent, ok := t.constraints[s.GetSubname()]; ok {
		delete(t.constraints, s.GetSubname())
		t.constraints[s.GetNewname()] = parent
		m.touch(parent, fp)
	}

	if t.indexes[t.schema+"."+s.GetSubname()] {
		delete(t.indexes, t.schema+"."+s.GetSubname())
		t.indexes[t.schema+"."+s.GetNewname()] = true
	}
}

func (m *Model) missing(ok bool, what string) {
	if !ok {
		m.unresolved(what)
	}
}

func (m *Model) drop(s *pg.DropStmt, fp string) {
	for _, obj := range s.GetObjects() {
		if !m.dropOne(s, obj, fp) {
			m.unknown(&pg.Node{Node: &pg.Node_DropStmt{DropStmt: s}}, fp)

			return
		}
	}
}

func (m *Model) dropOne(s *pg.DropStmt, obj *pg.Node, fp string) bool {
	switch s.GetRemoveType() {
	case pg.ObjectType_OBJECT_TABLE, pg.ObjectType_OBJECT_VIEW, pg.ObjectType_OBJECT_MATVIEW,
		pg.ObjectType_OBJECT_FOREIGN_TABLE, pg.ObjectType_OBJECT_SEQUENCE:
		schema, name := splitName(sqlref.Strings(obj.GetList().GetItems()))
		m.dropObject(m.relation(schema, name), s.GetMissingOk(), name, fp)
	case pg.ObjectType_OBJECT_INDEX:
		m.dropIndex(obj, s.GetMissingOk(), fp)
	case pg.ObjectType_OBJECT_FUNCTION, pg.ObjectType_OBJECT_PROCEDURE, pg.ObjectType_OBJECT_ROUTINE:
		m.dropFunction(obj.GetObjectWithArgs(), s.GetMissingOk(), fp)
	case pg.ObjectType_OBJECT_TYPE, pg.ObjectType_OBJECT_DOMAIN:
		schema, name := splitName(sqlref.Strings(obj.GetTypeName().GetNames()))
		m.dropObject(m.find([]string{pgid.Type}, schema, name), s.GetMissingOk(), name, fp)
	case pg.ObjectType_OBJECT_TRIGGER, pg.ObjectType_OBJECT_POLICY, pg.ObjectType_OBJECT_RULE:
		m.dropInside(obj, s.GetRemoveType() == pg.ObjectType_OBJECT_TRIGGER, s.GetMissingOk(), fp)
	case pg.ObjectType_OBJECT_EXTENSION:
		name := obj.GetString_().GetSval()
		m.dropObject(m.extension(name), s.GetMissingOk(), name, fp)
	case pg.ObjectType_OBJECT_SCHEMA:
		name := obj.GetString_().GetSval()
		for _, o := range m.objects {
			if o.schema == name && !o.dropped {
				m.dropObject(o, true, o.name, fp)
			}
		}
	default:
		return false
	}

	return true
}

func (m *Model) dropIndex(obj *pg.Node, missingOK bool, fp string) {
	schema, name := splitName(sqlref.Strings(obj.GetList().GetItems()))

	t := m.indexOwner(schema, name)
	if t == nil {
		m.missing(missingOK, "index "+name)

		return
	}

	delete(t.indexes, t.schema+"."+name)
	m.touch(t, fp)
}

func (m *Model) dropFunction(o *pg.ObjectWithArgs, missingOK bool, fp string) {
	// a function in pg_temp lived for the migration's session only
	if schema, _ := splitName(sqlref.Strings(o.GetObjname())); schema == "pg_temp" {
		return
	}

	fns := m.functionsOf(o)
	if len(fns) == 0 {
		m.missing(missingOK, "function "+nameOf(o))
	}

	for _, f := range fns {
		m.dropObject(f, true, f.name, fp)
	}
}

// dropInside drops a trigger, policy or rule: the last name is its own,
// the ones before it its table's.
func (m *Model) dropInside(obj *pg.Node, trigger, missingOK bool, fp string) {
	parts := sqlref.Strings(obj.GetList().GetItems())
	if len(parts) < 2 {
		return
	}

	schema, table := splitName(parts[:len(parts)-1])

	t := m.relation(schema, table)
	if t == nil {
		m.missing(missingOK, "table "+table)

		return
	}

	m.touch(t, fp)

	if trigger {
		m.dropTrigger(t, parts[len(parts)-1])
	}
}

func nameOf(o *pg.ObjectWithArgs) string {
	_, name := splitName(sqlref.Strings(o.GetObjname()))

	return name
}

func (m *Model) dropTrigger(t *object, name string) {
	gone := t.triggers[name]
	delete(t.triggers, name)

	for _, f := range gone {
		still := false

		for _, fns := range t.triggers {
			for _, g := range fns {
				still = still || g == f
			}
		}

		if !still {
			delete(t.uses, f)
		}
	}
}

// dropObject drops an object, and what depended on it changes too: CASCADE
// drops it, and without CASCADE it went first.
func (m *Model) dropObject(o *object, missingOK bool, name, fp string) {
	if o == nil {
		m.missing(missingOK, "drop of "+name)

		return
	}

	m.touch(o, fp)

	// its foreign keys no longer act on their parents
	for _, parent := range o.constraints {
		m.touch(parent, fp)
	}

	o.dropped = true

	for _, seq := range o.owned {
		if !seq.dropped {
			m.dropObject(seq, true, seq.name, fp)
		}
	}

	for _, other := range m.objects {
		if other.dropped || other == o {
			continue
		}

		if _, uses := other.uses[o]; uses || other.names(o) {
			m.touch(other, fp)
			delete(other.uses, o)
		}

		for c, parent := range other.constraints {
			if parent == o {
				m.touch(other, fp)
				delete(other.constraints, c)
			}
		}
	}
}

func (m *Model) setSchema(s *pg.AlterObjectSchemaStmt, fp string) {
	var o *object

	switch s.GetObjectType() {
	case pg.ObjectType_OBJECT_TABLE, pg.ObjectType_OBJECT_VIEW, pg.ObjectType_OBJECT_MATVIEW, pg.ObjectType_OBJECT_SEQUENCE:
		o = m.relation(rangeVarName(s.GetRelation()))
	case pg.ObjectType_OBJECT_TYPE, pg.ObjectType_OBJECT_DOMAIN:
		schema, name := splitName(sqlref.Strings(s.GetObject().GetList().GetItems()))
		o = m.find([]string{pgid.Type}, schema, name)
	case pg.ObjectType_OBJECT_FUNCTION, pg.ObjectType_OBJECT_PROCEDURE:
		for _, f := range m.functionsOf(s.GetObject().GetObjectWithArgs()) {
			m.touch(f, fp)
			f.schema = s.GetNewschema()
			m.mark(f)
		}

		return
	default:
		m.unknown(&pg.Node{Node: &pg.Node_AlterObjectSchemaStmt{AlterObjectSchemaStmt: s}}, fp)

		return
	}

	if o == nil {
		m.missing(s.GetMissingOk(), "object moved to "+s.GetNewschema())

		return
	}

	m.touch(o, fp)
	o.schema = s.GetNewschema()
	m.mark(o)
}

func (o *object) names(other *object) bool {
	for _, r := range o.refs {
		if r.ref.Function == (other.kind == pgid.Function) && r.ref.Name == other.name &&
			(r.ref.Schema == "" || r.ref.Schema == other.schema) {
			return true
		}
	}

	return false
}
