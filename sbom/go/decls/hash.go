package decls

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"go/ast"
	"go/token"
	"hash"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// hasher hashes a declaration's syntax tree by structure, never positions or
// comments: a reflowed call hashes the same, anything the compiler reads does
// not.
type hasher struct {
	h hash.Hash
	// names are what an identifier of the file may resolve to that its
	// syntax doesn't show: the import paths behind an import's name, and
	// a marker for a predeclared name the package shadows.
	names map[string][]string
}

func newHasher(names map[string][]string) *hasher {
	return &hasher{h: sha256.New(), names: names}
}

func (d *hasher) str(s string) {
	var n [binary.MaxVarintLen64]byte

	d.h.Write(n[:binary.PutUvarint(n[:], uint64(len(s)))])
	d.h.Write([]byte(s))
}

func (d *hasher) sum() string {
	return hex.EncodeToString(d.h.Sum(nil))
}

var (
	posType     = reflect.TypeFor[token.Pos]()
	commentType = reflect.TypeFor[*ast.CommentGroup]()
	// The parser's old name resolution, which links nodes in cycles.
	skipField = map[string]bool{"Obj": true, "Scope": true, "Unresolved": true}
)

func (d *hasher) node(n ast.Node) {
	d.value(reflect.ValueOf(n))
}

func (d *hasher) value(v reflect.Value) {
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			d.str("nil")

			return
		}

		if id, ok := reflect.TypeAssert[*ast.Ident](v); ok {
			d.ident(id)

			return
		}

		d.value(v.Elem())
	case reflect.Struct:
		d.str(v.Type().Name())

		for i := range v.NumField() {
			f := v.Type().Field(i)
			if f.Type == posType || f.Type == commentType || skipField[f.Name] {
				continue
			}

			d.value(v.Field(i))
		}
	case reflect.Slice:
		d.str("[" + strconv.Itoa(v.Len()))

		for i := range v.Len() {
			d.value(v.Index(i))
		}
	case reflect.String:
		d.str(v.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		d.str(strconv.FormatInt(v.Int(), 10))
	case reflect.Bool:
		d.str(strconv.FormatBool(v.Bool()))
	default:
		d.str(v.Kind().String())
	}
}

func (d *hasher) ident(id *ast.Ident) {
	d.str("I" + id.Name)

	for _, p := range d.names[id.Name] {
		d.str("R" + p)
	}
}

// directives hashes the `//go:` lines of comment groups: they change what
// the compiler does, unlike every other comment.
func (d *hasher) directives(groups ...*ast.CommentGroup) {
	for _, g := range groups {
		if g == nil {
			continue
		}

		for _, c := range g.List {
			if strings.HasPrefix(c.Text, "//go:") {
				d.str("D" + c.Text)
			}
		}
	}
}

// importNames maps the names a file's imports bind to their paths. An import
// without an alias is guessed by its path's last element, which only
// over-selects when wrong; unguessable ones sit under "" and dot imports under
// ".".
func importNames(f *ast.File) map[string][]string {
	names := map[string][]string{}

	for _, im := range f.Imports {
		p, err := strconv.Unquote(im.Path.Value)
		if err != nil {
			continue
		}

		switch {
		case im.Name != nil && im.Name.Name == "_":
		case im.Name != nil:
			names[im.Name.Name] = append(names[im.Name.Name], p)
		default:
			name, ok := guessName(p)
			if !ok {
				name = ""
			}

			names[name] = append(names[name], p)
		}
	}

	for k := range names {
		slices.Sort(names[k])
	}

	return names
}

// guessName is the package name an import path most likely binds: its last
// element, without a major version, a gopkg.in version or a go- prefix.
func guessName(p string) (string, bool) {
	parts := strings.Split(p, "/")
	last := parts[len(parts)-1]

	if len(parts) > 1 && len(last) > 1 && last[0] == 'v' && isDigits(last[1:]) {
		last = parts[len(parts)-2]
	}

	if i := strings.Index(last, ".v"); i > 0 && isDigits(last[i+2:]) {
		last = last[:i]
	}

	last = strings.TrimPrefix(last, "go-")
	last = strings.TrimSuffix(strings.TrimSuffix(last, "-go"), ".go")

	return last, token.IsIdentifier(last)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}

	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}

	return true
}

var predeclared = map[string]bool{}

func init() { //nolint:gochecknoinits // the table is built once from the list
	for n := range strings.FieldsSeq(`any append bool byte cap clear close comparable complex complex64
		complex128 copy delete error false float32 float64 imag int int8 int16 int32 int64 iota len make
		max min new nil panic print println real recover rune string true uint uint8 uint16 uint32 uint64
		uintptr`) {
		predeclared[n] = true
	}
}
