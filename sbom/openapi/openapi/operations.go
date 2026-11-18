package openapi

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// webhookPrefix marks a webhook's operation, which no router of the
// document's servers serves.
const webhookPrefix = "webhook:"

func (b *builder) operations(d Document, doc map[string]any) ([]Operation, error) {
	paths := asMap(doc["paths"])
	templates := sortedKeys(paths)

	var ops []Operation

	for _, tmpl := range templates {
		item, from, err := b.pathItem(paths[tmpl])
		if err != nil {
			return nil, fmt.Errorf("%s: path %s: %w", d.Path, tmpl, err)
		}

		overlaps := overlapping(tmpl, templates)

		for _, method := range methods {
			raw, ok := item[method].(map[string]any)
			if !ok {
				continue
			}

			op := operation(d, raw, method, tmpl)
			op.From = from
			b.addOperation(op, from, doc, item, raw, overlaps)
			ops = append(ops, op)
		}
	}

	hooks := asMap(doc["webhooks"])

	for _, name := range sortedKeys(hooks) {
		item, from, err := b.pathItem(hooks[name])
		if err != nil {
			return nil, fmt.Errorf("%s: webhook %s: %w", d.Path, name, err)
		}

		for _, method := range methods {
			raw, ok := item[method].(map[string]any)
			if !ok {
				continue
			}

			op := operation(d, raw, method, webhookPrefix+name)
			op.From = from
			b.addOperation(op, from, doc, item, raw, nil)
			ops = append(ops, op)
		}
	}

	return ops, nil
}

// pathItem follows a path item's $refs and returns the item with the file it
// was written in. Keys next to a ref (3.1) win over the target's; a cycle is
// "can't tell".
func (b *builder) pathItem(v any) (map[string]any, string, error) {
	item, ok := v.(map[string]any)
	if !ok {
		return nil, "", fmt.Errorf("%w: not a path item", ErrInvalid)
	}

	file := b.file
	seen := map[string]bool{}

	var chain []string

	for {
		raw, ok := item[keyRef].(string)
		if !ok {
			return item, file, nil
		}

		target, resolved, err := b.l.resolve(file, raw)
		if err != nil {
			return nil, "", err
		}

		chain = append(chain, target.String())
		if seen[target.String()] {
			return nil, "", fmt.Errorf("%w: path item refs form a cycle: %s", ErrCannotTell, strings.Join(chain, " -> "))
		}

		seen[target.String()] = true

		m, ok := resolved.(map[string]any)
		if !ok {
			return nil, "", fmt.Errorf("%w: $ref %s: not a path item", ErrInvalid, target)
		}

		merged := make(map[string]any, len(m)+len(item))
		maps.Copy(merged, m)

		for k, val := range item {
			if k != keyRef {
				merged[k] = rebase(val, file, target.File)
			}
		}

		item, file = merged, target.File
	}
}

func operation(d Document, raw map[string]any, method, tmpl string) Operation {
	opID := asString(raw["operationId"])
	if opID == "" {
		opID = defaultOperationID(method, tmpl)
	}

	return Operation{
		ID:          Namespace + ":" + d.Prefix + opID,
		OperationID: opID,
		Method:      strings.ToUpper(method),
		Path:        tmpl,
		File:        d.Path,
		GoName:      GoName(opID),
	}
}

// addOperation builds an operation's canonical form, including the other
// templates a request for it could be routed to. Its refs are read against
// from, the file the path item was written in.
func (b *builder) addOperation(op Operation, from string, doc, item, raw map[string]any, overlaps []string) {
	n := b.normalizer()
	n.file = from

	form := asMap(n.node(raw, ctxOperation))
	if form == nil {
		form = map[string]any{}
	}

	form["method"] = op.Method
	form["path"] = op.Path
	form[keyParameters] = b.parameters(n, from, item[keyParameters], raw[keyParameters])

	sec, own := raw["security"]
	if !own {
		sec = doc["security"]
	}

	form["security"] = sec

	if _, own := raw["servers"]; !own {
		if s, ok := item["servers"]; ok {
			form["servers"] = n.listOf(s, ctxServer)
		}
	}

	for k, v := range item {
		if strings.HasPrefix(k, "x-") {
			form["pathItem."+k] = v
		}
	}

	if len(overlaps) > 0 {
		form["overlaps"] = overlaps
	}

	edges, external := b.split(n)
	for _, e := range b.securityEdges(sec) {
		if b.exists(refOf(e).Ptr) {
			edges = append(edges, e)
		}
	}

	slices.Sort(edges)

	b.add(&node{
		unit:     b.unit(op.ID, KindOperation, true),
		own:      digestOf(form),
		edges:    slices.Compact(edges),
		external: external,
		text:     digestOf(map[string]any{"operation": raw, "pathItem": shared(item)}),
	})
}

func refOf(id string) ref {
	file, ptr, _ := strings.Cut(strings.TrimPrefix(id, Namespace+":"), "#")

	return ref{File: file, Ptr: ptr}
}

func (b *builder) securityEdges(sec any) []string {
	list := asList(sec)

	var out []string

	for _, req := range list {
		m := asMap(req)
		for name := range m {
			out = append(out, unitID(b.file, "/components/securitySchemes/"+escape(name)))
		}
	}

	return out
}

// parameters merges the path item's parameters with the operation's, whose
// own replace those of the same name and location.
func (b *builder) parameters(n *normalizer, from string, itemParams, opParams any) []any {
	type keyed struct {
		key string
		v   any
	}

	var list []keyed

	add := func(params any) {
		l := asList(params)
		for _, p := range l {
			k := b.paramKey(from, p)

			i := slices.IndexFunc(list, func(e keyed) bool { return e.key == k })
			if i >= 0 {
				list[i].v = p

				continue
			}

			list = append(list, keyed{key: k, v: p})
		}
	}

	add(itemParams)
	add(opParams)

	out := make([]any, 0, len(list))
	for _, e := range list {
		out = append(out, n.node(e.v, ctxParameter))
	}

	return out
}

func (b *builder) paramKey(from string, p any) string {
	m := asMap(p)
	if raw, ok := m[keyRef].(string); ok {
		// A parameter that can't be read keeps its own key, so it is
		// never merged away and stays in the form.
		_, v, err := b.l.resolve(from, raw)
		if err != nil {
			return "$ref " + raw
		}

		m = asMap(v)
	}

	name := asString(m["name"])
	in := asString(m["in"])

	return in + " " + name
}

// defaultOperationID is the name oapi-codegen derives for an operation
// without an operationId: the method and the path's segments.
func defaultOperationID(method, tmpl string) string {
	var b strings.Builder

	b.WriteString(strings.ToLower(method))

	for part := range strings.SplitSeq(tmpl, "/") {
		if part != "" {
			b.WriteString("-" + part)
		}
	}

	return b.String()
}

// overlapping lists the document's other templates that can match a URL this
// one matches: adding or removing one changes which operation serves it.
func overlapping(tmpl string, all []string) []string {
	segs := strings.Split(tmpl, "/")

	var out []string

	for _, other := range all {
		if other == tmpl {
			continue
		}

		o := strings.Split(other, "/")
		if len(o) != len(segs) {
			continue
		}

		match := true

		for i := range segs {
			if segs[i] != o[i] && !strings.Contains(segs[i], "{") && !strings.Contains(o[i], "{") {
				match = false

				break
			}
		}

		if match {
			out = append(out, other)
		}
	}

	return out
}

func shared(item map[string]any) map[string]any {
	out := make(map[string]any, len(item))

	for k, v := range item {
		if !slices.Contains(methods, k) {
			out[k] = v
		}
	}

	return out
}
