package model

import (
	_ "embed"
	"strconv"
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/sideways-works/pipelinegen-sbom-pg/pgid"
	"github.com/sideways-works/pipelinegen-sbom-pg/sqlref"
)

// maxIdent is the longest identifier PostgreSQL keeps (NAMEDATALEN - 1).
const maxIdent = 63

// chooseName builds name1_name2_label the way PostgreSQL's makeObjectName
// does, shortening the longer of the two names until it fits.
func chooseName(name1, name2, label string) string {
	n1, n2 := name1, name2

	overhead := len(label) + 1
	if n2 != "" {
		overhead++
	}

	for len(n1)+len(n2)+overhead > maxIdent {
		if len(n1) > len(n2) {
			n1 = n1[:len(n1)-1]
		} else {
			n2 = n2[:len(n2)-1]
		}
	}

	parts := []string{n1}
	if n2 != "" {
		parts = append(parts, n2)
	}

	return strings.Join(append(parts, label), "_")
}

// chooseNameNumbered is the name PostgreSQL tries after a collision: the
// label with a number appended.
func chooseNameNumbered(name1, name2, label string, n int) string {
	return chooseName(name1, name2, label+strconv.Itoa(n))
}

// joinCols joins column names for a generated name, as
// ChooseIndexNameAddition does: underscores between, cut at the limit.
func joinCols(cols []string) string {
	s := strings.Join(cols, "_")
	if len(s) > maxIdent {
		s = s[:maxIdent]
	}

	return s
}

func columnRefs(n *pg.Node) []string {
	var out []string

	var walk func(m protoreflect.Message)

	walk = func(m protoreflect.Message) {
		if c, ok := m.Interface().(*pg.ColumnRef); ok {
			if names := sqlref.Strings(c.GetFields()); len(names) > 0 {
				out = append(out, names[len(names)-1])
			}

			return
		}

		sqlref.Children(m, walk)
	}

	if n != nil {
		walk(n.ProtoReflect())
	}

	return out
}

func functionStringArgs(n *pg.Node, fn string) []string {
	var out []string

	var walk func(m protoreflect.Message)

	walk = func(m protoreflect.Message) {
		if c, ok := m.Interface().(*pg.FuncCall); ok {
			if _, name, ok := sqlref.FuncName(c.GetFuncname()); ok && name == fn && len(c.GetArgs()) > 0 {
				if s := stringConst(c.GetArgs()[0]); s != "" {
					out = append(out, s)
				}
			}
		}

		sqlref.Children(m, walk)
	}

	if n != nil {
		walk(n.ProtoReflect())
	}

	return out
}

func stringConst(n *pg.Node) string {
	if tc := n.GetTypeCast(); tc != nil {
		n = tc.GetArg()
	}

	return n.GetAConst().GetSval().GetSval()
}

func nextvalSequences(n *pg.Node) []string {
	return functionStringArgs(n, "nextval")
}

// splitQualified splits "schema.name" as written in a regclass string;
// quoted parts keep their case, unquoted ones fold.
func splitQualified(s string) []string {
	var (
		parts []string
		cur   strings.Builder
		quote bool
	)

	for i := 0; i < len(s); i++ {
		ch := s[i]

		switch {
		case ch == '"' && quote && i+1 < len(s) && s[i+1] == '"':
			cur.WriteByte('"')

			i++
		case ch == '"':
			quote = !quote
		case ch == '.' && !quote:
			parts = append(parts, cur.String())
			cur.Reset()
		case !quote:
			cur.WriteString(strings.ToLower(string(ch)))
		default:
			cur.WriteByte(ch)
		}
	}

	return append(parts, cur.String())
}

// builtinTypes maps PostgreSQL's own type names to how it prints them in
// a function's identity (int4 as integer).
var (
	//go:embed builtin-types.txt
	builtinTypesText string

	builtinTypes = func() map[string]string {
		m := map[string]string{}

		for line := range strings.SplitSeq(builtinTypesText, "\n") {
			if name, printed, ok := strings.Cut(line, " "); ok {
				m[name] = printed
			}
		}

		return m
	}()
)

// provided says which extensions provide a function, type or operator
// class, by kind and name, from extensions.txt.
var (
	//go:embed extensions.txt
	extensionsText string

	provided = func() map[string]map[string][]string {
		m := map[string]map[string][]string{}

		for line := range strings.SplitSeq(extensionsText, "\n") {
			f := strings.Fields(line)
			if len(f) != 3 || strings.HasPrefix(line, "#") {
				continue
			}

			if m[f[1]] == nil {
				m[f[1]] = map[string][]string{}
			}

			m[f[1]][f[2]] = append(m[f[1]][f[2]], f[0])
		}

		return m
	}()
)

func (m *Model) providers(kind, name string) []*object {
	var out []*object

	for _, ext := range provided[kind][name] {
		if o := m.extension(ext); o != nil {
			out = append(out, o)
		}
	}

	return out
}

func (m *Model) typeObject(tn *pg.TypeName) *object {
	if tn == nil {
		return nil
	}

	names := sqlref.Strings(tn.GetNames())
	if len(names) == 0 || names[0] == catalogSchema {
		return nil
	}

	schema, name := splitName(names)

	if o := m.find([]string{pgid.Type, pgid.Table, pgid.View}, schema, name); o != nil {
		return o
	}

	// a type from an extension: when two installed extensions both have
	// one by that name, the first stands for it
	if exts := m.providers("type", name); len(exts) > 0 {
		return exts[0]
	}

	return nil
}

// typeString prints a type as PostgreSQL prints a function's argument
// types with an empty search_path: its own types by their SQL names, every
// other type qualified, arrays with [].
func (m *Model) typeString(tn *pg.TypeName) string {
	if tn == nil {
		return ""
	}

	names := sqlref.Strings(tn.GetNames())
	if len(names) == 0 {
		return ""
	}

	schema, name := splitName(names)

	var s string

	switch {
	case tn.GetPctType():
		s = strings.Join(names, ".") + "%TYPE"
	case schema == catalogSchema || schema == "" && builtinTypes[name] != "" && m.find([]string{pgid.Type}, "", name) == nil:
		s = name
		if printed := builtinTypes[name]; printed != "" {
			s = printed
		}
	default:
		if schema == "" {
			if o := m.find([]string{pgid.Type, pgid.Table, pgid.View}, "", name); o != nil {
				schema = o.schema
			} else {
				schema = m.creationSchema("")
			}
		}

		s = schema + "." + name
	}

	// the identity of an argument prints one pair of brackets, whatever
	// the dimensions
	if len(tn.GetArrayBounds()) > 0 {
		s += "[]"
	}

	return s
}
