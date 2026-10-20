// Package openapi finds the units of OpenAPI documents, digests them so that
// only a change in behaviour changes a digest, links them to each other and to
// the code generated from them, and maps a test's requests back to operations.
package openapi

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// decode reads a YAML or JSON document. Mapping keys are always strings: a
// response code written as 200 is the key "200".
func decode(data []byte) (any, error) {
	var root yaml.Node

	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	if root.Kind == 0 {
		return nil, errors.New("decode: empty document")
	}

	return fromNode(&root, 0)
}

// maxDepth guards against alias loops, which YAML allows and a document never
// needs.
const maxDepth = 512

func fromNode(n *yaml.Node, depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("decode: nested too deep (an alias loop?)")
	}

	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}

		return fromNode(n.Content[0], depth+1)
	case yaml.AliasNode:
		return fromNode(n.Alias, depth+1)
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))

		for _, c := range n.Content {
			v, err := fromNode(c, depth+1)
			if err != nil {
				return nil, err
			}

			out = append(out, v)
		}

		return out, nil
	case yaml.MappingNode:
		return fromMapping(n, depth)
	case yaml.ScalarNode:
		var v any
		if err := n.Decode(&v); err != nil {
			return nil, fmt.Errorf("decode: line %d: %w", n.Line, err)
		}

		return v, nil
	default:
		return nil, fmt.Errorf("decode: line %d: unknown node kind %d", n.Line, n.Kind)
	}
}

func fromMapping(n *yaml.Node, depth int) (any, error) {
	out := make(map[string]any, len(n.Content)/2)

	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]

		// A merge key copies the keys of the mapping (or mappings) it names
		// unless the mapping sets them itself.
		if k.Tag == "!!merge" {
			if err := merge(out, v, depth); err != nil {
				return nil, err
			}

			continue
		}

		val, err := fromNode(v, depth+1)
		if err != nil {
			return nil, err
		}

		out[k.Value] = val
	}

	return out, nil
}

func merge(out map[string]any, v *yaml.Node, depth int) error {
	src, err := fromNode(v, depth+1)
	if err != nil {
		return err
	}

	var maps []map[string]any

	switch s := src.(type) {
	case map[string]any:
		maps = []map[string]any{s}
	case []any:
		for _, e := range s {
			if m, ok := e.(map[string]any); ok {
				maps = append(maps, m)
			}
		}
	}

	for _, m := range maps {
		for k, val := range m {
			if _, set := out[k]; !set {
				out[k] = val
			}
		}
	}

	return nil
}

// pointer resolves a JSON pointer ("/components/schemas/Pet") in a value.
func pointer(root any, ptr string) (any, bool) {
	if ptr == "" {
		return root, true
	}

	cur := root

	for _, raw := range strings.Split(strings.TrimPrefix(ptr, "/"), "/") {
		tok := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")

		switch c := cur.(type) {
		case map[string]any:
			next, ok := c[tok]
			if !ok {
				return nil, false
			}

			cur = next
		case []any:
			i, ok := index(tok, len(c))
			if !ok {
				return nil, false
			}

			cur = c[i]
		default:
			return nil, false
		}
	}

	return cur, true
}

func index(tok string, n int) (int, bool) {
	if tok == "" || len(tok) > 9 {
		return 0, false
	}

	i := 0

	for _, r := range tok {
		if r < '0' || r > '9' {
			return 0, false
		}

		i = i*10 + int(r-'0')
	}

	return i, i < n
}

func escape(tok string) string {
	return strings.ReplaceAll(strings.ReplaceAll(tok, "~", "~0"), "/", "~1")
}

// asMap, asList and asString read a node of the expected shape, or its zero
// value.
func asMap(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}

	return m
}

func asList(v any) []any {
	l, ok := v.([]any)
	if !ok {
		return nil
	}

	return l
}

func asString(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}

	return s
}
