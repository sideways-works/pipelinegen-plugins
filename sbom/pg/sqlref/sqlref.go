// Package sqlref reads which relations and functions SQL names, with
// PostgreSQL's own parser (libpg_query, run as WebAssembly so nothing
// needs a C toolchain).
package sqlref

import (
	"fmt"
	"sort"
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"
	pgquery "github.com/wasilibs/go-pgquery"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Ref is a relation or function some SQL names.
type Ref struct {
	// Function is true for a function call, false for a relation.
	Function bool
	// Schema is empty when the SQL left the name unqualified.
	Schema string
	Name   string
	// Read: the SQL reads rows of the relation (a SELECT, a join, an
	// UPDATE's or DELETE's own target, an INSERT with ON CONFLICT).
	// Write: it inserts, updates, deletes, merges, truncates or copies
	// into it. Both can be true.
	Read, Write bool
}

func (r Ref) key() string {
	return fmt.Sprintf("%t\x00%s\x00%s", r.Function, r.Schema, r.Name)
}

// Parse parses SQL text. PL/pgSQL evaluates an expression or an
// assignment ("NEW.total := (SELECT ...)") as SELECT <expression> and a
// server logs it as written, so text that doesn't parse is tried that way
// too.
func Parse(sql string) (*pg.ParseResult, error) {
	tree, err := pgquery.Parse(sql)
	if err == nil {
		return tree, nil
	}

	expr := sql
	if _, value, ok := strings.Cut(sql, ":="); ok {
		expr = value
	}

	if again, err2 := pgquery.Parse("SELECT " + expr); err2 == nil {
		return again, nil
	}

	return nil, fmt.Errorf("parsing: %w", err)
}

// Refs lists what SQL text names, sorted and merged per name, without
// PostgreSQL's own functions and catalogs.
func Refs(sql string) ([]Ref, error) {
	tree, err := Parse(sql)
	if err != nil {
		return nil, err
	}

	nodes := make([]*pg.Node, 0, len(tree.GetStmts()))
	for _, raw := range tree.GetStmts() {
		nodes = append(nodes, raw.GetStmt())
	}

	return Of(nodes...), nil
}

// Of lists what parsed statements or expressions name.
func Of(nodes ...*pg.Node) []Ref {
	w := walker{refs: map[string]*Ref{}, writes: map[*pg.RangeVar]bool{}, upserts: map[*pg.RangeVar]bool{}}

	for _, n := range nodes {
		if n == nil {
			continue
		}

		w.markWrites(n.ProtoReflect())
		w.visit(n.ProtoReflect())
	}

	out := make([]Ref, 0, len(w.refs))
	for _, r := range w.refs {
		out = append(out, *r)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })

	return out
}

type walker struct {
	refs map[string]*Ref
	// writes: the targets of writing statements, found before the walk
	// reaches them as plain range variables
	writes map[*pg.RangeVar]bool
	// upserts: write targets that also read: an UPDATE's, DELETE's or
	// MERGE's own target, an INSERT ... ON CONFLICT's
	upserts map[*pg.RangeVar]bool
}

func (w *walker) markWrites(m protoreflect.Message) {
	switch s := m.Interface().(type) {
	case *pg.InsertStmt:
		w.writes[s.GetRelation()] = true
		if s.GetOnConflictClause() != nil {
			w.upserts[s.GetRelation()] = true
		}
	case *pg.UpdateStmt:
		w.writes[s.GetRelation()] = true
		w.upserts[s.GetRelation()] = true
	case *pg.DeleteStmt:
		w.writes[s.GetRelation()] = true
		w.upserts[s.GetRelation()] = true
	case *pg.MergeStmt:
		w.writes[s.GetRelation()] = true
		w.upserts[s.GetRelation()] = true
	case *pg.TruncateStmt:
		for _, r := range s.GetRelations() {
			w.writes[r.GetRangeVar()] = true
		}
	case *pg.CopyStmt:
		if s.GetIsFrom() {
			w.writes[s.GetRelation()] = true
		}
	}

	Children(m, w.markWrites)
}

func (w *walker) visit(m protoreflect.Message) {
	switch v := m.Interface().(type) {
	case *pg.RangeVar:
		write := w.writes[v]
		w.add(Ref{Schema: v.GetSchemaname(), Name: v.GetRelname(), Read: !write || w.upserts[v], Write: write})

		return
	case *pg.FuncCall:
		if schema, name, ok := FuncName(v.GetFuncname()); ok {
			w.add(Ref{Function: true, Schema: schema, Name: name, Read: true})
		}
	case *pg.LockingClause:
		// FOR UPDATE OF x names a range of the query by its alias
		return
	}

	Children(m, w.visit)
}

// add records a name, leaving out PostgreSQL's own: a call of a built-in
// function and a read of a system catalog are never the repository's.
func (w *walker) add(r Ref) {
	if r.Name == "" || r.Function && r.Schema == "" && Builtin(r.Name) || !r.Function && System(r.Schema, r.Name) {
		return
	}

	k := r.key()
	if have, ok := w.refs[k]; ok {
		have.Read = have.Read || r.Read
		have.Write = have.Write || r.Write

		return
	}

	w.refs[k] = &r
}

// FuncName reads a possibly qualified function name. A name qualified
// with pg_catalog is PostgreSQL's own and never a repository's, so ok is
// false for it.
func FuncName(parts []*pg.Node) (schema, name string, ok bool) {
	names := Strings(parts)
	if len(names) != len(parts) {
		return "", "", false
	}

	switch len(names) {
	case 1:
		return "", names[0], true
	case 2:
		if names[0] == "pg_catalog" {
			return "", "", false
		}

		return names[0], names[1], true
	default:
		return "", "", false
	}
}

// Strings reads a list of String nodes; nodes of another kind are left out.
func Strings(nodes []*pg.Node) []string {
	out := make([]string, 0, len(nodes))

	for _, n := range nodes {
		if s := n.GetString_(); s != nil {
			out = append(out, s.GetSval())
		}
	}

	return out
}

// Children calls fn for every message directly inside m, in field order.
func Children(m protoreflect.Message, fn func(protoreflect.Message)) {
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Kind() != protoreflect.MessageKind || fd.IsMap() {
			return true
		}

		if fd.IsList() {
			l := v.List()
			for i := range l.Len() {
				fn(l.Get(i).Message())
			}

			return true
		}

		fn(v.Message())

		return true
	})
}
