package concourse

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

const privateImage = `resources:
- name: ci-image
  type: registry-image
  source: {repository: registry.example/ci, tag: "1", username: ((registry_user)), password: ((registry_password))}
`

func withImages(t *testing.T, planner bool) (plugin.Spec, string) {
	t.Helper()

	spec, _ := fixture(t)
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "root.yml"), []byte(privateImage), 0o600); err != nil {
		t.Fatal(err)
	}

	spec.CI["include"] = "root.yml"

	if planner {
		asMap(spec.CI["planner"])["prepare_image"] = "ci-image"
	}

	return spec, dir
}

func TestThePlannersPrepareImage(t *testing.T) {
	spec, root := withImages(t, true)

	p := placed(t, spec, root)

	for _, j := range asSlice(p["jobs"]) {
		job := asMap(j)
		name := fmt.Sprint(job["name"])
		isPlan := strings.HasPrefix(name, "plan-") || strings.HasPrefix(name, "sweep-")

		var gets []M

		for _, g := range asSlice(asMap(asSlice(job["plan"])[0])["in_parallel"]) {
			gets = append(gets, asMap(g))
		}

		got := map[string]M{}
		for _, g := range gets {
			got[fmt.Sprint(g["get"])] = g
		}

		if img, ok := got["ci-image"]; ok != isPlan {
			t.Errorf("%s: gets the prepare image: %v", name, ok)
		} else if ok && (img["trigger"] != nil || img["passed"] != nil || fmt.Sprint(img["tags"]) != "[planner]") {
			t.Errorf("%s: the image's get %v", name, img)
		}

		if _, ok := got["planner"]; ok == isPlan && name != "release" {
			t.Errorf("%s: gets the planner's image: %v", name, ok)
		}

		walkTasks(job, func(task M) {
			want := "planner"
			if task["task"] == "plan" {
				want = "ci-image"
			}

			if task["task"] == "report-error" {
				return // a stock image of its own
			}

			if task["image"] != want {
				t.Errorf("%s %s: runs in %v, want %s", name, task["task"], task["image"], want)
			}
		})
	}

	flyValidates(t, p)
}

func flyValidates(t *testing.T, p M) {
	t.Helper()

	fly, err := exec.LookPath("fly")
	if err != nil {
		return
	}

	file := filepath.Join(t.TempDir(), "p.yml")
	if err := os.WriteFile(file, []byte(text(t, p)), 0o600); err != nil {
		t.Fatal(err)
	}

	if out, err := exec.CommandContext(t.Context(), fly, "validate-pipeline", "--strict", "-c", file).CombinedOutput(); err != nil {
		t.Fatalf("fly: %v\n%s", err, out)
	}
}

func TestASuitesImage(t *testing.T) {
	spec, root := withImages(t, false)
	spec.Suites[suiteIndex(t, spec, "e2e")].Image = "ci-image"

	p := placed(t, spec, root)

	job := jobByName(t, p, "main-e2e")

	var body []any

	for _, st := range asSlice(job["plan"]) {
		if a := asMap(st); a["across"] != nil {
			body = asSlice(asMap(asSlice(a["do"])[0])["do"])
		}
	}

	if len(body) == 0 {
		t.Fatal("no gated work")
	}

	get := asMap(body[0])
	if get["get"] != "ci-image" || fmt.Sprint(get["tags"]) != "[browsers]" || get["trigger"] != nil || get["passed"] != nil {
		t.Errorf("the gated work begins with %v", get)
	}

	for _, g := range asSlice(asMap(asSlice(job["plan"])[0])["in_parallel"]) {
		if asMap(g)["get"] == "ci-image" {
			t.Error("the image is fetched before the gate")
		}
	}

	images := map[string]any{}

	walkFileTasks(job, func(task M) { images[fmt.Sprint(task["task"])] = task["image"] })
	walkTasks(job, func(task M) { images[fmt.Sprint(task["task"])] = task["image"] })

	for task, want := range map[string]any{
		"prepare": "ci-image", "batch": "ci-image", "whole.reconcile": "ci-image",
		"start": "planner", "batch-end": "planner", "end": "planner",
		"collect-reconcile": "planner", "whole.reconcile-ends": "planner",
	} {
		if images[task] != want {
			t.Errorf("main-e2e %s runs in %v, want %v", task, images[task], want)
		}
	}

	walkFileTasks(jobByName(t, p, "main-backend"), func(task M) {
		if task["image"] != nil {
			t.Errorf("main-backend %s runs in %v", task["task"], task["image"])
		}
	})

	if got := jobTasks(job); len(got) != 8 {
		t.Errorf("main-e2e runs %v", got)
	}

	flyValidates(t, p)
}

func TestASuiteImageThatIsThePlannersIsFetchedOnce(t *testing.T) {
	const twin = `resources:
- name: twin
  type: registry-image
  source: {repository: golang, tag: "1.27.1"}
  version: {digest: "DIGEST"}
`

	for _, tc := range []struct {
		name, root string
		shared     bool
	}{
		{"same digest", strings.Replace(twin, "DIGEST", "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 1), true},
		{"same tag", strings.Replace(twin, "  version: {digest: \"DIGEST\"}\n", "", 1), true},
		{"another digest", strings.Replace(twin, "DIGEST", "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", 1), false},
		{"another repository", strings.Replace(strings.Replace(twin, "repository: golang", "repository: node", 1), "  version: {digest: \"DIGEST\"}\n", "", 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, _ := fixture(t)
			root := t.TempDir()

			if err := os.WriteFile(filepath.Join(root, "root.yml"), []byte(tc.root), 0o600); err != nil {
				t.Fatal(err)
			}

			spec.CI["include"] = "root.yml"
			spec.Suites[suiteIndex(t, spec, "e2e")].Image = "twin"

			p, err := Generate(spec, root)
			if err != nil {
				t.Fatal(err)
			}

			job := jobByName(t, p, "main-e2e")
			gets := map[string]M{}

			var walk func(v any)

			walk = func(v any) {
				switch x := v.(type) {
				case M:
					if name, ok := x["get"].(string); ok {
						gets[name] = x
					}

					for _, c := range x {
						walk(c)
					}
				case []any:
					for _, c := range x {
						walk(c)
					}
				}
			}
			walk(job["plan"])

			images := map[string]any{}

			walkFileTasks(job, func(task M) { images[fmt.Sprint(task["task"])] = task["image"] })

			_, apart := gets["twin"]
			from := gets["planner"]["resource"]

			switch {
			case tc.shared && (apart || from != "twin" || images["batch"] != "planner"):
				t.Errorf("fetched apart %v, planner from %v, batch in %v", apart, from, images["batch"])
			case !tc.shared && (!apart || from != nil || images["batch"] != "twin"):
				t.Errorf("fetched apart %v, planner from %v, batch in %v", apart, from, images["batch"])
			}

			flyValidates(t, p)
		})
	}
}
