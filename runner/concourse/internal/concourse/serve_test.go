package concourse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func TestServeDescribeAndPipeline(t *testing.T) {
	var out, errs bytes.Buffer

	if code := Serve([]string{plugin.CallDescribe}, strings.NewReader("{}"), &out, &errs); code != 0 {
		t.Fatalf("describe: %d %s", code, errs.String())
	}

	var d plugin.Description
	if err := json.Unmarshal(out.Bytes(), &d); err != nil || d.Kind != plugin.KindRunner {
		t.Fatalf("describe: %+v %v", d, err)
	}

	spec, root := fixture(t)

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	for _, j := range asSlice(p["jobs"]) {
		walkTasks(asMap(j), func(task M) {
			for _, c := range asSlice(asMap(task["config"])["caches"]) {
				if path := asMap(c)["path"]; d.CacheScopes[fmt.Sprint(path)] != plugin.ScopeJob {
					t.Errorf("%v %v: cache %v has scope %q", asMap(j)["name"], task["task"], path, d.CacheScopes[fmt.Sprint(path)])
				}
			}
		})
	}

	work := t.TempDir()

	req, err := json.Marshal(plugin.PipelineRequest{Protocol: plugin.Protocol, Work: work, Spec: spec, Root: root})
	if err != nil {
		t.Fatal(err)
	}

	out.Reset()

	if code := Serve([]string{plugin.CallPipeline}, bytes.NewReader(req), &out, &errs); code != 0 {
		t.Fatalf("pipeline: %d %s", code, errs.String())
	}

	data, err := os.ReadFile(filepath.Join(work, "pipeline.yml"))
	if err != nil || !strings.Contains(string(data), "name: plan-pr") {
		t.Fatalf("pipeline: %v", err)
	}
}

// A task's command reaches its step through the plugin's one program.
func TestServeRunsASteps(t *testing.T) {
	var out, errs bytes.Buffer

	if code := Serve([]string{"step", "no-such-step"}, strings.NewReader(""), &out, &errs); code != 2 || !strings.Contains(errs.String(), `no step "no-such-step"`) {
		t.Errorf("exit %d: %s", code, errs.String())
	}

	if code := Serve([]string{"no-such-call"}, strings.NewReader("{}"), &out, &errs); code == 0 {
		t.Error("an unknown call was answered")
	}
}
