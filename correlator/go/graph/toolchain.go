package graph

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sideways-works/pipelinegen-sbom-go/decls"
)

// GoVersion is the version of the go command the correlator runs in dir, as
// `go env GOVERSION` prints it. It asks in dir because a version manager's
// shim picks the go by the working directory; a go whose GOROOT holds
// another release fails, since its tools would refuse to work.
func GoVersion(ctx context.Context, dir string, b decls.Build) (string, error) {
	where := dir
	if where == "" {
		where = "the working directory"
	}

	cmd := exec.CommandContext(ctx, "go", "env", "GOVERSION", "GOROOT")
	cmd.Dir = dir
	cmd.Env = Env(b)

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}

		return "", fmt.Errorf("%w: go env GOVERSION in %s: %w", ErrToolchain, where, err)
	}

	have, root, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	have, root = strings.TrimSpace(have), strings.TrimSpace(root)

	if have == "" {
		return "", fmt.Errorf("%w: go env GOVERSION in %s printed no version", ErrToolchain, where)
	}

	if holds, ok := rootRelease(root); ok && holds != release(have) {
		return "", fmt.Errorf("%w: the go command in %s is %s, but its GOROOT %s holds %s", ErrToolchain, where, have, root, holds)
	}

	return have, nil
}

// rootRelease is the release a GOROOT holds, from its VERSION file; a
// GOROOT without one (a build from source) says nothing.
func rootRelease(root string) (string, bool) {
	if root == "" {
		return "", false
	}

	b, err := os.ReadFile(filepath.Join(root, "VERSION")) //nolint:gosec // the go command's own GOROOT
	if err != nil {
		return "", false
	}

	first, _, _ := strings.Cut(string(b), "\n")
	first = strings.TrimSpace(first)

	return first, strings.HasPrefix(first, "go")
}

// release is a GOVERSION without what a build adds to the release it
// came from: experiments ("go1.27.4-X:nodwarf5") or a note after a space
// ("go1.27.1 X:boringcrypto").
func release(v string) string {
	v, _, _ = strings.Cut(v, " ")
	v, _, _ = strings.Cut(v, "-X:")

	return v
}
