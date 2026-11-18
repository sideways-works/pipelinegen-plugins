// Package goid names Go declarations: one stable ID per top-level
// declaration, shared by go-sbom and every plugin that links to them.
//
// An ID is `go:<package directory>#<kind>:<name>`, the directory relative to
// the repository's root with forward slashes ("." for the root):
//
//	function             go:internal/orders#func:Create
//	method               go:internal/orders#method:Order.Total (never with *)
//	generic type method  go:internal/orders#method:List.Push (List[T]: no type parameters)
//	init                 go:internal/orders#init:orders.go:1 (by file and order in it)
//	type                 go:internal/orders#type:Order
//	constant             go:internal/orders#const:Max
//	variable spec        go:internal/orders#var:a,b (its names, in order)
//	blank variable       go:internal/orders#var:_:orders.go:2 (by file and order)
//	in a test file       go:internal/orders#func:helper@test
//	external test        go:internal/orders#func:helper@xtest
//	left-out file        go:internal/orders#file:orders_windows.go
//	type, own part       go:internal/orders#typedecl:Order (without its methods)
//	test type, own part  go:internal/orders#typedecl:env@test
//	method signature     go:internal/orders#methodsig:Order.Total (receiver, name, signature)
//	package API          go:internal/orders#api:orders (by package name)
//
// An ID never names the file of a named declaration, so moving it between
// files keeps its ID.
package goid

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// Namespace is the IDs' prefix, without its colon.
const Namespace = "go"

const prefix = Namespace + ":"

type Kind string

const (
	Func   Kind = "func"
	Method Kind = "method"
	Init   Kind = "init"
	Type   Kind = "type"
	Const  Kind = "const"
	Var    Kind = "var"
	// Excluded is a whole Go file whose declarations can't be read as the
	// build compiles them: one the build constraints leave out, or any file
	// of a package where a file doesn't parse.
	Excluded Kind = "file"
	// TypeDecl is a type with methods as declared, without its method
	// set: code that never hands the type to an interface or to
	// reflection depends on the methods it calls, not on the others.
	TypeDecl Kind = "typedecl"
	// MethodSig is one method's receiver, name and signature, without its
	// body: what an interface or a call that resolves to it reads of it.
	MethodSig Kind = "methodsig"
	// API is what other packages can read of a package: its declarations
	// without function bodies, so a body edit leaves it as it is while
	// anything that can change how an importer type-checks changes it.
	API Kind = "api"
)

const (
	// TestSuffix marks a declaration of a _test.go file in the package
	// itself.
	TestSuffix = "@test"
	// XTestSuffix marks a declaration of an external test package.
	XTestSuffix = "@xtest"
)

type ID string

var ErrID = errors.New("go declaration id")

func New(dir string, kind Kind, name string) ID {
	return ID(prefix + dir + "#" + string(kind) + ":" + name)
}

func Parse(s string) (dir string, kind Kind, name string, err error) {
	rest, ok := strings.CutPrefix(s, prefix)
	if !ok {
		return "", "", "", fmt.Errorf("%w %q: no %s prefix", ErrID, s, prefix)
	}

	dir, rest, ok = strings.Cut(rest, "#")
	if !ok || dir == "" {
		return "", "", "", fmt.Errorf("%w %q: want go:<dir>#<kind>:<name>", ErrID, s)
	}

	k, name, ok := strings.Cut(rest, ":")
	if !ok || name == "" {
		return "", "", "", fmt.Errorf("%w %q: want go:<dir>#<kind>:<name>", ErrID, s)
	}

	switch Kind(k) {
	case Func, Method, Init, Type, Const, Var, Excluded, TypeDecl, MethodSig, API:
	default:
		return "", "", "", fmt.Errorf("%w %q: unknown kind %q", ErrID, s, k)
	}

	return dir, Kind(k), name, nil
}

func (id ID) Dir() string {
	dir, _, _, _ := Parse(string(id)) //nolint:dogsled,errcheck // empty for a malformed ID

	return dir
}

func (id ID) Kind() Kind {
	_, k, _, _ := Parse(string(id)) //nolint:dogsled,errcheck // as above

	return k
}

func (id ID) Name() string {
	_, _, name, _ := Parse(string(id)) //nolint:dogsled,errcheck // as above

	return name
}

func (id ID) Test() bool {
	return strings.HasSuffix(string(id), TestSuffix) || strings.HasSuffix(string(id), XTestSuffix)
}

// MaxLabel is the longest label: one line of `pipelinegen why`.
const MaxLabel = 80

// Label names a declaration as a developer would, with its package's short
// name pkg: notes.Create, notes.Store.Get, package notes init (notes.go:1).
// It is for people only: nothing compares, hashes or caches it.
func Label(id ID, pkg string) string {
	dir, kind, name, err := Parse(string(id))
	if err != nil {
		return Clip(string(id))
	}

	if pkg == "" {
		pkg = path.Base(dir)
	}

	test := ""

	switch {
	case strings.HasSuffix(name, XTestSuffix):
		name, pkg, test = strings.TrimSuffix(name, XTestSuffix), pkg+"_test", " (test)"
	case strings.HasSuffix(name, TestSuffix):
		name, test = strings.TrimSuffix(name, TestSuffix), " (test)"
	}

	var out string

	switch kind {
	case Init:
		// by file and order in it: notes.go:1
		out = "package " + pkg + " init (" + name + ")"
	case Excluded:
		out = "the file " + path.Join(dir, name) + " (left out of the build)"
	case TypeDecl:
		out = pkg + "." + name + " as declared"
	case MethodSig:
		out = pkg + "." + name + "'s signature"
	case API:
		out = "what package " + pkg + " exports"
	case Var, Const:
		if blank, where, ok := strings.Cut(name, ":"); ok && blank == "_" {
			out = "package " + pkg + "'s blank " + string(kind) + " in " + where
		} else {
			out = pkg + "." + name
		}
	default:
		out = pkg + "." + name
	}

	return Clip(out + test)
}

// Clip keeps a label within MaxLabel characters, cutting its middle.
func Clip(s string) string {
	r := []rune(s)
	if len(r) <= MaxLabel {
		return s
	}

	half := (MaxLabel - 1) / 2

	return string(r[:half]) + "…" + string(r[len(r)-(MaxLabel-1-half):])
}
