package runstep

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// releases serves GitHub's API and GitLab's release links for the tools of
// one build, and notes each request with its Authorization header.
type releases struct {
	srv  *httptest.Server
	mu   sync.Mutex
	asks []string
}

func newReleases(t *testing.T, binary []byte) *releases {
	t.Helper()

	r := &releases{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.asks = append(r.asks, req.Method+" "+req.Host+req.URL.EscapedPath()+" "+req.Header.Get("Authorization"))
		r.mu.Unlock()

		var out []byte

		switch req.Host + req.URL.EscapedPath() {
		case "api.test/repos/o/plugins/releases/tags/driver/go/v2":
			out = fmt.Appendf(nil, `{"assets": [{"url": "http://api.test/repos/o/plugins/releases/assets/8", "name": "go-driver-linux-amd64"}]}`)
		case "api.test/repos/o/plugins/releases/assets/8",
			"gitlab.com/g/sub/plugins/-/releases/sbom%2Fts%2Fv3/downloads/ts-sbom-linux-amd64":
			out = binary
		default:
			w.WriteHeader(http.StatusNotFound)
		}

		if _, err := w.Write(out); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(r.srv.Close)

	return r
}

func (r *releases) asked() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := r.asks
	r.asks = nil

	return out
}

// client sends every request to the server, whatever its host.
func (r *releases) client() *http.Client {
	to, _ := url.Parse(r.srv.URL) //nolint:errcheck // the test server's own URL

	return &http.Client{Transport: rerouted{to}}
}

type rerouted struct{ to *url.URL }

func (r rerouted) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	out.URL.Scheme, out.URL.Host = r.to.Scheme, r.to.Host
	out.Host = req.URL.Host

	return http.DefaultTransport.RoundTrip(out) //nolint:wrapcheck // the transport's own error
}

func TestTheFetchTakesAToolFromItsDirectorysRelease(t *testing.T) {
	binary := "#!/bin/sh\necho tool\n"
	sum := sha256.Sum256([]byte(binary))
	hexSum := hex.EncodeToString(sum[:])

	r := newReleases(t, []byte(binary))

	tk, out := task(t, "GITHUB_TOKEN=read-token-value", "PIPELINEGEN_GITHUB_API=http://api.test")
	tk.HTTP = r.client()

	f := &fetcher{t: tk, env: tk.Env}
	for _, d := range []string{"cache", "tools/plugins"} {
		if err := os.MkdirAll(filepath.Join(tk.Dir, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}

	pins := pluginPins("go https://github.com/o/plugins v2 " + hexSum + " - driver/go\n" +
		"ts https://gitlab.com/g/sub/plugins.git v3 " + hexSum + " - sbom/ts\n")

	for _, p := range pins {
		if code := f.fetch(p, filepath.Join("tools/plugins", p.name)); code != 0 {
			t.Fatalf("%s: exit %d\n%s", p.name, code, out)
		}
	}

	want := []string{
		"GET api.test/repos/o/plugins/releases/tags/driver/go/v2 Bearer read-token-value",
		"GET api.test/repos/o/plugins/releases/assets/8 Bearer read-token-value",
		// the read token is GitHub's: GitLab gets none
		"HEAD gitlab.com/g/sub/plugins/-/releases/sbom%2Fts%2Fv3/downloads/ts-sbom-linux-amd64 ",
		"GET gitlab.com/g/sub/plugins/-/releases/sbom%2Fts%2Fv3/downloads/ts-sbom-linux-amd64 ",
	}
	if got := r.asked(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("asked\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	for _, p := range []string{"cache/go-driver-release-v2/go-driver", "tools/plugins/go", "cache/ts-sbom-release-v3/ts-sbom", "tools/plugins/ts"} {
		if got := readFile(t, filepath.Join(tk.Dir, p)); got != binary {
			t.Errorf("%s: %q", p, got)
		}
	}

	// a release without the file, and a server whose releases have no files
	for line, says := range map[string]string{
		"x https://gitlab.com/g/sub/plugins v4 " + hexSum + " - sbom/ts\n":    "release sbom/ts/v4 of https://gitlab.com/g/sub/plugins has neither ts-sbom-linux-amd64 nor ts-sbom.mjs",
		"y o/plugins v9 " + hexSum + " - driver/go\n":                         "release driver/go/v9 of o/plugins has neither go-driver-linux-amd64 nor go-driver.mjs",
		"z git@git.example.com:g/plugins.git v1 " + hexSum + " - driver/go\n": "a plain git server has no release files; the runner fetches them from GitHub and GitLab",
	} {
		out.Reset()

		p := pluginPins(line)[0]
		if code := f.fetch(p, filepath.Join("tools/plugins", p.name)); code == 0 || !strings.Contains(out.String(), says) {
			t.Errorf("%s: exit %d\n%s", p.name, code, out)
		}
	}
}

func TestACommitsBuildStartsInItsDirectory(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "tools.log")

	write(t, filepath.Join(bin, "git"), "#!/bin/sh\necho \"git $*\" >> \"$TOOLS_LOG\"\n[ \"$1\" = init ] && mkdir -p \"$3/driver/go\"\nexit 0\n", 0o700)
	write(t, filepath.Join(bin, "go"), "#!/bin/sh\necho \"go $1 $4 in ${PWD#$WORK/} GOWORK=$GOWORK GOPRIVATE=$GOPRIVATE\" >> \"$TOOLS_LOG\"\nprintf '#!/bin/sh\\n' > \"$3\"\n", 0o700)
	// the build's commands are found on the step's own PATH
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	tk, out := task(t, "TOOLS_LOG="+log, "PIPELINEGEN_TOOL_REPOSITORY=o/pipelinegen", "PIPELINEGEN_PRIVATE=github.com/o/plugins")
	tk.Env = append(tk.Env, "WORK="+tk.Dir)

	if err := os.MkdirAll(filepath.Join(tk.Dir, "tools", "plugins"), 0o750); err != nil {
		t.Fatal(err)
	}

	f := &fetcher{t: tk, env: tk.fetchEnv(tk.Env)}

	p := pluginPins("go git@github.com:o/plugins.git - - c0ffee driver/go\n")[0]
	if code := f.fetch(p, "tools/plugins/go"); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}

	want := "git init -q " + filepath.Join(tk.Dir, "src") + "\n" +
		"git -C " + filepath.Join(tk.Dir, "src") + " fetch -q --depth 1 https://github.com/o/plugins.git c0ffee\n" +
		"git -C " + filepath.Join(tk.Dir, "src") + " checkout -q FETCH_HEAD\n" +
		"go build ./cmd/go-driver in src/driver/go GOWORK=off GOPRIVATE=github.com/o/pipelinegen,github.com/o/plugins\n"
	if got := readFile(t, log); got != want {
		t.Errorf("built with\n%s\nwant\n%s", got, want)
	}

	if !tk.executable("tools/plugins/go") || !tk.executable("cache/go-driver-commit-c0ffee/go-driver") {
		t.Error("the build isn't placed")
	}
}
