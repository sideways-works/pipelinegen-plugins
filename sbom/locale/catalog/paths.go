package catalog

import (
	"path"
	"regexp"
	"slices"
	"strings"
)

var secretNames = []string{
	".npmrc", ".netrc", ".pgpass", ".git-credentials", ".pypirc", ".dockercfg",
	"credentials", "credentials.json", "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519",
	"authorized_keys", "known_hosts",
}

var secretDirs = []string{"secret", "secrets", ".secret", ".secrets", "credentials", ".ssh", ".gnupg", ".aws", ".docker", ".kube", ".config"}

var secretPattern = regexp.MustCompile(`(?i)^(\.env(\..*)?|.*\.(pem|key|p12|pfx|jks|keystore|kdbx|gpg|asc|age|crt|cer|der|p8|ovpn|ppk)|id_(rsa|dsa|ecdsa|ed25519)(\.pub)?|.*secret.*|.*token.*|.*password.*|.*passwd.*)$`)

// Secret tells whether a path looks like it holds a secret. Such a path is
// never a catalog and never in a recording.
func Secret(p string) bool {
	parts := strings.Split(p, "/")
	for _, d := range parts[:len(parts)-1] {
		if slices.Contains(secretDirs, strings.ToLower(d)) {
			return true
		}
	}

	name := parts[len(parts)-1]

	return slices.Contains(secretNames, strings.ToLower(name)) || secretPattern.MatchString(name)
}

// InTree tells whether p is a clean path inside a tree: relative, without
// a part that climbs out, and not the root itself.
func InTree(p string) bool {
	if p == "" || p == "." || path.IsAbs(p) || strings.ContainsAny(p, "\x00\n\\") {
		return false
	}

	if path.Clean(p) != p {
		return false
	}

	return p != ".." && !strings.HasPrefix(p, "../")
}

func skipped(p string) bool {
	for d := range strings.SplitSeq(path.Dir(p), "/") {
		if d == ".git" || d == "node_modules" || strings.HasPrefix(d, "bazel-") || d == "dist" {
			return true
		}
	}

	return false
}

// Why says why a tree path may never be a catalog file, or "" when it may.
func Why(p string) string {
	switch {
	case !InTree(p):
		return "outside the tree"
	case skipped(p):
		return "not the tree's own"
	case Secret(p):
		return "looks like a secret"
	}

	return ""
}

// Candidates are the paths a catalog may have; a plugin entry's include adds
// others.
var Candidates = []string{
	"**/locales/**/*.json", "**/locale/**/*.json", "**/i18n/**/*.json",
	"**/lang/**/*.json", "**/langs/**/*.json", "**/translations/**/*.json",
}
