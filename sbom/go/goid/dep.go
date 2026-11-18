package goid

import "strings"

// DepNamespace is the namespace of the modules a repository depends on and
// of the toolchain it builds with, without its colon.
const DepNamespace = "dep:go"

const depPrefix = DepNamespace + ":"

// The toolchain's units. None of them is a module path: a module path's
// first element holds a dot.
const (
	// GoVersion is the go directive of the repository's go.mod files.
	GoVersion = depPrefix + "go"
	// Toolchain is their toolchain directive.
	Toolchain = depPrefix + "toolchain"
	// BazelSDK is the Go SDK Bazel builds with, from MODULE.bazel.
	BazelSDK = depPrefix + "bazel_sdk"
)

// Dep is the unit of a module, by its path exactly as go.mod writes it,
// major version suffix included: dep:go:github.com/jackc/pgx/v5.
func Dep(modulePath string) string {
	return depPrefix + modulePath
}

// DepPath is the module path of a dep:go unit, and false for anything
// else, the toolchain's units included.
func DepPath(id string) (string, bool) {
	p, ok := strings.CutPrefix(id, depPrefix)
	if !ok || id == GoVersion || id == Toolchain || id == BazelSDK || p == "" {
		return "", false
	}

	return p, true
}
