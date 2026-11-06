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

// Install is off and no slice is asked, so an accepted step runs nothing.
func TestASettingTheDriverWouldIgnoreIsRefused(t *testing.T) {
	cases := []struct {
		conf map[string]any
		want string
	}{
		{map[string]any{"wokers": 2}, "wokers: unknown key; the keys of playwright-driver's settings are " +
			"dir, config, project, workers, install, flags"},
		{map[string]any{"workers": "2"}, `workers: takes a whole number, not "2"`},
		{map[string]any{"workers": 1.5}, "workers: takes a whole number, not 1.5"},
		{map[string]any{"flags": "--retries=2"}, `flags: takes a list, not "--retries=2"`},
		{map[string]any{"project": nil}, "project: has no value"},
		{map[string]any{"workers": 0}, "workers: 0 is less than 1; leave workers out for the config's"},
		{map[string]any{"workers": -1}, "workers: -1 is less than 1; leave workers out for the config's"},
		{map[string]any{"project": ""}, "project is empty, which runs every project; leave it out for that"},
		{map[string]any{"config": ""}, "config is empty; leave it out for Playwright's own lookup"},
		{map[string]any{"dir": "../web"}, `dir: "../web" is no directory inside the tree`},
		{map[string]any{"dir": "/web"}, `dir: "/web" is no directory inside the tree`},
	}

	for _, c := range cases {
		conf := maps.Clone(c.conf)
		conf["install"] = false

		req := plugin.DriverRequest{Protocol: plugin.Protocol, Work: t.TempDir(), Tree: t.TempDir(), Config: conf}

		err := Handlers(io.Discard).Run(t.Context(), req, func(plugin.DriverEvent) error { return nil })
		if want := "settings: " + c.want; err == nil || err.Error() != want {
			t.Errorf("%v: %v, want %q", c.conf, err, want)
		}
	}
}

func TestInstallWithNoValueIsRefused(t *testing.T) {
	_, err := settingsOf(plugin.DriverRequest{Config: map[string]any{"install": nil}})
	if want := "settings: install: has no value"; err == nil || err.Error() != want {
		t.Errorf("%v, want %q", err, want)
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
