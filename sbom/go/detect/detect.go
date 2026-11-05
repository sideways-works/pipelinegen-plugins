// Package detect claims the files of a tree the Go plugins read from path and
// content alone, so the answer for a file can be cached by its content.
package detect

import (
	"bytes"
	"path"
	"regexp"
	"strings"
)

// The roles of the files the Go plugins read.
const (
	RoleSource    = "source"
	RoleGenerated = "generated"
	RoleManifest  = "manifest"
	RoleLockfile  = "lockfile"
	RoleDirective = "directive"
)

// vendorDir is where go mod vendor writes.
const vendorDir = "vendor"

// Candidates are the path patterns of the files worth asking about.
var Candidates = []string{
	"**/*.go", "**/go.mod", "**/go.sum", "**/go.work", "**/go.work.sum",
	"**/vendor/modules.txt", "MODULE.bazel",
}

// Claim is one claimed file.
type Claim struct {
	Path string `json:"path"`
	Role string `json:"role"`
}

// generated is the header the Go project defines for generated files
// (https://go.dev/s/generatedcode).
var generated = regexp.MustCompile(`(?m)^// Code generated .*DO NOT EDIT\.$`)

var sdkDownload = regexp.MustCompile(`go_sdk\.download\(`)

// File claims one file by its path and content, or says nothing.
func File(p string, content []byte) (Claim, bool) {
	if skipped(p) {
		return Claim{}, false
	}

	switch base := path.Base(p); {
	case strings.HasSuffix(base, ".go"):
		return goFile(p, content)
	case base == "go.mod" || base == "go.work":
		return Claim{Path: p, Role: RoleManifest}, true
	case base == "go.sum" || base == "go.work.sum":
		return Claim{Path: p, Role: RoleLockfile}, true
	case base == "modules.txt" && path.Base(path.Dir(p)) == vendorDir:
		return Claim{Path: p, Role: RoleLockfile}, true
	case p == "MODULE.bazel" && sdkDownload.Match(content):
		return Claim{Path: p, Role: RoleDirective}, true
	}

	return Claim{}, false
}

func goFile(p string, content []byte) (Claim, bool) {
	head, ok := beforePackage(content)
	if !ok {
		return Claim{}, false
	}

	c := Claim{Path: p, Role: RoleSource}

	if generated.Match(head) {
		c.Role = RoleGenerated
	}

	return c, true
}

func beforePackage(content []byte) ([]byte, bool) {
	for i := 0; i < len(content); {
		end := bytes.IndexByte(content[i:], '\n')
		if end < 0 {
			end = len(content) - i
		}

		line := bytes.TrimSpace(content[i : i+end])
		if bytes.HasPrefix(line, []byte("package ")) {
			return content[:i], true
		}

		i += end + 1
	}

	return nil, false
}

// skipped tells whether a path sits where the go command never looks:
// testdata, vendor (except its modules.txt), and directories starting with
// . or _.
func skipped(p string) bool {
	parts := strings.Split(path.Dir(p), "/")
	for i, part := range parts {
		if part == vendorDir && i == len(parts)-1 && path.Base(p) == "modules.txt" {
			continue
		}

		if part == "testdata" || part == vendorDir || part == "node_modules" ||
			part != "." && (strings.HasPrefix(part, ".") || strings.HasPrefix(part, "_")) || strings.HasPrefix(part, "bazel-") {
			return true
		}
	}

	return false
}
