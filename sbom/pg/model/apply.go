package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/sideways-works/pipelinegen-plugins/sbom/pg/sqlref"
)

// Apply runs one migration file's statements against the model. A file the
// parser refuses is opaque (see Report.Unparsed).
func (m *Model) Apply(file, sql string) {
	m.file = file
	m.files = append(m.files, file)
	m.report.Files++

	tree, err := sqlref.Parse(sql)
	if err != nil {
		// what a file the parser refuses does can't be known: it reaches
		// every unit, and the file itself can't be told
		sum := sha256.Sum256([]byte(sql))
		m.opaqueStatement(hex.EncodeToString(sum[:]), "the file doesn't parse: "+err.Error())
		m.report.Unparsed = append(m.report.Unparsed, file)

		return
	}

	for _, raw := range tree.GetStmts() {
		text := statementText(sql, raw)
		m.statement(raw.GetStmt(), text)
	}

	// SET LOCAL lasts until the file's transaction ends
	m.path = m.sessionPath

	// a table renamed in this file shows under its new name as well
	for o := range m.changed[file] {
		m.stepIDs[file][o.id()] = true
	}
}

// statementText cuts one statement's source out of the file, for parsing
// a function body with the PL/pgSQL parser, which needs the source.
func statementText(sql string, raw *pg.RawStmt) string {
	start := int(raw.GetStmtLocation())
	end := len(sql)

	if n := int(raw.GetStmtLen()); n > 0 {
		end = start + n
	}

	if start < 0 || start > len(sql) || end > len(sql) || start > end {
		return sql
	}

	return sql[start:end]
}

// fingerprint is a statement's identity in a digest: its parse tree
// without source positions, so whitespace and comments change nothing.
func fingerprint(n proto.Message) string {
	c := proto.Clone(n)
	clearLocations(c.ProtoReflect())

	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(c)
	if err != nil {
		// a parse tree always marshals; fall back to its text form
		b = []byte(fmt.Sprint(c))
	}

	sum := sha256.Sum256(b)

	return hex.EncodeToString(sum[:16])
}

func clearLocations(m protoreflect.Message) {
	fields := m.Descriptor().Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if fd.Kind() == protoreflect.Int32Kind && strings.HasSuffix(string(fd.Name()), "location") && m.Has(fd) {
			m.Clear(fd)
		}
	}

	sqlref.Children(m, clearLocations)
}

func (m *Model) count(n *pg.Node) {
	m.report.Statements++
	m.report.Kinds[nodeKind(n)]++
}

func nodeKind(n *pg.Node) string {
	if n == nil || n.GetNode() == nil {
		return "empty"
	}

	t := reflect.TypeOf(n.GetNode()).Elem().Name()

	return strings.TrimPrefix(t, "Node_")
}

func (m *Model) opaqueStatement(fp, why string) {
	m.opaque = append(m.opaque, fp)
	m.report.Opaque = append(m.report.Opaque, m.file+": "+why)
}

func (m *Model) unresolved(what string) {
	m.report.Unresolved = append(m.report.Unresolved, m.file+": "+what)
}

func (m *Model) statement(n *pg.Node, text string) {
	m.count(n)
	fp := fingerprint(n)

	if m.define(n, text, fp) {
		return
	}

	switch s := n.GetNode().(type) {
	case *pg.Node_CreatePolicyStmt:
		m.policy(s.CreatePolicyStmt.GetTable(), fp, s.CreatePolicyStmt.GetQual(), s.CreatePolicyStmt.GetWithCheck())
	case *pg.Node_AlterPolicyStmt:
		m.policy(s.AlterPolicyStmt.GetTable(), fp, s.AlterPolicyStmt.GetQual(), s.AlterPolicyStmt.GetWithCheck())
	case *pg.Node_RuleStmt:
		m.policy(s.RuleStmt.GetRelation(), fp, s.RuleStmt.GetWhereClause())
	case *pg.Node_GrantStmt:
		m.grant(s.GrantStmt, fp)
	case *pg.Node_AlterOwnerStmt, *pg.Node_CommentStmt, *pg.Node_TransactionStmt, *pg.Node_LockStmt,
		*pg.Node_NotifyStmt, *pg.Node_ListenStmt, *pg.Node_VacuumStmt, *pg.Node_CheckPointStmt,
		*pg.Node_DiscardStmt, *pg.Node_ReindexStmt, *pg.Node_ClusterStmt, *pg.Node_CreateRoleStmt,
		*pg.Node_AlterRoleStmt, *pg.Node_GrantRoleStmt, *pg.Node_AlterDefaultPrivilegesStmt,
		*pg.Node_ExplainStmt, *pg.Node_VariableShowStmt:
		// ownership, comments, locks, maintenance and roles change no
		// object's behaviour inside the database's tests
	case *pg.Node_VariableSetStmt:
		m.set(s.VariableSetStmt)
	case *pg.Node_CreateSchemaStmt:
		for _, e := range s.CreateSchemaStmt.GetSchemaElts() {
			m.statement(e, text)
		}
	case *pg.Node_AlterObjectSchemaStmt:
		m.setSchema(s.AlterObjectSchemaStmt, fp)
	case *pg.Node_InsertStmt, *pg.Node_UpdateStmt, *pg.Node_DeleteStmt, *pg.Node_MergeStmt,
		*pg.Node_CopyStmt, *pg.Node_TruncateStmt, *pg.Node_SelectStmt, *pg.Node_CallStmt:
		m.data(n, fp)
	case *pg.Node_DoStmt:
		m.do(s.DoStmt, fp)
	default:
		m.unknown(n, fp)
	}
}

