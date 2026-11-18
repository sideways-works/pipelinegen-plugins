package concourse

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const supersededConfig = `pipeline: shop
suites:
  e2e:
    tests: ["e2e/*.spec.ts"]
    run_slice: ci/e2e.yml
git: {remote: "REMOTE"}
concourse: {url: "https://ci", repository: {uri: x}, planner: {image: {repository: p}, digest: "sha256:x", bootstrap: {release: {version: v1, sha256: 0000000000000000000000000000000000000000000000000000000000000000}, bazelisk: {version: v1, sha256: 0000000000000000000000000000000000000000000000000000000000000000}}}}
runs:
  full:
    trigger: {branch: main, continuous: true}
    compare: none
    select: all
    suites: {e2e: {}}
`

func TestASupersededCommitRunsNothing(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go to build pipelinegen with")
	}

	work := t.TempDir()
	bare := filepath.Join(t.TempDir(), "remote.git")
	repo := t.TempDir()

	git := func(dir string, args ...string) string {
		t.Helper()

		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.test"}, args...)...)
		cmd.Env = cleanEnv(t)

		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}

		return strings.TrimSpace(string(out))
	}

	git(work, "init", "-q", "--bare", bare)
	git(repo, "init", "-q", "-b", "main")

	for p, body := range map[string]string{
		"pipelinegen.yaml": strings.Replace(supersededConfig, "REMOTE", "file://"+bare, 1),
		"e2e/cart.spec.ts": "test('cart')\n",
		"ci/e2e.yml":       "platform: linux\nrun: {path: true}\n",
		"MODULE.bazel":     "module(name = \"shop\")\n",
		"e2e/BUILD.bazel":  "",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, p)), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(repo, p), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	git(repo, "add", "-A")
	git(repo, "commit", "-q", "-m", "one")
	old := git(repo, "rev-parse", "HEAD")
	git(repo, "push", "-q", bare, "HEAD:refs/heads/main")
	git(repo, "commit", "-q", "--allow-empty", "-m", "two")
	tip := git(repo, "rev-parse", "HEAD")
	git(repo, "push", "-q", bare, "HEAD:refs/heads/main")

	// The git resource checks out each version every build takes.
	git(work, "clone", "-q", bare, "trigger")
	git(filepath.Join(work, "trigger"), "checkout", "-q", "--detach", old)

	build := exec.CommandContext(t.Context(), gobin, "build", "-o", filepath.Join(work, "tools", "pipelinegen"), "github.com/sideways-works/pipelinegen/cmd/pipelinegen")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building pipelinegen: %v\n%s", err, out)
	}

	env := cleanEnv(t, "PIPELINEGEN_RUN=full", "PIPELINEGEN_TRIGGER=trigger", "PIPELINEGEN_JOB_URL=https://ci/builds/1")

	out, code := runStep(t, work, env, "plan")
	if code != 0 || !strings.Contains(string(out), "superseded by "+tip[:12]) {
		t.Fatalf("the plan step: exit %d\n%s", code, out)
	}

	for _, d := range []string{"batches", "gate", "source"} {
		if err := os.MkdirAll(filepath.Join(work, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}

	suiteEnv := append(slices.Clone(env), "PIPELINEGEN_SUITE=e2e")

	mustStep(t, work, suiteEnv, "start")

	data, err := os.ReadFile(filepath.Join(work, "batches", "batches.json"))
	if err != nil || strings.TrimSpace(string(data)) != "[]" {
		t.Fatalf("a superseded commit's suite dealt batches: %q %v", data, err)
	}
}
