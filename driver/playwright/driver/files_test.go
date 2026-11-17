package driver

import (
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func TestAFailedTestsAttachmentsAreKept(t *testing.T) {
	results := t.TempDir()
	shot := filepath.Join(results, "a-finds-chromium", "test-failed-1.png")
	trace := filepath.Join(results, "a-finds-chromium", "trace.zip")
	video := filepath.Join(results, "a-finds-chromium", "video.webm")
	pipe := filepath.Join(results, "a-finds-chromium", "pipe")

	for p, body := range map[string]string{shot: "\x89PNG\x00", trace: "PK\x03\x04", video: "webm"} {
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}

	ft := &fileTests{titles: []string{"finds", "passes"}, tests: []test{
		{ProjectName: "chromium", Status: "unexpected", Results: []result{{Status: "failed", Attachments: []attachment{
			{Name: "screenshot", ContentType: "image/png", Path: shot},
			{Name: "video", ContentType: "video/webm", Path: video},
			{Name: "trace", ContentType: "application/zip", Path: trace},
			{Name: "screenshot", ContentType: "image/png", Path: pipe},
			{Name: "error-context", ContentType: "text/markdown", Body: base64.StdEncoding.EncodeToString([]byte("# Page\n"))},
		}}}},
		{Status: "expected", Results: []result{{Status: "passed", Attachments: []attachment{{Name: "screenshot", Path: shot}}}}},
	}}

	dir := filepath.Join(t.TempDir(), "files")
	got := keep(dir, ft, io.Discard)

	want := []plugin.File{
		{Path: "a-finds-chromium/test-failed-1.png", Label: "screenshot of [chromium] finds"},
		{Path: "a-finds-chromium/trace.zip", Label: "trace of [chromium] finds"},
		{Path: "a-finds-chromium/error-context.md", Label: "page at the failure of [chromium] finds"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("kept %+v", got)
	}

	for p, body := range map[string]string{"test-failed-1.png": "\x89PNG\x00", "trace.zip": "PK\x03\x04", "error-context.md": "# Page\n"} {
		if data, err := os.ReadFile(filepath.Join(dir, "a-finds-chromium", p)); err != nil || string(data) != body {
			t.Errorf("%s: %q %v", p, data, err)
		}
	}
}

func TestRealFailedSpecsKeepTheirFilesRunOneByOne(t *testing.T) {
	if testing.Short() || !haveChromium() {
		t.Skip("needs Playwright's Chromium")
	}

	tree := fixture(t)
	req := request(t, tree, false, false, nil)
	req.Config = map[string]any{"config": "playwright.files.config.mjs"}
	req.Slices = nil

	for _, s := range []string{"e2e/page.spec.mjs", "e2e/shot.spec.mjs", "e2e/a.spec.mjs"} {
		req.Slices = append(req.Slices, plugin.DriverSlice{Name: s, Sources: []string{s}, Files: filepath.Join(t.TempDir(), "files")})
	}

	got, log := runAll(t, req)

	for i, s := range []string{"e2e/page.spec.mjs", "e2e/shot.spec.mjs"} {
		ev := got[s]

		var labels []string

		for _, f := range ev.Files {
			labels = append(labels, strings.Fields(f.Label)[0])

			if _, err := os.Stat(filepath.Join(req.Slices[i].Files, filepath.FromSlash(f.Path))); err != nil {
				t.Errorf("%s: %s: %v", s, f.Path, err)
			}
		}

		slices.Sort(labels)

		if ev.Passed || !slices.Equal(labels, []string{"page", "screenshot", "trace"}) {
			t.Errorf("%s: %s, files %+v\n%s", s, judged(ev), ev.Files, log)
		}
	}

	if ev := got["e2e/a.spec.mjs"]; !ev.Passed || len(ev.Files) != 0 {
		t.Errorf("a passed spec: %s, files %+v", judged(ev), ev.Files)
	}

	req.Config["files"] = false
	req.Slices = req.Slices[:1]

	if got, _ := runAll(t, req); len(got["e2e/page.spec.mjs"].Files) != 0 {
		t.Errorf("files: false kept %+v", got["e2e/page.spec.mjs"].Files)
	}
}
