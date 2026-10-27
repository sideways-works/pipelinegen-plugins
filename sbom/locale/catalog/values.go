package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// ErrNotCatalog is JSON that is no catalog: a value that isn't text.
var ErrNotCatalog = errors.New("not a catalog")

// Parse reads a catalog file: a JSON object whose values are strings,
// objects and arrays of them. It answers ErrNotCatalog, wrapped, for JSON
// of another shape, and a syntax error for a file that is no JSON.
func Parse(b []byte) (map[string]any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()

	var v any
	if err := d.Decode(&v); err != nil {
		return nil, fmt.Errorf("reading the JSON: %w", err)
	}

	if _, err := d.Token(); err == nil {
		return nil, fmt.Errorf("reading the JSON: %w", errTrailing)
	}

	top, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: the file is no JSON object", ErrNotCatalog)
	}

	if err := textOnly(top, nil); err != nil {
		return nil, err
	}

	return top, nil
}

var errTrailing = errors.New("data after the top-level value")

func textOnly(v any, at []string) error {
	switch t := v.(type) {
	case string:
		return nil
	case map[string]any:
		for k, c := range t {
			if err := textOnly(c, append(at, k)); err != nil {
				return err
			}
		}

		return nil
	case []any:
		for i, c := range t {
			if err := textOnly(c, append(at, strconv.Itoa(i))); err != nil {
				return err
			}
		}

		return nil
	default:
		return fmt.Errorf("%w: %s is %T, not text", ErrNotCatalog, strings.Join(at, "."), v)
	}
}

// Node is one key of a namespace: its joined path and the digest of what
// it means. Leaf says it holds a string.
type Node struct {
	Key    string
	Digest string
	Leaf   bool
}

// Nodes are every key of a namespace, the empty key included, sorted by key.
// A key's digest covers its decoded value in canonical form, so formatting
// and key order change nothing, and a flat "a.b" and a nested a.b are one
// node, as i18next finds either.
func Nodes(top map[string]any) []Node {
	w := walker{canon: map[string][][]byte{}, leaf: map[string]bool{}}
	w.walk(top, "")

	out := make([]Node, 0, len(w.canon))

	for k, vs := range w.canon {
		slices.SortFunc(vs, bytes.Compare)

		var b []byte
		if len(vs) == 1 {
			b = vs[0]
		} else {
			b = bytes.Join(append([][]byte{[]byte("merged")}, vs...), []byte("\n"))
		}

		out = append(out, Node{Key: k, Digest: Digest(b), Leaf: w.leaf[k] && len(vs) == 1})
	}

	slices.SortFunc(out, func(a, b Node) int { return strings.Compare(a.Key, b.Key) })

	return out
}

type walker struct {
	canon map[string][][]byte
	leaf  map[string]bool
}

func join(key, part string) string {
	if key == "" {
		return part
	}

	return key + "." + part
}

func (w walker) walk(v any, key string) []byte {
	var b []byte

	switch t := v.(type) {
	case string:
		b = quote(t)
		w.leaf[key] = true
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}

		slices.Sort(keys)

		b = append(b, '{')

		for i, k := range keys {
			if i > 0 {
				b = append(b, ',')
			}

			b = append(b, quote(k)...)
			b = append(b, ':')
			b = append(b, w.walk(t[k], join(key, k))...)
		}

		b = append(b, '}')
	case []any:
		b = append(b, '[')

		for i, c := range t {
			if i > 0 {
				b = append(b, ',')
			}

			b = append(b, w.walk(c, join(key, strconv.Itoa(i)))...)
		}

		b = append(b, ']')
	}

	w.canon[key] = append(w.canon[key], b)

	return b
}

func quote(s string) []byte { return strconv.AppendQuote(nil, s) }
