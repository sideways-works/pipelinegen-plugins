package openapi

import (
	"maps"
	"slices"
	"strings"
)

// A context is the kind of OpenAPI object a node is. The walk follows the
// specification's structure because a key like "description" is
// documentation in a schema but a property's name inside "properties".
type context string

const (
	ctxGeneric        context = "generic"
	ctxSchema         context = "schema"
	ctxParameter      context = "parameter"
	ctxHeader         context = "header"
	ctxRequestBody    context = "requestBody"
	ctxMediaType      context = "mediaType"
	ctxEncoding       context = "encoding"
	ctxResponse       context = "response"
	ctxLink           context = "link"
	ctxCallback       context = "callback"
	ctxPathItem       context = "pathItem"
	ctxOperation      context = "operation"
	ctxServer         context = "server"
	ctxSecurityScheme context = "securityScheme"
	ctxExample        context = keyExample
)

const (
	keyRef          = "$ref"
	keyDescription  = "description"
	keySummary      = "summary"
	keyExample      = "example"
	keyExamples     = "examples"
	keyParameters   = "parameters"
	keyHeaders      = "headers"
	keyContent      = "content"
	keyComponents   = "components"
	keyExternalDocs = "externalDocs"
)

var sectionContext = map[string]context{
	"schemas":         ctxSchema,
	keyParameters:     ctxParameter,
	keyHeaders:        ctxHeader,
	"requestBodies":   ctxRequestBody,
	"responses":       ctxResponse,
	"links":           ctxLink,
	"callbacks":       ctxCallback,
	"pathItems":       ctxPathItem,
	"securitySchemes": ctxSecurityScheme,
	keyExamples:       ctxExample,
}

// docKeys are the keys that only document an object: they never change what
// a server or a generated client does.
var docKeys = map[context][]string{
	ctxSchema:         {keyDescription, "title", keyExample, keyExamples, keyExternalDocs, "$comment"},
	ctxParameter:      {keyDescription, keyExample, keyExamples},
	ctxHeader:         {keyDescription, keyExample, keyExamples},
	ctxRequestBody:    {keyDescription},
	ctxMediaType:      {keyExample, keyExamples},
	ctxResponse:       {keyDescription},
	ctxLink:           {keyDescription},
	ctxPathItem:       {keySummary, keyDescription},
	ctxOperation:      {keySummary, keyDescription, keyExternalDocs},
	ctxServer:         {keyDescription},
	ctxSecurityScheme: {keyDescription},
	ctxExample:        {keySummary, keyDescription, "any", "externalValue"},
}

var methods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

type edge struct {
	to  ref
	ctx context
	// soft marks a ref in a place the walk doesn't know; one that names nothing
	// is skipped rather than failing the document.
	soft bool
}

// normalizer turns a node into its canonical form and collects its refs as
// edges. It never follows a ref: the digests close over the edges, so a
// shared schema is normalised once.
type normalizer struct {
	file     string
	keepDocs bool
	edges    []edge
	errs     []error
}

func (n *normalizer) node(v any, ctx context) any {
	m, ok := v.(map[string]any)
	if !ok {
		if ctx == ctxGeneric {
			return n.scan(v)
		}

		return v
	}

	if raw, ok := m[keyRef].(string); ok {
		return n.ref(m, raw, ctx)
	}

	drop := map[string]bool{}

	if !n.keepDocs {
		for _, k := range docKeys[ctx] {
			drop[k] = true
		}
	}

	out := make(map[string]any, len(m))

	for k, val := range m {
		if drop[k] {
			continue
		}

		out[k] = n.child(ctx, k, val)
	}

	return out
}

// ref keeps a ref as its target's canonical name, with any siblings (OpenAPI
// 3.1 and JSON Schema 2020-12 allow them).
func (n *normalizer) ref(m map[string]any, raw string, ctx context) any {
	target, err := parseRef(n.file, raw)
	if err != nil {
		n.errs = append(n.errs, err)

		return map[string]any{keyRef: raw}
	}

	n.edges = append(n.edges, edge{to: target, ctx: ctx})

	out := map[string]any{keyRef: target.String()}

	for k, val := range m {
		if k == keyRef {
			continue
		}

		if !n.keepDocs && (k == keySummary || k == keyDescription || isDocKey(ctx, k)) {
			continue
		}

		out[k] = n.child(ctx, k, val)
	}

	return out
}

func isDocKey(ctx context, k string) bool {
	return slices.Contains(docKeys[ctx], k)
}

func (n *normalizer) child(ctx context, k string, v any) any {
	if strings.HasPrefix(k, "x-") {
		// Extensions are kept verbatim: they often decide behaviour, and nothing
		// says which of their keys are prose.
		return v
	}

	switch ctx {
	case ctxSchema:
		return n.schemaChild(k, v)
	case ctxParameter, ctxHeader:
		return n.byKey(k, v, map[string]context{"schema": ctxSchema}, map[string]context{keyContent: ctxMediaType})
	case ctxRequestBody:
		return n.byKey(k, v, nil, map[string]context{keyContent: ctxMediaType})
	case ctxMediaType:
		return n.byKey(k, v, map[string]context{"schema": ctxSchema}, map[string]context{"encoding": ctxEncoding})
	case ctxEncoding:
		return n.byKey(k, v, nil, map[string]context{keyHeaders: ctxHeader})
	case ctxResponse:
		return n.byKey(k, v, nil, map[string]context{keyHeaders: ctxHeader, keyContent: ctxMediaType, "links": ctxLink})
	case ctxLink:
		return n.byKey(k, v, map[string]context{"server": ctxServer}, nil)
	case ctxCallback:
		return n.node(v, ctxPathItem)
	case ctxPathItem:
		return n.pathItemChild(k, v)
	case ctxOperation:
		return n.operationChild(k, v)
	case ctxServer:
		if k == "variables" {
			return n.serverVariables(v)
		}

		return v
	default:
		return n.scan(v)
	}
}

