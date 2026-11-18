package correlate

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/sideways-works/pipelinegen-plugins/sbom/openapi/openapi"
)

// What the lines of the request log hold.
const (
	// LinesURLs: each line is a request's method and URL or path, as the
	// client sent it. A line that matches no operation is ordinary (a page,
	// a health check, a 404).
	LinesURLs = "urls"
	// LinesPatterns: each line is a request's method and the route pattern
	// the server's router matched. A pattern no contract has means the
	// recording and the tree disagree.
	LinesPatterns = "patterns"
)

var errLines = errors.New("config: request_log_lines")

// ErrUnknownRoute marks a logged route pattern that no contract in the
// tree has and other_routes doesn't list: the recording can't be
// attributed, so the slice can't tell.
var ErrUnknownRoute = errors.New("a logged route is in no contract")

func (c Config) checkLines() error {
	switch c.RequestLogLines {
	case "", LinesURLs, LinesPatterns:
		return nil
	default:
		return fmt.Errorf("%w: %q, want %q or %q", errLines, c.RequestLogLines, LinesURLs, LinesPatterns)
	}
}

// wildcard is one parameter of a template or a pattern: {id}, {path...}.
// {$} (the end of a path in Go's ServeMux) is no parameter.
var wildcard = regexp.MustCompile(`\{[^}$]*\}`)

// routeKey is a method and a pattern with its parameter names left out, so
// /notes/{id} and /notes/{noteID} are the same route.
func routeKey(method, pattern string) string {
	p := wildcard.ReplaceAllStringFunc(pattern, func(w string) string {
		if strings.HasSuffix(w, "...}") {
			return "{...}"
		}

		return "{}"
	})

	return strings.ToUpper(method) + " " + p
}

type patternSet struct {
	routes map[string]string
	other  map[string]bool
}

func (m *Model) patterns() patternSet {
	ps := patternSet{routes: map[string]string{}, other: map[string]bool{}}
	bases := m.bases()

	for _, d := range m.Config.Documents {
		base, ok := bases[d.Path]
		if !ok {
			base = serverPath(m.Servers[d.Path])
		}

		base = strings.TrimSuffix(base, "/")

		for _, op := range m.OperationsOf(d.Path) {
			if strings.HasPrefix(op.Path, "webhook:") {
				continue
			}

			ps.routes[routeKey(op.Method, base+op.Path)] = op.ID
		}
	}

	for _, o := range m.Config.OtherRoutes {
		method, pattern, ok := strings.Cut(strings.TrimSpace(o), " ")
		if ok {
			ps.other[routeKey(method, strings.TrimSpace(pattern))] = true
		}
	}

	return ps
}

func (ps patternSet) match(method, pattern, logFile string, r *openapi.Router) (string, error) {
	method = strings.ToUpper(method)

	for _, m := range []string{method, "GET"} {
		key := routeKey(m, pattern)
		if id, ok := ps.routes[key]; ok {
			return id, nil
		}

		if ps.other[key] {
			return "", nil
		}

		// Go's ServeMux serves HEAD with the GET pattern.
		if method != "HEAD" {
			break
		}
	}

	if _, ok := r.Match(method, pattern); ok {
		return "", fmt.Errorf("%w: %s %s in %s is no route of the contracts, but matches one as a URL: the log seems to hold URLs while request_log_lines is %q", ErrUnknownRoute, method, pattern, logFile, LinesPatterns)
	}

	return "", fmt.Errorf("%w: %s %s in %s is no route of the contracts in the tree and not listed under other_routes", ErrUnknownRoute, method, pattern, logFile)
}

// serverPath is the path of a document's first server's URL, the base
// requests carry when the config names none (as the router takes it).
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
