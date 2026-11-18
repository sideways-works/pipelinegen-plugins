package main

import (
	"bytes"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen-plugins/driver/bazel/internal/driver"
)

// batch, the Bazel and the tests' variables come from the step and the
// request, so in with they are refused.
func TestASettingTheDriverWouldIgnoreIsRefused(t *testing.T) {
	keys := "the keys of bazel-driver's settings are startup, flags, instrument, timeout"
	cases := []struct {
		conf map[string]any
		want string
	}{
		{map[string]any{"timout": 600}, "timout: unknown key; " + keys},
		{map[string]any{"batch": true}, "batch: a step's batch sits beside with, not in it: {run: bazel, batch: true, with: {...}}"},
		{map[string]any{"bazel": "/opt/bazel"}, "bazel: the driver runs the bazelisk pipelinegen hands it with the batch's tools, or else the bazel on PATH; with names no binary"},
		{map[string]any{"test_env": []any{"DB"}}, "test_env: the variables set-up steps export reach the tests by themselves; pass any other with flags: [--test_env=NAME]"},
		{map[string]any{"timeout": "10m"}, `timeout: takes a whole number, not "10m"`},
		{map[string]any{"timeout": nil}, "timeout: has no value"},
		{map[string]any{"flags": "--config=ci"}, `flags: takes a list, not "--config=ci"`},
		{map[string]any{"flags": []any{"--config=ci", 2}}, "flags[1]: takes a string, not 2"},
		{map[string]any{"timeout": 0}, "timeout: 0 is less than 1 second; leave timeout out for Bazel's own"},
		{map[string]any{"timeout": -60}, "timeout: -60 is less than 1 second; leave timeout out for Bazel's own"},
		{map[string]any{"instrument": ""}, "instrument is empty; leave it out for every package of the workspace"},
	}

	for _, c := range cases {
		in, err := json.Marshal(map[string]any{
			"work": t.TempDir(), "cache": t.TempDir(), "tree": t.TempDir(), "config": c.conf,
			// No Bazel to start: a run that got past its settings fails otherwise.
			"tools":  map[string]string{"bazelisk": "/nonexistent/bazel"},
			"slices": []any{map[string]any{"name": "//a:a_test", "label": "//a:a_test", "kind": "sh_test"}},
		})
		if err != nil {
			t.Fatal(err)
		}

		var out bytes.Buffer

		err = run(t.Context(), bytes.NewReader(in), &out)
		if want := "settings: " + c.want; err == nil || err.Error() != want {
			t.Errorf("%v: %v, want %q", c.conf, err, want)
		}

		if out.Len() > 0 {
			t.Errorf("%v: a refused run answered %q", c.conf, out.String())
		}
	}
}

func TestTheKeysReadAreTheKeysDescribed(t *testing.T) {
	var read []string

	for f := range reflect.TypeFor[driver.With]().Fields() {
		if name, _, _ := strings.Cut(f.Tag.Get("json"), ","); f.IsExported() && name != "" {
			read = append(read, name)
		}
	}

	if described := slices.Sorted(maps.Keys(describe().Settings)); !slices.Equal(slices.Sorted(slices.Values(read)), described) {
		t.Errorf("read %v, described %v", read, described)
	}
}