func (n *normalizer) byKey(k string, v any, one, many map[string]context) any {
	if c, ok := one[k]; ok {
		return n.node(v, c)
	}

	if c, ok := many[k]; ok {
		return n.mapOf(v, c)
	}

	return n.scan(v)
}

func (n *normalizer) mapOf(v any, ctx context) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}

	out := make(map[string]any, len(m))

	for k, val := range m {
		if strings.HasPrefix(k, "x-") {
			out[k] = val

			continue
		}

		out[k] = n.node(val, ctx)
	}

	return out
}

func (n *normalizer) listOf(v any, ctx context) any {
	l, ok := v.([]any)
	if !ok {
		return n.node(v, ctx)
	}

	out := make([]any, len(l))
	for i, e := range l {
		out[i] = n.node(e, ctx)
	}

	return out
}

func (n *normalizer) pathItemChild(k string, v any) any {
	switch k {
	case keyParameters:
		return n.listOf(v, ctxParameter)
	case "servers":
		return n.listOf(v, ctxServer)
	}

	if slices.Contains(methods, k) {
		return n.node(v, ctxOperation)
	}

	return n.scan(v)
}

func (n *normalizer) operationChild(k string, v any) any {
	switch k {
	case keyParameters:
		return n.listOf(v, ctxParameter)
	case "requestBody":
		return n.node(v, ctxRequestBody)
	case "responses":
		return n.mapOf(v, ctxResponse)
	case "callbacks":
		return n.mapOf(v, ctxCallback)
	case "servers":
		return n.listOf(v, ctxServer)
	case "security", "tags", "operationId", "deprecated":
		return v
	default:
		return n.scan(v)
	}
}

func (n *normalizer) serverVariables(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}

	out := make(map[string]any, len(m))

	for name, val := range m {
		vm, ok := val.(map[string]any)
		if !ok || n.keepDocs {
			out[name] = val

			continue
		}

		kept := make(map[string]any, len(vm))

		for k, x := range vm {
			if k != keyDescription {
				kept[k] = x
			}
		}

		out[name] = kept
	}

	return out
}

// schemaChild knows JSON Schema's keywords that hold schemas. Every other
// keyword's value is data, so a property named "example" survives.
func (n *normalizer) schemaChild(k string, v any) any {
	switch k {
	case "properties", "patternProperties", "$defs", "definitions", "dependentSchemas":
		return n.mapOf(v, ctxSchema)
	case "items", "additionalProperties", "additionalItems", "not", "if", "then", "else",
		"contains", "propertyNames", "unevaluatedProperties", "unevaluatedItems", "contentSchema":
		// items may be a list in older drafts; additionalProperties may be a
		// boolean, which node keeps as it is.
		return n.listOf(v, ctxSchema)
	case "allOf", "anyOf", "oneOf", "prefixItems":
		return n.listOf(v, ctxSchema)
	case "discriminator":
		return n.discriminator(v)
	case "enum", "const", "default", "type", "format", "pattern", "required":
		return v
	default:
		return n.scan(v)
	}
}

// discriminator turns a mapping's targets, by ref or by bare name, into
// edges.
func (n *normalizer) discriminator(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}

	out := make(map[string]any, len(m))
	maps.Copy(out, m)

	mapping, ok := m["mapping"].(map[string]any)
	if !ok {
		return out
	}

	mapped := make(map[string]any, len(mapping))

	for name, val := range mapping {
		s, ok := val.(string)
		if !ok {
			mapped[name] = val

			continue
		}

		raw := s
		if !strings.Contains(s, "#") && !strings.Contains(s, "/") {
			raw = "#/components/schemas/" + escape(s)
		}

		target, err := parseRef(n.file, raw)
		if err != nil {
			n.errs = append(n.errs, err)
			mapped[name] = s

			continue
		}

		n.edges = append(n.edges, edge{to: target, ctx: ctxSchema})
		mapped[name] = target.String()
	}

	out["mapping"] = mapped

	return out
}

// scan turns every ref inside a node of unknown structure into a soft edge
// and drops nothing: keeping too much only selects more.
func (n *normalizer) scan(v any) any {
	switch t := v.(type) {
	case map[string]any:
		if raw, ok := t[keyRef].(string); ok {
			if target, err := parseRef(n.file, raw); err == nil {
				n.edges = append(n.edges, edge{to: target, ctx: ctxGeneric, soft: true})
			}
		}

		for _, val := range t {
			n.scan(val)
		}
	case []any:
		for _, e := range t {
			n.scan(e)
		}
	}

	return v
}
