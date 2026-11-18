package concourse

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

var update = flag.Bool("update", false, "write the golden pipelines the packages tests compare with")

const (
	keyBackend = "1111111111111111111111111111111111111111111111111111111111111111"
	keyE2E     = "2222222222222222222222222222222222222222222222222222222222222222"
	golangSum  = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

const packagesInclude = `resources:
- name: golang
  type: registry-image
  source: {repository: golang, tag: "1.27", username: ci, password: ((hub_password))}
  version: {digest: "` + golangSum + `"}
- name: builder
  type: registry-image
  source: {repository: registry.example/builder}
`

func script(t *testing.T, name string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", "packages", name))
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

// withPackages is the fixture with backend run by drivers, its prepare in
// another image, e2e's task files in golang, and packages for both and for
// the planner; with a registry, each suite's golang has a cached image.
func withPackages(t *testing.T, registry bool) (plugin.Spec, Packages, string) {
	t.Helper()

	spec, root := driven(t)
	spec.CI["include"] = "ci/root.yml"
	spec.Suites[suiteIndex(t, spec, "e2e")].Image = "golang"

	if err := os.MkdirAll(filepath.Join(root, "ci"), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "ci", "root.yml"), []byte(packagesInclude), 0o600); err != nil {
		t.Fatal(err)
	}

	backend := SpecPackages{Names: []string{"jq"}, Install: script(t, "install-jq.sh")}
	e2e := SpecPackages{Names: []string{"git", "jq"}, Install: script(t, "install-jq-git.sh")}

	pkgs := Packages{
		Planner: &SpecPackages{Names: []string{"curl"}, Install: script(t, "install-curl.sh")},
		Suites:  map[string]SpecPackages{"backend": backend, "e2e": e2e},
	}

	if registry {
		backend.Images = map[string]string{"golang": keyBackend}
		e2e.Images = map[string]string{"golang": keyE2E}
		pkgs.Suites = map[string]SpecPackages{"backend": backend, "e2e": e2e}
		pkgs.Images = &SpecImages{
			Registry: "registry.example/ci-images", Credentials: "registry_push", PullCredentials: "registry_pull",
			Builds: []SpecImageBuild{
				{Key: keyBackend, Base: "golang", Digest: golangSum, Names: backend.Names, Install: backend.Install},
				{Key: keyE2E, Base: "golang", Digest: golangSum, Names: e2e.Names, Install: e2e.Install},
			},
		}
	}

	return spec, pkgs, root
}

func golden(t *testing.T, name string, p M) {
	t.Helper()

	file := filepath.Join("testdata", name)
	got := text(t, p)

	if *update {
		if err := os.WriteFile(file, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	if got != string(want) {
		t.Errorf("%s: the pipeline differs from the golden file; go test -run %s -update writes it", name, t.Name())
	}

	if fly, err := exec.LookPath("fly"); err == nil {
		if out, err := exec.CommandContext(t.Context(), fly, "validate-pipeline", "--strict", "-c", file).CombinedOutput(); err != nil {
			t.Fatalf("fly: %v\n%s", err, out)
		}
	}
}

// The goldens: without packages the pipeline is the one the spec always
// gave; with packages, every step installs them; with a registry, too, the
// suites' tasks run in their cached images and a job builds each.
func TestPackagesGoldens(t *testing.T) {
	for name, registry := range map[string]bool{"pipeline-packages.golden.yml": false, "pipeline-images.golden.yml": true} {
		t.Run(name, func(t *testing.T) {
			spec, pkgs, root := withPackages(t, registry)

			p, err := GenerateAll(spec, pkgs, root, fixtureConfig())
			if err != nil {
				t.Fatal(err)
			}

			golden(t, name, p)
		})
	}

	spec, root := fixture(t)

	p, err := GenerateAll(spec, Packages{}, root, fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}

	want, err := os.ReadFile(filepath.Join("testdata", "pipeline.golden.yml"))
	if err != nil {
		t.Fatal(err)
	}

	if text(t, p) != string(want) {
		t.Fatal("no packages: the pipeline changed")
	}
}

func TestEveryToolTaskInstallsFirst(t *testing.T) {
	spec, pkgs, root := withPackages(t, false)

	p, err := GenerateAll(spec, pkgs, root, nil)
	if err != nil {
		t.Fatal(err)
	}

	var tasks int

	for _, j := range asSlice(p["jobs"]) {
		walkTasks(asMap(j), func(task M) {
			cfg := asMap(task["config"])
			run := asMap(cfg["run"])
			args := fmt.Sprint(run["args"])

			if run["path"] != "sh" || !runsTheTool(run) {
				return
			}

			tasks++

			want := pkgs.Planner.Install
			if strings.Contains(args, "tools/pipelinegen slice") {
				want = pkgs.Suites["backend"].Install
			}

			if !strings.Contains(args, want) {
				t.Errorf("task %v installs not what it needs first: %s", task["task"], args)
			}

			if asMap(cfg["params"])[envCache] != packageCache || !slices.ContainsFunc(cacheList(cfg["caches"]), func(c M) bool { return c["path"] == packageCache }) {
				t.Errorf("task %v keeps no download cache: %v %v", task["task"], cfg["params"], cfg["caches"])
			}
		})
	}

	if tasks < 10 {
		t.Fatalf("only %d tasks install", tasks)
	}
}

// A cached image comes from a task, never a get: a get is the job's input,
// and the job would wait for an image the registry hasn't got.
func TestACachedImageIsFetchedByATask(t *testing.T) {
	spec, pkgs, root := withPackages(t, true)

	p, err := GenerateAll(spec, pkgs, root, nil)
	if err != nil {
		t.Fatal(err)
	}

	job := text(t, M{"jobs": []any{jobByName(t, p, "pr-backend")}})

	for _, want := range []string{
		"task: image-" + short(keyBackend), "image: cached-" + short(keyBackend), "- name: cached-" + short(keyBackend),
		"((registry_pull.username))", `"tag":"` + keyBackend + `"`, `"repository":"golang"`, "BASE_DIGEST: " + golangSum,
		// the prepare's image has no cached one: it is got, and the task installs
		"get: builder",
	} {
		if !strings.Contains(job, want) {
			t.Errorf("no %q in pr-backend", want)
		}
	}

	if strings.Contains(job, "get: golang") || strings.Contains(job, "registry_push") || strings.Contains(job, "cached-"+short(keyE2E)) {
		t.Errorf("pr-backend gets the base, holds the pushing var or another suite's image:\n%s", job)
	}

	e2e := text(t, M{"jobs": []any{jobByName(t, p, "pr-e2e")}})
	if strings.Count(e2e, "image: cached-"+short(keyE2E)) < 2 {
		t.Errorf("e2e's task files don't run in its cached image:\n%s", e2e)
	}
}

func TestAJobBuildsEachCachedImage(t *testing.T) {
	spec, pkgs, root := withPackages(t, true)

	p, err := GenerateAll(spec, pkgs, root, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{keyBackend, keyE2E} {
		job := jobByName(t, p, imageJob(key))
		s := text(t, M{"jobs": []any{job}})

		for _, want := range []string{
			"get: config\n", "trigger: true", "get: base\n", "resource: golang", "format: oci", "task: look", "load_var: missing", "task: build",
			"privileged: true", "IMAGE_ARG_base_image: base/image.tar", "put: cached-" + short(key), "no_get: true",
		} {
			if !strings.Contains(s, want) {
				t.Errorf("%s: no %q", imageJob(key), want)
			}
		}

		for _, never := range []string{"serial", "max_in_flight", "name: source\n", "file: source", "registry_pull", "lock"} {
			if strings.Contains(s, never) {
				t.Errorf("%s: %q", imageJob(key), never)
			}
		}
	}

	for _, j := range asSlice(p["jobs"]) {
		name := fmt.Sprint(asMap(j)["name"])
		if s := text(t, M{"jobs": []any{j}}); strings.Contains(s, "registry_push") && !strings.HasPrefix(name, "image-") {
			t.Errorf("job %s holds the var that pushes", name)
		}
	}

	resources := text(t, M{"resources": p["resources"]})
	if strings.Count(resources, "check_every: never") != 3 || strings.Count(resources, "((registry_push.password))") != 2 {
		t.Errorf("the push resources: %d %d\n%s", strings.Count(resources, "check_every: never"), strings.Count(resources, "((registry_push.password))"), resources)
	}
}

// The runner reads the spec's packages from the request: an old request
// has none, and the pipeline is the one it always was.
func TestReadPackages(t *testing.T) {
	spec, pkgs, _ := withPackages(t, true)

	data, err := json.Marshal(map[string]any{"spec": spec})
	if err != nil {
		t.Fatal(err)
	}

	got, err := ReadPackages(data)
	if err != nil || got.Images != nil || got.Planner != nil || len(got.Suites) > 0 {
		t.Fatalf("an old request: %+v %v", got, err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}

	s := asMap(raw["spec"])
	s["planner_packages"], s["images"] = pkgs.Planner, pkgs.Images

	for _, su := range asSlice(s["suites"]) {
		if sp, ok := pkgs.Suites[fmt.Sprint(asMap(su)["name"])]; ok {
			asMap(su)["packages"] = sp
		}
	}

	if data, err = json.Marshal(raw); err != nil {
		t.Fatal(err)
	}

	got, err = ReadPackages(data)
	if err != nil || got.Images.Builds[1].Key != keyE2E || got.Suites["backend"].Images["golang"] != keyBackend || got.Planner.Names[0] != "curl" {
		t.Fatalf("a request with packages: %+v %v", got, err)
	}

	if !slices.Contains(describe().Capabilities, capPackages) {
		t.Fatal("the runner doesn't say it installs packages")
	}
}

func TestTheWrappedStepRunsInImages(t *testing.T) {
	tool, err := exec.LookPath("docker")
	if err != nil {
		if tool, err = exec.LookPath("podman"); err != nil {
			t.Skip("neither docker nor podman")
		}
	}

	if exec.CommandContext(t.Context(), tool, "info").Run() != nil {
		t.Skip(tool + " doesn't answer")
	}

	task := M{"task": "batch", "config": M{"params": M{}, "run": M{"path": "tools/pipelinegen", "args": []string{"slice", "run", "--root", "source"}}}}
	g := &gen{pkgs: Packages{Suites: map[string]SpecPackages{"unit": {Install: script(t, "install-jq.sh")}}}}
	g.install(task, "unit")

	run := asMap(asMap(task["config"])["run"])
	args := make([]string, 0, len(asSlice(run["args"])))

	for _, a := range asSlice(run["args"]) {
		args = append(args, fmt.Sprint(a))
	}

	stand := "mkdir -p tools && printf '#!/bin/sh\\necho tool: \"$*\"; jq --version\\n' > tools/pipelinegen && chmod +x tools/pipelinegen && "
	cmd := "cd /tmp && " + stand + "PIPELINEGEN_PACKAGE_CACHE=pkgcache sh \"$@\" && PIPELINEGEN_PACKAGE_CACHE=pkgcache sh \"$@\""

	for _, image := range []string{"alpine:3.20", "debian:bookworm-slim"} {
		out, err := exec.CommandContext(t.Context(), tool, append([]string{"run", "--rm", image, "sh", "-c", cmd, "sh"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", image, err, out)
		}

		if s := string(out); strings.Count(s, "pipelinegen: installing jq") != 1 || strings.Count(s, "tool: slice run --root source") != 2 || !strings.Contains(s, "jq-") {
			t.Errorf("%s:\n%s", image, s)
		}
	}
}
