package runstep

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// pluginZip is a plugin packed as pipelinegen plugin build packs one: its manifest at the top level, a
// program for each platform beside it.
func pluginZip(t *testing.T) []byte {
	t.Helper()

	var b bytes.Buffer

	zw := zip.NewWriter(&b)

	files := map[string]string{
		"manifest.yaml": `name: hello
kind: driver
program: hello
build:
  image: busybox@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec
  run: [sh, -c, "true"]
platforms: [linux/amd64, darwin/arm64]
files: [LICENSE]
`,
		"LICENSE":            "license",
		"hello-linux-amd64":  "#!/bin/sh\necho linux\n",
		"hello-darwin-arm64": "#!/bin/sh\necho darwin\n",
	}

	for name, body := range files {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(0o644)

		if strings.HasPrefix(name, "hello-") {
			h.SetMode(0o755)
		}

		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return b.Bytes()
}

func TestAZipPinnedPluginIsUnpackedOncePerWorker(t *testing.T) {
	data := pluginZip(t)
	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])

	var hits atomic.Int32

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)

		_, _ = w.Write(data) //nolint:errcheck // a test server
	}))
	t.Cleanup(srv.Close)

	cache := t.TempDir()

	for build := range 2 {
		tk, out := task(t, "PIPELINEGEN_TOOL_REPOSITORY=o/pipelinegen", "PIPELINEGEN_TOOL_COMMIT=c0ffee",
			"PIPELINEGEN_PLUGINS=hello - - - - - - "+srv.URL+"/hello.zip "+hexSum+"\n")
		tk.HTTP = srv.Client()

		// the task's cache, kept on the worker between builds
		if err := os.Symlink(cache, filepath.Join(tk.Dir, "cache")); err != nil {
			t.Fatal(err)
		}

		write(t, filepath.Join(tk.Dir, tool), "#!/bin/sh\n", 0o700)

		if code := tk.fetchTools(tk.Env); code != 0 {
			t.Fatalf("build %d: exit %d\n%s", build, code, out)
		}

		got, err := os.ReadFile(filepath.Join(tk.Dir, "tools", "plugins", "hello"))
		if err != nil || string(got) != "#!/bin/sh\necho linux\n" {
			t.Fatalf("build %d: tools/plugins/hello holds %q: %v", build, got, err)
		}
	}

	if n := hits.Load(); n != 1 {
		t.Errorf("the zip was downloaded %d times, want once a worker", n)
	}

	// a build whose pins name the zip no more drops it from the cache
	tk, out := task(t, "PIPELINEGEN_TOOL_REPOSITORY=o/pipelinegen", "PIPELINEGEN_TOOL_COMMIT=c0ffee")
	if err := os.Symlink(cache, filepath.Join(tk.Dir, "cache")); err != nil {
		t.Fatal(err)
	}

	write(t, filepath.Join(tk.Dir, tool), "#!/bin/sh\n", 0o700)

	if code := tk.fetchTools(tk.Env); code != 0 || tk.exists(filepath.Join("cache", "plugins", hexSum)) {
		t.Fatalf("the unpinned zip is kept: exit %d\n%s", code, out)
	}
}

func TestAZipThatIsntItsPinIsRefused(t *testing.T) {
	data := pluginZip(t)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(data) //nolint:errcheck // a test server
	}))
	t.Cleanup(srv.Close)

	tk, out := task(t, "PIPELINEGEN_TOOL_REPOSITORY=o/pipelinegen", "PIPELINEGEN_TOOL_COMMIT=c0ffee",
		"PIPELINEGEN_PLUGINS=hello - - - - - - "+srv.URL+"/hello.zip "+strings.Repeat("a", 64)+"\n")
	tk.HTTP = srv.Client()
	write(t, filepath.Join(tk.Dir, tool), "#!/bin/sh\n", 0o700)

	if code := tk.fetchTools(tk.Env); code == 0 || tk.exists("tools/plugins/hello") {
		t.Fatalf("a zip off its sha256 was placed: exit %d\n%s", code, out)
	}
}
