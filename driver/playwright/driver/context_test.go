package driver

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

// errorContext is an attachment as Playwright 1.63 writes it.
const errorContext = "# Instructions\n\n- Following Playwright test failed.\n\n# Test info\n\n- Name: x.spec.mjs >> finds\n\n" +
	"# Error details\n\n```\nError: expect(locator).toBeVisible() failed\n```\n\n" +
	"```yaml\n- main:\n  - heading \"Notes\" [level=1]\n  - button \"Add a note\"\n```\n\n# Test source\n\n```ts\n  1 | import { test } from '@playwright/test';\n```\n"

func TestAFailureCarriesThePageAsItWas(t *testing.T) {
	p := filepath.Join(t.TempDir(), "error-context.md")
	if err := os.WriteFile(p, []byte(errorContext), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, a := range []attachment{
		{Name: "error-context", ContentType: "text/markdown", Path: p},
		{Name: "error-context", ContentType: "text/markdown", Body: base64.StdEncoding.EncodeToString([]byte(errorContext))},
	} {
		ft := &fileTests{titles: []string{"finds"}, tests: []test{{Status: "unexpected", Results: []result{{
			Status: "failed", Errors: []testError{{Message: "Error: expect(locator).toBeVisible() failed"}}, Attachments: []attachment{a},
		}}}}}

		ev, ok := end("e2e/x.spec.mjs", ft, false)
		if !ok || ev.Passed || len(ev.Failures) != 1 {
			t.Fatalf("%v %s", ok, judged(ev))
		}

		tail := ev.Failures[0].Tail
		if !strings.Contains(tail, "toBeVisible() failed") || !strings.Contains(tail, "the page at the failure:") ||
			!strings.Contains(tail, `  - heading "Notes" [level=1]`) || strings.Contains(tail, "Instructions") || strings.Contains(tail, "Test source") {
			t.Errorf("the tail:\n%s", tail)
		}
	}
}

func TestAPagesContextIsCapped(t *testing.T) {
	var b strings.Builder

	b.WriteString("```yaml\n- main:\n")

	for i := range 500 {
		fmt.Fprintf(&b, "  - listitem: note %d\n", i)
	}

	b.WriteString("```\n")

	p := filepath.Join(t.TempDir(), "error-context.md")
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	got := pageContext(&result{Attachments: []attachment{{Name: "error-context", Path: p}}})

	if lines := strings.Count(got, "\n"); lines > contextLines+3 || len(got) > contextBytes+200 {
		t.Errorf("%d lines, %d bytes:\n%s", lines, len(got), got)
	}

	if !strings.Contains(got, "note 0") || strings.Contains(got, "note 499") || !strings.Contains(got, "more lines in "+p) {
		t.Errorf("the cut:\n%s", got)
	}
}

func TestARealFailedPageCarriesItsSnapshot(t *testing.T) {
	if testing.Short() || !haveChromium() {
		t.Skip("needs Playwright's Chromium")
	}

	tree := fixture(t)
	req := request(t, tree, true, false, nil)
	req.Slices = []plugin.DriverSlice{{Name: "e2e/page.spec.mjs", Sources: []string{"e2e/page.spec.mjs"}}}

	got, log := runAll(t, req)

	ev, ok := got["e2e/page.spec.mjs"]
	if !ok || ev.Passed || len(ev.Failures) != 1 {
		t.Fatalf("%v %s\n%s", ok, judged(ev), log)
	}

	if tail := ev.Failures[0].Tail; !strings.Contains(tail, "the page at the failure:") || !strings.Contains(tail, `button "Add a note"`) {
		t.Errorf("the tail:\n%s", tail)
	}
}

func haveChromium() bool {
	dir := os.Getenv("PLAYWRIGHT_BROWSERS_PATH")
	if dir == "" {
		home, err := os.UserCacheDir()
		if err != nil {
			return false
		}

		dir = filepath.Join(home, "ms-playwright")
	}

	found, _ := filepath.Glob(filepath.Join(dir, "chromium*")) //nolint:errcheck // a bad pattern finds nothing

	return len(found) > 0
}
