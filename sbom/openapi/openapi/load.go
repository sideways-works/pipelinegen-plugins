package openapi

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// A ref names a node: a document's slash-separated path from the tree's root
// and a JSON pointer into it ("" for the whole document).
type ref struct {
	File string
	Ptr  string
}

func (r ref) String() string { return r.File + "#" + r.Ptr }

// loader reads each file of a tree once and remembers which it read: an
// answer is cached by every file its build read.
type loader struct {
	tree string
	docs map[string]any
	read []string
}

func newLoader(tree string) *loader {
	return &loader{tree: tree, docs: map[string]any{}}
}

var errNotInTree = errors.New("not in the tree")

func (l *loader) load(file string) (any, error) {
	if v, ok := l.docs[file]; ok {
		return v, nil
	}

	if !filepath.IsLocal(filepath.FromSlash(file)) {
		return nil, fmt.Errorf("%w: %s is outside the tree", errNotInTree, file)
	}

	data, err := os.ReadFile(filepath.Join(l.tree, filepath.FromSlash(file)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s is %w", file, errNotInTree)
	}

	if err != nil {
		return nil, fmt.Errorf("load: %w", err)
	}

	v, err := decode(data)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", file, err)
	}

	l.docs[file] = v
	l.read = append(l.read, file)

	return v, nil
}

func (l *loader) files() []string {
	return slices.Sorted(slices.Values(l.read))
}

// resolve turns a $ref written in file into the node it names. A URL is
// "can't tell": its content isn't part of the commit.
func (l *loader) resolve(file, raw string) (ref, any, error) {
	target, err := parseRef(file, raw)
	if err != nil {
		return ref{}, nil, err
	}

	doc, err := l.load(target.File)
	if err != nil {
		return ref{}, nil, fmt.Errorf("$ref %q in %s: %w", raw, file, missing(err))
	}

	v, ok := pointer(doc, target.Ptr)
	if !ok {
		return ref{}, nil, fmt.Errorf("%w: $ref %q in %s: no such node", ErrInvalid, raw, file)
	}

	return target, v, nil
}

// missing makes a ref to a file the tree doesn't hold "can't tell". A
// document asked about by name that isn't there stays an error.
func missing(err error) error {
	if errors.Is(err, errNotInTree) {
		return fmt.Errorf("%w: %w", ErrCannotTell, err)
	}

	return err
}

func parseRef(file, raw string) (ref, error) {
	if strings.Contains(raw, "://") {
		return ref{}, fmt.Errorf("%w: $ref %q in %s names a remote document", ErrCannotTell, raw, file)
	}

	doc, ptr, _ := strings.Cut(raw, "#")
	target := file

	if doc != "" {
		target = path.Clean(path.Join(path.Dir(file), doc))
	}

	return ref{File: target, Ptr: ptr}, nil
}

// rebase copies a node written in from so that its refs mean the same when
// read as written in to.
func rebase(v any, from, to string) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))

		for k, val := range t {
			out[k] = rebase(val, from, to)
		}

		if raw, ok := t[keyRef].(string); ok {
			// A remote ref fails to parse and is kept: the walk reports it.
			if target, err := parseRef(from, raw); err == nil {
				out[keyRef] = relative(to, target.File) + "#" + target.Ptr
			}
		}

		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = rebase(e, from, to)
		}

		return out
	default:
		return v
	}
}

func relative(base, file string) string {
	rel, err := filepath.Rel(filepath.FromSlash(path.Dir(base)), filepath.FromSlash(file))
	if err != nil {
		// Both are relative to one root, so Rel can't fail; fall back to
		// a path that still resolves from the root's level.
		return file
	}

	return filepath.ToSlash(rel)
}