func (m *Model) define(n *pg.Node, text, fp string) bool {
	switch s := n.GetNode().(type) {
	case *pg.Node_CreateStmt:
		m.createTable(s.CreateStmt, fp)
	case *pg.Node_AlterTableStmt:
		m.alterTable(s.AlterTableStmt, fp)
	case *pg.Node_RenameStmt:
		m.rename(s.RenameStmt, fp)
	case *pg.Node_DropStmt:
		m.drop(s.DropStmt, fp)
	case *pg.Node_IndexStmt:
		m.createIndex(s.IndexStmt, fp)
	case *pg.Node_CreateFunctionStmt:
		m.createFunction(s.CreateFunctionStmt, text, fp)
	case *pg.Node_AlterFunctionStmt:
		m.alterFunction(s.AlterFunctionStmt, fp)
	case *pg.Node_CreateTrigStmt:
		m.createTrigger(s.CreateTrigStmt, fp)
	case *pg.Node_ViewStmt:
		m.createView(s.ViewStmt, fp)
	case *pg.Node_CreateTableAsStmt:
		m.createTableAs(s.CreateTableAsStmt, fp)
	case *pg.Node_RefreshMatViewStmt:
		m.writeData(m.relation(s.RefreshMatViewStmt.GetRelation().GetSchemaname(), s.RefreshMatViewStmt.GetRelation().GetRelname()), fp)
	default:
		return m.defineType(n, fp)
	}

	return true
}

func (m *Model) defineType(n *pg.Node, fp string) bool {
	switch s := n.GetNode().(type) {
	case *pg.Node_CreateEnumStmt:
		m.createType(sqlref.Strings(s.CreateEnumStmt.GetTypeName()), fp)
	case *pg.Node_CreateDomainStmt:
		m.createDomain(s.CreateDomainStmt, fp)
	case *pg.Node_CompositeTypeStmt:
		m.createType([]string{s.CompositeTypeStmt.GetTypevar().GetSchemaname(), s.CompositeTypeStmt.GetTypevar().GetRelname()}, fp)
	case *pg.Node_CreateRangeStmt:
		m.createType(sqlref.Strings(s.CreateRangeStmt.GetTypeName()), fp)
	case *pg.Node_AlterEnumStmt:
		m.alterType(sqlref.Strings(s.AlterEnumStmt.GetTypeName()), fp)
	case *pg.Node_AlterDomainStmt:
		m.alterType(sqlref.Strings(s.AlterDomainStmt.GetTypeName()), fp)
	case *pg.Node_CreateSeqStmt:
		m.createSequence(s.CreateSeqStmt, fp)
	case *pg.Node_AlterSeqStmt:
		m.alterSequence(s.AlterSeqStmt, fp)
	case *pg.Node_CreateExtensionStmt:
		m.createExtension(s.CreateExtensionStmt, fp)
	case *pg.Node_AlterExtensionStmt:
		m.touch(m.extension(s.AlterExtensionStmt.GetExtname()), fp)
	default:
		return false
	}

	return true
}

// unknown folds a statement the model doesn't know into the objects it
// names, or into every unit when it names none.
func (m *Model) unknown(n *pg.Node, fp string) {
	named := 0

	for _, r := range sqlref.Of(n) {
		if r.Function {
			continue
		}

		if o := m.relation(r.Schema, r.Name); o != nil {
			m.touch(o, fp)

			named++
		}
	}

	if named == 0 {
		m.opaqueStatement(fp, "a "+nodeKind(n)+" statement")
	}
}

func rangeVarName(r *pg.RangeVar) (string, string) {
	return r.GetSchemaname(), r.GetRelname()
}

func splitName(parts []string) (string, string) {
	switch len(parts) {
	case 0:
		return "", ""
	case 1:
		return "", parts[0]
	default:
		return parts[len(parts)-2], parts[len(parts)-1]
	}
}

func (m *Model) set(s *pg.VariableSetStmt) {
	if s.GetName() != "search_path" {
		return
	}

	var path []string

	for _, a := range s.GetArgs() {
		if c := a.GetAConst(); c != nil {
			path = append(path, c.GetSval().GetSval())
		}
	}

	if s.GetKind() != pg.VariableSetKind_VAR_SET_VALUE || len(path) == 0 {
		path = []string{defaultSchema}
	}

	m.path = path

	if !s.GetIsLocal() {
		m.sessionPath = path
	}
}
