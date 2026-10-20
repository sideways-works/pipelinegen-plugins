package openapi

import (
	"net/url"
	"strings"
)

type route struct {
	id     string
	method string
	segs   []string
}

// Router maps requests to operations the way a router picks among
// templates: segment by segment, a literal before a parameter.
type Router struct {
	bases  []string
	routes [][]route
}

// NewRouter builds a router over the model's documents. Requests to a
// document's operations carry bases[file] before the templates, or the
// path of the document's first server's URL when bases names none.
func (m *Model) NewRouter(bases map[string]string) *Router {
	r := &Router{}

	for _, file := range m.files {
		base, ok := bases[file]
		if !ok {
			base = serverPath(m.Servers[file])
		}

		var routes []route

		for _, op := range m.OperationsOf(file) {
			if strings.HasPrefix(op.Path, webhookPrefix) {
				continue
			}

			routes = append(routes, route{id: op.ID, method: op.Method, segs: split(op.Path)})
		}

		r.bases = append(r.bases, strings.TrimSuffix(base, "/"))
		r.routes = append(r.routes, routes)
	}

	return r
}

func serverPath(servers []string) string {
	if len(servers) == 0 {
		return ""
	}

	u, err := url.Parse(servers[0])
	if err != nil {
		return ""
	}

	return u.Path
}

func split(p string) []string {
	return strings.Split(strings.Trim(p, "/"), "/")
}

// Match returns the operation a request reaches, if any.
func (r *Router) Match(method, raw string) (string, bool) {
	p := raw
	if u, err := url.Parse(raw); err == nil {
		p = u.EscapedPath()
	}

	method = strings.ToUpper(method)

	for i, base := range r.bases {
		rest, ok := strings.CutPrefix(p, base)
		if !ok || (rest != "" && !strings.HasPrefix(rest, "/")) {
			continue
		}

		if id, ok := best(r.routes[i], method, split(rest)); ok {
			return id, true
		}
	}

	return "", false
}

// best picks the most specific template: at the first segment where two
// differ, a literal wins over a parameter.
func best(routes []route, method string, segs []string) (string, bool) {
	var found *route

	for i := range routes {
		rt := &routes[i]
		if rt.method != method || !matches(rt.segs, segs) {
			continue
		}

		if found == nil || moreSpecific(rt.segs, found.segs) {
			found = rt
		}
	}

	if found == nil {
		return "", false
	}

	return found.id, true
}

func matches(tmpl, segs []string) bool {
	if len(tmpl) != len(segs) {
		return false
	}

	for i, t := range tmpl {
		if !segmentMatches(t, segs[i]) {
			return false
		}
	}

	return true
}

// segmentMatches matches one segment: a literal, a whole parameter
// ("{id}"), or a parameter with literal text around it ("{id}.json").
func segmentMatches(t, s string) bool {
	open := strings.Index(t, "{")
	if open < 0 {
		return t == s
	}

	end := strings.LastIndex(t, "}")
	if end < open {
		return t == s
	}

	prefix, suffix := t[:open], t[end+1:]

	return len(s) > len(prefix)+len(suffix) && strings.HasPrefix(s, prefix) && strings.HasSuffix(s, suffix)
}

func moreSpecific(a, b []string) bool {
	for i := range a {
		if ra, rb := rank(a[i]), rank(b[i]); ra != rb {
			return ra > rb
		}
	}

	return false
}

// rank orders segments by how much they fix: a literal, then a parameter
// with literal text around it, then a bare parameter.
func rank(seg string) int {
	switch {
	case !strings.Contains(seg, "{"):
		return 2
	case strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}"):
		return 0
	default:
		return 1
	}
}
