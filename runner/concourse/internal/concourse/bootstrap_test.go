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
	"slices"
	"strings"
	"testing"
)

func releases(t *testing.T, body []byte) *httptest.Server {
	t.Helper()

	var base string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var out []byte

		switch r.URL.Path {
		case "/repos/o/pipelinegen/releases/tags/v1":
			out = release(base, "pipelinegen")
		case "/repos/o/go-sbom/releases/tags/v1":
			out = release(base, "go-sbom")
		case "/repos/o/pipelinegen/releases/assets/7", "/repos/o/go-sbom/releases/assets/7",
			"/bazelisk/v1.27.0/bazelisk-linux-amd64":
			out = body
		default:
			w.WriteHeader(http.StatusNotFound)
		}

		if _, err := w.Write(out); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)

	base = srv.URL

	return srv
}

func runBootstrap(t *testing.T, work, sum string, srv *httptest.Server) ([]byte, error) {
	t.Helper()

	out, code := runStep(t, work, pinsEnv(t, sum, srv), "tools")
	if code != 0 {
		return out, fmt.Errorf("bootstrap: %w %d", errExit, code)
	}

	return out, nil
}

// pinsEnv pins every tool to the release server's one file.
func pinsEnv(t *testing.T, sum string, srv *httptest.Server) []string {
	t.Helper()

	return cleanEnv(t, "GITHUB_TOKEN=x", "PIPELINEGEN_GITHUB_API="+srv.URL, "PIPELINEGEN_BAZELISK_BASE="+srv.URL+"/bazelisk",
		"PIPELINEGEN_TOOL_REPOSITORY=o/pipelinegen", "PIPELINEGEN_TOOL_VERSION=v1", "PIPELINEGEN_TOOL_SHA256="+sum,
		"PIPELINEGEN_TOOL_COMMIT=", "PIPELINEGEN_BAZELISK_VERSION=v1.27.0", "PIPELINEGEN_BAZELISK_SHA256="+sum,
		"PIPELINEGEN_PLUGINS=go o/go-sbom v1 "+sum+" -\n", "PIPELINEGEN_PRIVATE=")
}

// A build aborted while its start task waited for a worker has no tools.
func TestTheEndFetchesTheToolsTheBuildNeverHad(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl")
	}

	for _, c := range []struct {
		name              string
		noTrigger, badSum bool
		want              []string
		fetched           bool
	}{
		{name: "fetched", want: []string{"report suite"}, fetched: true},
		{name: "no trigger", noTrigger: true},
		{name: "unfetchable", badSum: true},
	} {
		work := t.TempDir()
		fakeTool(t, work, true)

		tool, err := os.ReadFile(filepath.Join(work, "tools", "pipelinegen"))
		if err != nil {
			t.Fatal(err)
		}

		for _, d := range []string{"tools", "source", "batches"} {
			if err := os.RemoveAll(filepath.Join(work, d)); err != nil {
				t.Fatal(err)
			}
		}

		if c.noTrigger {
			if err := os.RemoveAll(filepath.Join(work, "trigger")); err != nil {
				t.Fatal(err)
			}
		}

		sum := sha256.Sum256(tool)
		if c.badSum {
			sum = sha256.Sum256([]byte("another tool"))
		}

		out, code := runStep(t, work, pinsEnv(t, hex.EncodeToString(sum[:]), releases(t, tool)), "end")
		if code != 0 {
			t.Errorf("%s: exit %d\n%s", c.name, code, out)
		}

		if got := calls(t, work); !slices.Equal(got, c.want) {
			t.Errorf("%s: calls %v\n%s", c.name, got, out)
		}

		if _, err := os.Stat(filepath.Join(work, "tools", "bazelisk")); (err == nil) != c.fetched {
			t.Errorf("%s: tools fetched %v, want %v\n%s", c.name, err == nil, c.fetched, out)
		}
	}
}

var cached = []string{"cache/pipelinegen-release-v1/pipelinegen", "cache/go-sbom-release-v1/go-sbom", "cache/bazelisk-v1.27.0"}

