package concourse

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// o/pipelinegen-sbom-go is fetched into the entry its short name kept
// (go-sbom-release-v1), so switching spellings fetches nothing again.
func TestBootstrapFetchesARenamedRepositorysProgram(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl")
	}

	binary := []byte("#!/bin/sh\necho binary\n")
	script := []byte("#!/usr/bin/env node\nconsole.log('script')\n")
	bsum, ssum := sha256.Sum256(binary), sha256.Sum256(script)

	var (
		base string
		mu   sync.Mutex
		asks []string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()

		asks = append(asks, r.URL.Path)

		mu.Unlock()

		var out []byte

		switch r.URL.Path {
		case "/repos/o/pipelinegen/releases/tags/v1":
			out = release(base, "pipelinegen")
		case "/repos/o/pipelinegen-sbom-go/releases/tags/v1", "/repos/o/go-sbom/releases/tags/v1":
			out = release(base, "go-sbom")
		case "/repos/o/pipelinegen-correlator-react/releases/tags/v1":
			out = fmt.Appendf(nil, `{"assets": [{"url": "%[1]s/repos/o/pipelinegen-correlator-react/releases/assets/9", "id": 9, "name": "react-correlator.mjs"}]}`, base)
		case "/repos/o/pipelinegen-correlator-react/releases/assets/9":
			out = script
		case "/repos/o/pipelinegen/releases/assets/7", "/repos/o/go-sbom/releases/assets/7", "/bazelisk/v1.27.0/bazelisk-linux-amd64":
			out = binary
		default:
			w.WriteHeader(http.StatusNotFound)
		}

		if _, err := w.Write(out); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)

	base = srv.URL
	work := t.TempDir()
	sum := hex.EncodeToString(bsum[:])

	run := func(plugins string) []byte {
		t.Helper()

		mu.Lock()
		asks = nil
		mu.Unlock()

		return mustStep(t, work, cleanEnv(t, "GITHUB_TOKEN=x", "PIPELINEGEN_GITHUB_API="+srv.URL, "PIPELINEGEN_BAZELISK_BASE="+srv.URL+"/bazelisk",
			"PIPELINEGEN_TOOL_REPOSITORY=o/pipelinegen", "PIPELINEGEN_TOOL_VERSION=v1", "PIPELINEGEN_TOOL_SHA256="+sum,
			"PIPELINEGEN_TOOL_COMMIT=", "PIPELINEGEN_BAZELISK_VERSION=v1.27.0", "PIPELINEGEN_BAZELISK_SHA256="+sum,
			"PIPELINEGEN_PLUGINS="+plugins, "PIPELINEGEN_PRIVATE="), "tools")
	}

	run("go o/go-sbom v1 " + sum + " -\nreact o/pipelinegen-correlator-react v1 " + hex.EncodeToString(ssum[:]) + " -\n")

	for p, want := range map[string][]byte{
		"cache/go-sbom-release-v1/go-sbom":                   binary,
		"tools/plugins/go":                                   binary,
		"cache/react-correlator-release-v1/react-correlator": script,
		"tools/plugins/react":                                script,
	} {
		if got, err := os.ReadFile(filepath.Join(work, p)); err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s: %q (%v)", p, got, err)
		}
	}

	// a task's tools start empty: only the cache is shared
	if err := os.RemoveAll(filepath.Join(work, "tools")); err != nil {
		t.Fatal(err)
	}

	out := run("go o/pipelinegen-sbom-go v1 " + sum + " -\nreact o/pipelinegen-correlator-react v1 " + hex.EncodeToString(ssum[:]) + " -\n")

	mu.Lock()
	got := strings.Join(asks, " ")
	mu.Unlock()

	if strings.Contains(got, "/repos/o/") || strings.Contains(string(out), "dropped") {
		t.Errorf("switching the spelling fetched or dropped: asked %s\n%s", got, out)
	}

	if b, err := os.ReadFile(filepath.Join(work, "tools/plugins/go")); err != nil || !bytes.Equal(b, binary) {
		t.Errorf("tools/plugins/go: %q (%v)", b, err)
	}

	// a task's tools start empty
	for _, d := range []string{"cache", "tools"} {
		if err := os.RemoveAll(filepath.Join(work, d)); err != nil {
			t.Fatal(err)
		}
	}

	asked := func() {
		mu.Lock()
		defer mu.Unlock()

		if !strings.Contains(strings.Join(asks, " "), "/repos/o/pipelinegen-sbom-go/releases/tags/v1") {
			t.Errorf("the release wasn't asked of the repository the pin names: %v", asks)
		}
	}

	run("go o/pipelinegen-sbom-go v1 " + sum + " -\n")
	asked()

	if b, err := os.ReadFile(filepath.Join(work, "cache/go-sbom-release-v1/go-sbom")); err != nil || !bytes.Equal(b, binary) {
		t.Errorf("cache/go-sbom-release-v1/go-sbom: %q (%v)", b, err)
	}
}

