package sqlref

import (
	_ "embed"
	"strings"
)

// builtinNames are the functions PostgreSQL defines in pg_catalog (see
// genbuiltins).
//
//go:embed builtins.txt
var builtinNames string

var builtins = func() map[string]bool {
	m := map[string]bool{}

	for line := range strings.SplitSeq(builtinNames, "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			m[line] = true
		}
	}

	return m
}()

// Builtin tells whether an unqualified function name is one of
// PostgreSQL's own. pg_catalog comes first on every search path that
// doesn't name it, so a call by that name reaches the built-in.
func Builtin(name string) bool {
	return builtins[name]
}

// System tells whether a relation is PostgreSQL's own: anything in
// pg_catalog or information_schema, and an unqualified name starting with
// pg_ (every relation of pg_catalog does).
func System(schema, name string) bool {
	switch schema {
	case "pg_catalog", "information_schema":
		return true
	case "":
		return strings.HasPrefix(name, "pg_")
	default:
		return false
	}
}
