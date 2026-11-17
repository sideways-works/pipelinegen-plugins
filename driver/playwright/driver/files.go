package driver

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"
)

// Playwright's names of a failed test's screenshot and trace attachments.
const (
	screenshotName = "screenshot"
	traceName      = "trace"
)

// kept are the attachments of a failed test that show why, by Playwright's
// names, and what each is to a person.
var kept = map[string]string{
	screenshotName:   "screenshot",
	errorContextName: "page at the failure",
	traceName:        "trace",
}

// keep copies each failed test's screenshots, error context and trace into
// the slice's files directory, a directory per test, and names them. The
// attachments come from the report, never from a glob of Playwright's
// output directory, which a bundle's spec files share.
func keep(dir string, ft *fileTests, log io.Writer) []plugin.File {
	if ft == nil {
		return nil
	}

	var (
		out  []plugin.File
		used = map[string]bool{}
	)

	for i, t := range ft.tests {
		last := lastResult(t)
		if t.Status != "unexpected" || last == nil {
			continue
		}

		sub := testDir(last, title(ft.titles[i], t), used)
		names := map[string]bool{}

		for _, a := range last.Attachments {
			what, ok := kept[a.Name]
			if !ok {
				continue
			}

			name := attachmentName(a, names)
			rel := sub + "/" + name

			if err := a.copyTo(filepath.Join(dir, sub, name)); err != nil {
				sayf(log, "playwright-driver: keeping the %s of %s: %v\n", what, title(ft.titles[i], t), err)

				continue
			}

			out = append(out, plugin.File{Path: rel, Label: what + " of " + title(ft.titles[i], t)})
		}
	}

	return out
}

// testDir is the test's own directory: Playwright's output directory's
// name for it, which is unique in a run, or one made of its title.
func testDir(r *result, title string, used map[string]bool) string {
	name := ""

	for _, a := range r.Attachments {
		if a.Path != "" {
			name = filepath.Base(filepath.Dir(a.Path))

			break
		}
	}

	if name == "" || name == "." || name == string(filepath.Separator) {
		name = strings.Trim(unsafeChars.ReplaceAllString(title, "-"), "-")
	}

	if len(name) > 80 {
		name = name[:80]
	}

	if name == "" {
		name = "test"
	}

	base := name
	for n := 2; used[name]; n++ {
		name = base + "-" + strconv.Itoa(n)
	}

	used[name] = true

	return name
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

var (
	errNoContent = errors.New("the attachment has neither a file nor a body")
	errNotPlain  = errors.New("the attachment's path is no regular file")
)

func attachmentName(a attachment, names map[string]bool) string {
	name := filepath.Base(a.Path)
	if a.Path == "" || name == "." || name == string(filepath.Separator) {
		name = a.Name + map[string]string{errorContextName: ".md", screenshotName: ".png", traceName: ".zip"}[a.Name]
	}

	name = unsafeChars.ReplaceAllString(name, "-")

	base := name
	for n := 2; names[name]; n++ {
		name = strconv.Itoa(n) + "-" + base
	}

	names[name] = true

	return name
}

// copyTo writes the attachment's file, or its body when Playwright gave it
// inline.
func (a attachment) copyTo(dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return fmt.Errorf("files: %w", err)
	}

	if a.Path == "" {
		text, ok := a.text()
		if !ok {
			return errNoContent
		}

		if err := os.WriteFile(dst, []byte(text), 0o600); err != nil {
			return fmt.Errorf("files: %w", err)
		}

		return nil
	}

	// a test may attach any path: a FIFO would block the driver for good
	if fi, err := os.Stat(a.Path); err != nil || !fi.Mode().IsRegular() {
		return errNotPlain
	}

	in, err := os.Open(a.Path)
	if err != nil {
		return fmt.Errorf("files: %w", err)
	}

	defer func() { _ = in.Close() }() //nolint:errcheck // only read

	if fi, err := in.Stat(); err != nil || !fi.Mode().IsRegular() {
		return errNotPlain
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // the slice's own files directory
	if err != nil {
		return fmt.Errorf("files: %w", err)
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close() //nolint:errcheck // the copy's error says it

		return fmt.Errorf("files: %w", err)
	}

	if err := out.Close(); err != nil {
		return fmt.Errorf("files: %w", err)
	}

	return nil
}