func TestBootstrapBuildsARenamedRepositorysProgram(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program with an empty build cache")
	}

	for _, tool := range []string{"curl", "git", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("no %s", tool)
		}
	}

	goroot, err := exec.CommandContext(t.Context(), "go", "env", "GOROOT").Output()
	if err != nil {
		t.Skipf("go env GOROOT: %v", err)
	}

	gobin := filepath.Join(strings.TrimSpace(string(goroot)), "bin")

	// <hosts>/o/pipelinegen-sbom-go.git stands for https://github.com/.
	hosts := t.TempDir()
	src := filepath.Join(hosts, "o", "pipelinegen-sbom-go.git")

	for name, content := range map[string]string{
		"go.mod":              "module github.com/o/pipelinegen-sbom-go\n\ngo 1.27\n",
		"cmd/go-sbom/main.go": "package main\n\nfunc main() { println(\"go-sbom\") }\n",
	} {
		p := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	git := func(args ...string) string {
		t.Helper()

		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = src
		cmd.Env = cleanEnv(t)

		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}

		return strings.TrimSpace(string(out))
	}

	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "go-sbom")
	git("config", "uploadpack.allowAnySHA1InWant", "true")
	commit := git("rev-parse", "HEAD")

	gitconfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(gitconfig, []byte("[url \"file://"+hosts+"/\"]\n\tinsteadOf = https://github.com/\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := []byte("#!/bin/sh\necho binary\n")
	bsum := sha256.Sum256(binary)
	sum := hex.EncodeToString(bsum[:])

	srv := releases(t, binary)
	work := t.TempDir()

	mustStep(t, work, cleanEnv(t, "PATH="+gobin+string(os.PathListSeparator)+os.Getenv("PATH"), "GIT_CONFIG_GLOBAL="+gitconfig, "GOTOOLCHAIN=local",
		"GOPROXY=off", "GITHUB_TOKEN=x", "PIPELINEGEN_GITHUB_API="+srv.URL, "PIPELINEGEN_BAZELISK_BASE="+srv.URL+"/bazelisk",
		"PIPELINEGEN_TOOL_REPOSITORY=o/pipelinegen", "PIPELINEGEN_TOOL_VERSION=v1", "PIPELINEGEN_TOOL_SHA256="+sum,
		"PIPELINEGEN_TOOL_COMMIT=", "PIPELINEGEN_BAZELISK_VERSION=v1.27.0", "PIPELINEGEN_BAZELISK_SHA256="+sum,
		"PIPELINEGEN_PLUGINS=go o/pipelinegen-sbom-go - - "+commit+"\n", "PIPELINEGEN_PRIVATE="), "tools")

	built := filepath.Join(work, "cache", "go-sbom-commit-"+commit, "go-sbom")

	out, err := exec.CommandContext(t.Context(), built).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "go-sbom" {
		t.Errorf("%s: %q (%v)", built, out, err)
	}

	if _, err := os.Stat(filepath.Join(work, "tools", "plugins", "go")); err != nil {
		t.Errorf("the build wasn't placed: %v", err)
	}
}
