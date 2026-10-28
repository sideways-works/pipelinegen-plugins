package model

import (
	"encoding/json"
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"
	pgquery "github.com/wasilibs/go-pgquery"

	"github.com/sideways-works/pipelinegen-sbom-pg/pgid"
	"github.com/sideways-works/pipelinegen-sbom-pg/sqlref"
)

func (m *Model) createFunction(s *pg.CreateFunctionStmt, text, fp string) {
	schema, name := splitName(sqlref.Strings(s.GetFuncname()))
	args := m.argTypes(s.GetParameters())

	var f *object

	if have := m.functions(schema, name, args); len(have) > 0 {
		f = have[0]
		m.touch(f, fp)
	} else {
		f = m.add(pgid.Function, m.creationSchema(schema), name, fp)
		f.args = args
	}

	f.refs = nil
	f.dynamic = false

	for _, p := range s.GetParameters() {
		f.use(m.typeObject(p.GetFunctionParameter().GetArgType()))
	}

	f.use(m.typeObject(s.GetReturnType()))

	language, body := functionBody(s)

	switch {
	case s.GetSqlBody() != nil:
		m.bodyRefs(f, sqlref.Of(s.GetSqlBody()))
	case language == "sql":
		if refs, err := sqlref.Refs(body); err == nil {
			m.bodyRefs(f, refs)
		} else {
			f.dynamic = true
		}
	case language == "plpgsql":
		m.plpgsql(f, text)
	default:
		// C or another language: what it touches can't be read
		f.dynamic = true
	}
}

func (m *Model) bodyRefs(f *object, refs []sqlref.Ref) {
	for _, r := range refs {
		kind := Reads
		if r.Function {
			kind = Uses
		} else if r.Write {
			kind = Writes
		}

		f.refs = append(f.refs, pendingRef{ref: r, path: m.path, kind: kind})
	}
}

func (m *Model) plpgsql(f *object, text string) {
	queries, dynamic, err := plpgsqlQueries(text)
	if err != nil {
		f.dynamic = true

		return
	}

	f.dynamic = dynamic

	for _, q := range queries {
		refs, err := sqlref.Refs(q)
		if err != nil {
			continue
		}

		m.bodyRefs(f, refs)
	}
}

func plpgsqlQueries(createFunction string) ([]string, bool, error) {
	out, err := pgquery.ParsePlPgSqlToJSON(createFunction)
	if err != nil {
		return nil, false, err //nolint:wrapcheck // the caller only needs to know it failed
	}

	var tree any
	if err := json.Unmarshal([]byte(out), &tree); err != nil {
		return nil, false, err //nolint:wrapcheck // as above
	}

	var (
		queries []string
		dynamic bool
	)

	var walk func(v any)

	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				switch k {
				case "PLpgSQL_stmt_dynexecute", "PLpgSQL_stmt_dynfors":
					dynamic = true
				case "PLpgSQL_stmt_open":
					if o, ok := val.(map[string]any); ok && o["dynquery"] != nil {
						dynamic = true
					}
				case "query":
					if q, ok := val.(string); ok {
						queries = append(queries, q)
					}
				}

				walk(val)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}

	walk(tree)

	return queries, dynamic, nil
}

func functionBody(s *pg.CreateFunctionStmt) (string, string) {
	var language, body string

	for _, o := range s.GetOptions() {
		d := o.GetDefElem()
		switch d.GetDefname() {
		case "language":
			language = d.GetArg().GetString_().GetSval()
		case "as":
			parts := sqlref.Strings(d.GetArg().GetList().GetItems())
			if len(parts) > 0 {
				body = parts[0]
			}
		}
	}

	return strings.ToLower(language), body
}

// argTypes are a function's identity: the types of its input arguments.
func (m *Model) argTypes(params []*pg.Node) []string {
	args := []string{}

	for _, p := range params {
		fp := p.GetFunctionParameter()
		switch fp.GetMode() {
		case pg.FunctionParameterMode_FUNC_PARAM_OUT, pg.FunctionParameterMode_FUNC_PARAM_TABLE:
			continue
		default:
			args = append(args, m.typeString(fp.GetArgType()))
		}
	}

	return args
}

func (m *Model) functionsOf(o *pg.ObjectWithArgs) []*object {
	schema, name := splitName(sqlref.Strings(o.GetObjname()))

	if o.GetArgsUnspecified() {
		return m.functions(schema, name, nil)
	}

	args := make([]string, 0, len(o.GetObjargs()))
	for _, a := range o.GetObjargs() {
		args = append(args, m.typeString(a.GetTypeName()))
	}

	if found := m.functions(schema, name, args); len(found) > 0 {
		return found
	}

	// argument types written another way than the model prints them:
	// every overload of the name, so nothing is missed
	return m.functions(schema, name, nil)
}

func (m *Model) alterFunction(s *pg.AlterFunctionStmt, fp string) {
	for _, f := range m.functionsOf(s.GetFunc()) {
		m.touch(f, fp)
	}
}