func TestBootstrapChecksItsCacheEveryTime(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl")
	}

	right := []byte("#!/bin/sh\necho right\n")
	sum := sha256.Sum256(right)
	work := t.TempDir()

	for _, p := range cached {
		file := filepath.Join(work, p)
		if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(file, []byte("#!/bin/sh\necho wrong\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	if out, err := runBootstrap(t, work, hex.EncodeToString(sum[:]), releases(t, right)); err != nil {
		t.Fatalf("bootstrap: %v\n%s", err, out)
	}

	for _, p := range append([]string{"tools/pipelinegen", "tools/plugins/go", "tools/bazelisk"}, cached...) {
		got, err := os.ReadFile(filepath.Join(work, p))
		if err != nil || !bytes.Equal(got, right) {
			t.Errorf("%s isn't the verified file: %v", p, err)
		}
	}
}

func TestBootstrapKeepsOnlyWhatItsPinsName(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl")
	}

	right := []byte("#!/bin/sh\necho right\n")
	sum := sha256.Sum256(right)
	work := t.TempDir()

	others := []string{
		"cache/pipelinegen-release-v0/pipelinegen", "cache/go-sbom-release-v0/go-sbom",
		"cache/sql-correlator-release-v2/sql-correlator", "cache/go-sbom-commit-0123abc/go-sbom",
		"cache/bazelisk-v1.25.0", "cache/bazelisk-v1.27.0.tmp",
	}
	stays := []string{"cache/gomodcache/m@v1/go.mod", "cache/gocache/README"}

	for _, p := range slices.Concat(cached, others, stays) {
		file := filepath.Join(work, p)
		if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(file, right, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	out, err := runBootstrap(t, work, hex.EncodeToString(sum[:]), releases(t, right))
	if err != nil {
		t.Fatalf("bootstrap: %v\n%s", err, out)
	}

	for _, p := range slices.Concat(cached, stays) {
		if _, err := os.Stat(filepath.Join(work, p)); err != nil {
			t.Errorf("%s left the cache: %v", p, err)
		}
	}

	for _, p := range append(others, "cache/.pinned") {
		if _, err := os.Stat(filepath.Join(work, p)); err == nil {
			t.Errorf("%s stayed in the cache", p)
		}
	}

	want := "dropped from the job's cache, as no pin names them: go-sbom-release-v0 pipelinegen-release-v0 " +
		"sql-correlator-release-v2 go-sbom-commit-0123abc bazelisk-v1.25.0 bazelisk-v1.27.0.tmp\n"
	if !strings.Contains(string(out), want) {
		t.Errorf("the bootstrap didn't name what it dropped:\n%s", out)
	}
}

func TestBootstrapRefusesAWrongDownload(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl")
	}

	sum := sha256.Sum256([]byte("#!/bin/sh\necho right\n"))
	work := t.TempDir()

	out, err := runBootstrap(t, work, hex.EncodeToString(sum[:]), releases(t, []byte("#!/bin/sh\necho tampered\n")))
	if err == nil {
		t.Fatalf("a wrong download was accepted\n%s", out)
	}

	if !strings.Contains(string(out), "doesn't match its sha256") {
		t.Errorf("the bootstrap didn't say why: %s", out)
	}

	for _, p := range append([]string{"tools/pipelinegen", cached[0] + ".tmp"}, cached...) {
		if _, err := os.Stat(filepath.Join(work, p)); err == nil {
			t.Errorf("%s was left behind", p)
		}
	}
}

func TestBootstrapTakesANodePluginsScript(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl")
	}

	binary := []byte("#!/bin/sh\necho binary\n")
	script := []byte("#!/usr/bin/env node\nconsole.log('script')\n")
	bsum, ssum := sha256.Sum256(binary), sha256.Sum256(script)

	var base string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var out []byte

		switch r.URL.Path {
		case "/repos/o/pipelinegen/releases/tags/v1":
			out = release(base, "pipelinegen")
		case "/repos/o/ts-sbom/releases/tags/v1":
			out = fmt.Appendf(nil, `{"assets": [{"url": "%[1]s/repos/o/ts-sbom/releases/assets/8", "id": 8, "name": "plugin.json"},
				{"url": "%[1]s/repos/o/ts-sbom/releases/assets/10", "id": 10, "name": "ts-sbomXmjs"},
				{"url": "%[1]s/repos/o/ts-sbom/releases/assets/9", "id": 9, "name": "ts-sbom.mjs"}]}`, base)
		case "/repos/o/empty/releases/tags/v1":
			out = []byte(`{"assets": []}`)
		case "/repos/o/ts-sbom/releases/assets/9":
			out = script
		case "/repos/o/pipelinegen/releases/assets/7", "/bazelisk/v1.27.0/bazelisk-linux-amd64":
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

	run := func(plugins string) (string, []byte, error) {
		work := t.TempDir()

		out, code := runStep(t, work, cleanEnv(t, "GITHUB_TOKEN=x", "PIPELINEGEN_GITHUB_API="+srv.URL, "PIPELINEGEN_BAZELISK_BASE="+srv.URL+"/bazelisk",
			"PIPELINEGEN_TOOL_REPOSITORY=o/pipelinegen", "PIPELINEGEN_TOOL_VERSION=v1", "PIPELINEGEN_TOOL_SHA256="+hex.EncodeToString(bsum[:]),
			"PIPELINEGEN_TOOL_COMMIT=", "PIPELINEGEN_BAZELISK_VERSION=v1.27.0", "PIPELINEGEN_BAZELISK_SHA256="+hex.EncodeToString(bsum[:]),
			"PIPELINEGEN_PLUGINS="+plugins, "PIPELINEGEN_PRIVATE="), "tools")
		if code != 0 {
			return work, out, fmt.Errorf("bootstrap: %w %d", errExit, code)
		}

		return work, out, nil
	}

	work, out, err := run("ts o/ts-sbom v1 " + hex.EncodeToString(ssum[:]) + " -\n")
	if err != nil {
		t.Fatalf("bootstrap: %v\n%s", err, out)
	}

	placed := filepath.Join(work, "tools", "plugins", "ts")

	got, err := os.ReadFile(placed)
	if err != nil || !bytes.Equal(got, script) {
		t.Fatalf("the plugin isn't the script: %q %v", got, err)
	}

	if info, err := os.Stat(placed); err != nil || info.Mode()&0o100 == 0 {
		t.Errorf("the script isn't executable: %v %v", info, err)
	}

	if _, out, err := run("ts o/ts-sbom v1 " + hex.EncodeToString(bsum[:]) + " -\n"); err == nil ||
		!strings.Contains(string(out), "ts-sbom.mjs doesn't match its sha256") {
		t.Errorf("a wrong script was accepted: %v\n%s", err, out)
	}

	if _, out, err := run("x o/empty v1 " + hex.EncodeToString(ssum[:]) + " -\n"); err == nil ||
		!strings.Contains(string(out), "has neither empty-linux-amd64 nor empty.mjs") {
		t.Errorf("a release with neither was accepted: %v\n%s", err, out)
	}
}
