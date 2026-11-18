package driver

import (
	"io"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func refusal(t *testing.T, verb string, conf map[string]any) error {
	t.Helper()

	h := Handlers(io.Discard)
	req := request(t, conf, "calc/calc_test.sh")

	switch verb {
	case plugin.CallPrepare:
		req.Prepared = t.TempDir()

		return h.Prepare(t.Context(), req)
	case plugin.CallSetup:
		stop := make(chan struct{})
		close(stop)

		return h.Setup(t.Context(), req, func(map[string]string) error { return nil }, stop)
	default:
		return h.Run(t.Context(), req, func(plugin.DriverEvent) error { return nil })
	}
}

func TestAnUnknownKeyIsRefusedByEveryVerb(t *testing.T) {
	top := "settings: timout: unknown key; the keys of shell-driver's settings are " +
		"script, command, functions, shell, dir, env, timeout, stop, checks, caches"

	for _, verb := range plugin.DriverVerbs {
		err := refusal(t, verb, map[string]any{"script": "touch ran", "timout": "5m"})
		if err == nil || err.Error() != top {
			t.Errorf("%s: %v, want %q", verb, err, top)
		}

		err = refusal(t, verb, cachesConf("true", map[string]any{"dir": "a", "mb": 1}, map[string]any{"dir": "b", "mb": 1, "aeg": "6h"}))
		if want := "settings: caches[1].aeg: unknown key; the keys of a cache are dir, env, mb, age, unit"; err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", verb, err, want)
		}
	}

	err := refusal(t, plugin.CallRun, checksConf(
		map[string]any{"name": "a", "run": "true"},
		map[string]any{"name": "b", "run": "true"},
		map[string]any{"name": "c", "run": "true", "timout": "5m"},
	))
	if want := "settings: checks[2].timout: unknown key; the keys of a check are name, run, timeout, need"; err == nil || err.Error() != want {
		t.Errorf("a misspelt check: %v, want %q", err, want)
	}
}

func TestAValueOfAnotherTypeIsRefused(t *testing.T) {
	cases := []struct {
		conf map[string]any
		want string
	}{
		{checksConf(map[string]any{"name": "a", "run": "true", "timeout": 5}), "checks[0].timeout: takes a string, not 5"},
		{checksConf(map[string]any{"name": "a", "run": "true", "need": "b"}), `checks[0].need: takes a list, not "b"`},
		{map[string]any{"checks": "a"}, `checks: takes a list, not "a"`},
		{map[string]any{"checks": []any{"a"}}, `checks[0]: takes a map of keys, not "a"`},
		{cachesConf("true", map[string]any{"dir": "a", "mb": "big"}), `caches[0].mb: takes a whole number, not "big"`},
		{cachesConf("true", map[string]any{"dir": "a", "mb": 1.5}), "caches[0].mb: takes a whole number, not 1.5"},
		{map[string]any{"script": "true", "env": map[string]any{"A": 1}}, `env["A"]: takes a string, not 1`},
		{map[string]any{"script": "true", "timeout": nil}, "timeout: has no value"},
		{map[string]any{"script": "true", "env": nil}, "env: has no value"},
		{map[string]any{"script": true}, "script: takes a string, not true"},
	}

	for _, c := range cases {
		err := refusal(t, plugin.CallRun, c.conf)
		if want := "settings: " + c.want; err == nil || err.Error() != want {
			t.Errorf("%v: %v, want %q", c.conf, err, want)
		}
	}
}

func TestValuesTheDriverWouldSilentlyMisuseAreRefused(t *testing.T) {
	cases := []struct {
		verb string
		conf map[string]any
		want string
	}{
		{plugin.CallRun, map[string]any{"script": " \n\t"}, "script holds nothing to run"},
		{plugin.CallSetup, map[string]any{"script": "true", "stop": "\n"}, "stop holds nothing to run"},
		{plugin.CallRun, map[string]any{"script": "true", "env": map[string]any{"A=B": "c"}}, `env["A=B"]: "A=B" is no variable's name`},
		{plugin.CallRun, map[string]any{"script": "true", "env": map[string]any{EnvTree: "x"}}, `env["PIPELINEGEN_TREE"]: PIPELINEGEN_TREE is the driver's own variable, which it sets itself`},
		{plugin.CallRun, cachesConf("true", map[string]any{"dir": "a", "mb": 1, "env": EnvWork}), "caches[0].env: PIPELINEGEN_WORK is the driver's own variable, which it sets itself"},
		{plugin.CallRun, map[string]any{
			"script": "true", "env": map[string]any{"GOCACHE": "x"},
			"caches": []any{map[string]any{"dir": "a", "mb": 1, "env": "GOCACHE"}},
		}, `env["GOCACHE"] and caches[0].env both set GOCACHE`},
		{plugin.CallRun, cachesConf("true", map[string]any{"dir": "a", "mb": 1, "env": "C"}, map[string]any{"dir": "b", "mb": 1, "env": "C"}), "caches[0].env and caches[1].env both set C"},
	}

	for _, c := range cases {
		err := refusal(t, c.verb, c.conf)
		if want := "settings: " + c.want; err == nil || err.Error() != want {
			t.Errorf("%s %v: %v, want %q", c.verb, c.conf, err, want)
		}
	}
}

func TestTheKeysReadAreTheKeysDescribed(t *testing.T) {
	var read []string

	for f := range reflect.TypeFor[Settings]().Fields() {
		if name, _, _ := strings.Cut(f.Tag.Get("json"), ","); f.IsExported() && name != "" {
			read = append(read, name)
		}
	}

	if described := slices.Sorted(maps.Keys(Describe().Settings)); !slices.Equal(slices.Sorted(slices.Values(read)), described) {
		t.Errorf("read %v, described %v", read, described)
	}
}