func (m *Model) createTrigger(s *pg.CreateTrigStmt, fp string) {
	t := m.relation(rangeVarName(s.GetRelation()))
	if t == nil {
		m.unresolved("trigger on " + s.GetRelation().GetRelname())

		return
	}

	if t.triggers == nil {
		t.triggers = map[string][]*object{}
	}

	m.touch(t, fp)

	schema, name := splitName(sqlref.Strings(s.GetFuncname()))
	fns := m.functions(schema, name, []string{})

	if len(fns) == 0 {
		fns = m.functions(schema, name, nil)
	}

	if len(fns) == 0 {
		m.unresolved("trigger function " + name)
	}

	t.triggers[s.GetTrigname()] = fns

	for _, f := range fns {
		t.use(f)
	}

	if s.GetWhenClause() != nil {
		m.expression(t, s.GetWhenClause())
	}
}

func (m *Model) createType(names []string, fp string) {
	schema, name := splitName(names)
	if have := m.find([]string{pgid.Type}, schema, name); have != nil {
		m.touch(have, fp)

		return
	}

	m.add(pgid.Type, m.creationSchema(schema), name, fp)
}

func (m *Model) createDomain(s *pg.CreateDomainStmt, fp string) {
	m.createType(sqlref.Strings(s.GetDomainname()), fp)

	schema, name := splitName(sqlref.Strings(s.GetDomainname()))
	if d := m.find([]string{pgid.Type}, schema, name); d != nil {
		d.use(m.typeObject(s.GetTypeName()))
	}
}

func (m *Model) alterType(names []string, fp string) {
	schema, name := splitName(names)

	t := m.find([]string{pgid.Type}, schema, name)
	if t == nil {
		m.unresolved("type " + name)

		return
	}

	m.touch(t, fp)
}

func (m *Model) createSequence(s *pg.CreateSeqStmt, fp string) {
	schema, name := rangeVarName(s.GetSequence())
	if have := m.relation(schema, name); have != nil {
		m.touch(have, fp)

		return
	}

	seq := m.add(pgid.Sequence, m.creationSchema(schema), name, fp)
	m.ownedBy(seq, s.GetOptions())
}

func (m *Model) alterSequence(s *pg.AlterSeqStmt, fp string) {
	seq := m.find([]string{pgid.Sequence}, s.GetSequence().GetSchemaname(), s.GetSequence().GetRelname())
	if seq == nil {
		if !s.GetMissingOk() {
			m.unresolved("sequence " + s.GetSequence().GetRelname())
		}

		return
	}

	m.touch(seq, fp)
	m.ownedBy(seq, s.GetOptions())
}

func (m *Model) ownedBy(seq *object, options []*pg.Node) {
	for _, o := range options {
		d := o.GetDefElem()
		if d.GetDefname() != "owned_by" {
			continue
		}

		parts := sqlref.Strings(d.GetArg().GetList().GetItems())
		if len(parts) < 2 {
			continue
		}

		schema, table := splitName(parts[:len(parts)-1])
		if t := m.relation(schema, table); t != nil {
			t.use(seq)
			t.owned = append(t.owned, seq)
		}
	}
}

func (m *Model) createExtension(s *pg.CreateExtensionStmt, fp string) {
	name, ifNotExists := s.GetExtname(), s.GetIfNotExists()

	if have := m.extension(name); have != nil {
		if !ifNotExists {
			m.unresolved("extension " + name + " created twice")
		}

		m.touch(have, fp)

		return
	}

	schema := ""

	for _, opt := range s.GetOptions() {
		if d := opt.GetDefElem(); d.GetDefname() == "schema" {
			schema = d.GetArg().GetString_().GetSval()
		}
	}

	// extensions belong to no schema themselves; the empty one keeps find
	// simple
	o := &object{kind: pgid.Extension, name: name, created: m.file, extSchema: m.creationSchema(schema)}
	m.objects = append(m.objects, o)
	m.touch(o, fp)
}

// do applies a DO block: one that only reads changes nothing, one that runs
// fixed statements applies them as if they stood alone, and one that builds
// SQL at run time can't be followed.
func (m *Model) do(s *pg.DoStmt, fp string) {
	var body, language string

	for _, a := range s.GetArgs() {
		d := a.GetDefElem()
		switch d.GetDefname() {
		case "as":
			body = d.GetArg().GetString_().GetSval()
		case "language":
			language = d.GetArg().GetString_().GetSval()
		}
	}

	if language != "" && strings.ToLower(language) != "plpgsql" {
		m.opaqueStatement(fp, "a DO block in "+language)

		return
	}

	tag := "$pgsbom$"
	for strings.Contains(body, tag) {
		tag = "$pgsbom" + tag[1:]
	}

	queries, dynamic, err := plpgsqlQueries("CREATE FUNCTION pgsbom_do() RETURNS void LANGUAGE plpgsql AS " + tag + body + tag)
	if err != nil || dynamic {
		m.opaqueStatement(fp, "a DO block that builds SQL at run time or doesn't parse")

		return
	}

	for _, q := range queries {
		tree, err := sqlref.Parse(q)
		if err != nil {
			m.opaqueStatement(fp, "a DO block statement that doesn't parse")

			continue
		}

		for _, raw := range tree.GetStmts() {
			inner := raw.GetStmt()
			if _, isSelect := inner.GetNode().(*pg.Node_SelectStmt); isSelect && !callsRepositoryFunction(m, inner) {
				// a condition or a check: it reads, and changes nothing
				continue
			}

			m.statement(inner, q)
		}
	}
}

func callsRepositoryFunction(m *Model, n *pg.Node) bool {
	for _, r := range sqlref.Of(n) {
		if r.Function && len(m.functions(r.Schema, r.Name, nil)) > 0 {
			return true
		}
	}

	return false
}
