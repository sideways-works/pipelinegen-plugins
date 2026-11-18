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

func refusal(t *testing.T, conf map[string]any) error {
	t.Helper()

	req := plugin.DriverRequest{
		Protocol: plugin.Protocol, Work: t.TempDir(), Cache: t.TempDir(), Config: conf,
		Tree: fixture(t), Slices: fixtureSlices[:1],
	}

	return Handlers(io.Discard).Run(t.Context(), req, func(plugin.DriverEvent) error { return nil })
}

func TestASettingTheDriverWouldIgnoreIsRefused(t *testing.T) {
	cases := []struct {
		conf map[string]any
		want string
	}{
		{map[string]any{"timout": "5m"}, "timout: unknown key; the keys of go-driver's settings are " +
			"timeout, cache_mb, modcache_mb, coverpkg, tags, cover_tags, flags, race, module, must_run, uncached"},
		{map[string]any{"timeout": 300}, "timeout: takes a string, not 300"},
		{map[string]any{"timeout": nil}, "timeout: has no value"},
		{map[string]any{"cache_mb": "1G"}, `cache_mb: takes a whole number, not "1G"`},
		{map[string]any{"race": "yes"}, `race: takes true or false, not "yes"`},
		{map[string]any{"tags": "integration"}, `tags: takes a list, not "integration"`},
		{map[string]any{"tags": []any{"a", 1}}, "tags[1]: takes a string, not 1"},
		{map[string]any{"must_run": map[string]any{"a/b_test.go": 1}}, `must_run["a/b_test.go"]: takes a string, not 1`},
		{map[string]any{"must_run": []any{"a/b_test.go"}}, "must_run: takes a map, not a list"},
		// go test reads -timeout 0 as no limit.
		{map[string]any{"timeout": "0s"}, `timeout "0s" is no positive duration`},
		{map[string]any{"timeout": "-5m"}, `timeout "-5m" is no positive duration`},
		{map[string]any{"modcache_mb": -1}, "modcache_mb: -1 is less than 0"},
		{map[string]any{"module": "../elsewhere"}, `module: "../elsewhere" is no directory inside the tree`},
		{map[string]any{"module": "/abs"}, `module: "/abs" is no directory inside the tree`},
		// No slice names such a file, so it would hold no file to its tests.
		{map[string]any{"must_run": map[string]any{"api/access.go": "^TestAccess$"}}, `must_run["api/access.go"]: "api/access.go" is no test file of the tree`},
		{map[string]any{"must_run": map[string]any{"/abs/a_test.go": "^TestA$"}}, `must_run["/abs/a_test.go"]: "/abs/a_test.go" is no test file of the tree`},
		{map[string]any{"must_run": map[string]any{"a_test.go": ""}}, `must_run["a_test.go"]: an empty pattern holds every test`},
	}

	for _, c := range cases {
		err := refusal(t, c.conf)
		if want := "settings: " + c.want; err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%v: %v, want %q", c.conf, err, want)
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

	d, err := describe()
	if err != nil {
		t.Fatal(err)
	}

	if described := slices.Sorted(maps.Keys(d.Settings)); !slices.Equal(slices.Sorted(slices.Values(read)), described) {
		t.Errorf("read %v, described %v", read, described)
	}
}
