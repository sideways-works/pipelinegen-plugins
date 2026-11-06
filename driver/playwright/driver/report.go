package driver

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"
)

type report struct {
	Config struct {
		RootDir string `json:"rootDir"` //nolint:tagliatelle // Playwright's field name
	} `json:"config"`
	Suites []suite     `json:"suites"`
	Errors []testError `json:"errors"`
}

type suite struct {
	Title  string  `json:"title"`
	File   string  `json:"file"`
	Specs  []spec  `json:"specs"`
	Suites []suite `json:"suites"`
}

type spec struct {
	Title string `json:"title"`
	File  string `json:"file"`
	Tests []test `json:"tests"`
}

type test struct {
	ProjectName string   `json:"projectName"` //nolint:tagliatelle // Playwright's field name
	Status      string   `json:"status"`
	Results     []result `json:"results"`
}

type result struct {
	Status      string       `json:"status"`
	Duration    float64      `json:"duration"`
	Errors      []testError  `json:"errors"`
	Attachments []attachment `json:"attachments"`
}

// attachment carries its content in Body (base64) or in the file at Path.
type attachment struct {
	Name        string `json:"name"`
	ContentType string `json:"contentType"` //nolint:tagliatelle // Playwright's field name
	Path        string `json:"path"`
	Body        string `json:"body"`
}

type testError struct {
	Message string `json:"message"`
}

const tailLines = 15

func readReport(p string) (*report, error) {
	data, err := os.ReadFile(p) //nolint:gosec // the report the driver's own run wrote
	if err != nil {
		return nil, fmt.Errorf("the JSON report: %w", err)
	}

	var r report
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("the JSON report %s: %w", p, err)
	}

	return &r, nil
}

type fileTests struct {
	titles []string
	tests  []test
}

func (r *report) byFile(tree string) map[string]*fileTests {
	out := map[string]*fileTests{}

	var walk func(s suite, titles []string)

	walk = func(s suite, titles []string) {
		if s.Title != "" && s.Title != s.File {
			titles = append(titles, s.Title)
		}

		for _, sp := range s.Specs {
			name := fromTree(tree, r.Config.RootDir, sp.File)

			ft := out[name]
			if ft == nil {
				ft = &fileTests{}
				out[name] = ft
			}

			for _, t := range sp.Tests {
				ft.titles = append(ft.titles, strings.Join(append(append([]string{}, titles...), sp.Title), " > "))
				ft.tests = append(ft.tests, t)
			}
		}

		for _, child := range s.Suites {
			walk(child, titles)
		}
	}

	for _, s := range r.Suites {
		walk(s, nil)
	}

	return out
}

func fromTree(tree, rootDir, file string) string {
	p := file
	if !filepath.IsAbs(p) {
		p = filepath.Join(rootDir, file)
	}

	if rel, err := filepath.Rel(tree, p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}

	return filepath.ToSlash(file)
}

// end returns false when the report holds no result for the file: it isn't
// there, or a test never finished (its worker died) and none failed. In a
// stopped run Playwright reports unreached tests as skipped, so no file with
// a skip passes there.
func end(slice string, ft *fileTests, stopped bool) (plugin.DriverEvent, bool) {
	ev := plugin.DriverEvent{Slice: slice, Event: plugin.EventEnd, Failures: []plugin.Failure{}}
	if ft == nil {
		return ev, false
	}

	ran, complete := 0, true

	for i, t := range ft.tests {
		last := lastResult(t)

		switch {
		case t.Status == "unexpected":
			ev.Failures = append(ev.Failures, plugin.Failure{Test: title(ft.titles[i], t), Tail: errorTail(last) + pageContext(last)})
		case t.Status == "skipped" && stopped:
			complete = false

			continue
		case t.Status == "skipped":
		case last == nil || last.Status == "interrupted" || last.Status == "":
			complete = false

			continue
		}

		ran++

		for _, r := range t.Results {
			ev.Seconds += r.Duration / 1000
		}
	}

	ev.Tests = &ran

	switch {
	case len(ev.Failures) > 0:
		return ev, true
	case !complete:
		return ev, false
	default:
		ev.Passed = true

		return ev, true
	}
}

func lastResult(t test) *result {
	if len(t.Results) == 0 {
		return nil
	}

	return &t.Results[len(t.Results)-1]
}

func title(t string, tt test) string {
	if tt.ProjectName != "" {
		return "[" + tt.ProjectName + "] " + t
	}

	return t
}

func errorTail(r *result) string {
	if r == nil {
		return ""
	}

	var msgs []string
	for _, e := range r.Errors {
		msgs = append(msgs, e.Message)
	}

	lines := strings.Split(strings.TrimRight(strings.Join(msgs, "\n"), "\n"), "\n")
	if len(lines) > tailLines {
		lines = lines[len(lines)-tailLines:]
	}

	return strings.Join(lines, "\n")
}

func firstLine(msg string) string {
	line, _, _ := strings.Cut(msg, "\n")

	return ansi.ReplaceAllString(line, "")
}

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

const (
	contextLines = 40
	contextBytes = 4000
)

// errorContextName is the attachment Playwright gives a failed test that
// had a page; it holds the page's accessibility snapshot.
const errorContextName = "error-context"

func pageContext(r *result) string {
	if r == nil {
		return ""
	}

	for _, a := range r.Attachments {
		if a.Name != errorContextName {
			continue
		}

		text, ok := a.text()
		if !ok {
			continue
		}

		page := snapshot(text)
		if strings.TrimSpace(page) == "" {
			return ""
		}

		return "\nthe page at the failure:\n" + capped(page, a.Path)
	}

	return ""
}

func (a attachment) text() (string, bool) {
	if a.Body != "" {
		data, err := base64.StdEncoding.DecodeString(a.Body)
		if err != nil {
			return a.Body, true
		}

		return string(data), true
	}

	if a.Path == "" {
		return "", false
	}

	data, err := os.ReadFile(a.Path)
	if err != nil {
		return "", false
	}

	return string(data), true
}

// snapshot is the error context's YAML blocks or, without any, the context
// from "# Test info" on, skipping the instructions before it.
func snapshot(md string) string {
	var (
		out    []string
		inYAML bool
	)

	for line := range strings.SplitSeq(md, "\n") {
		switch {
		case !inYAML && strings.HasPrefix(line, "```yaml"):
			inYAML = true
		case inYAML && strings.HasPrefix(line, "```"):
			inYAML = false
		case inYAML:
			out = append(out, line)
		}
	}

	if len(out) > 0 {
		return strings.Join(out, "\n")
	}

	if _, rest, ok := strings.Cut(md, "\n# Test info"); ok {
		return "# Test info" + rest
	}

	return md
}

func capped(text, path string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")

	var (
		kept []string
		size int
	)

	for _, l := range lines {
		if len(kept) == contextLines || size+len(l)+1 > contextBytes {
			break
		}

		kept = append(kept, l)
		size += len(l) + 1
	}

	out := strings.Join(kept, "\n") + "\n"

	if more := len(lines) - len(kept); more > 0 {
		where := ""
		if path != "" {
			where = " in " + path
		}

		out += fmt.Sprintf("(%d more lines%s)\n", more, where)
	}

	return out
}
