package concourse

import (
	"os"
	"path/filepath"
	"testing"
)

// The fixture's placement gives the goldens' worker tags.
func TestWithoutImagesTheOutputStays(t *testing.T) {
	for name, self := range map[string]bool{"pipeline.golden.yml": false, "pipeline-self-set.golden.yml": true} {
		spec, root := fixture(t)
		spec.CI["self_set"] = self

		p, err := GenerateWith(spec, root, fixtureConfig())
		if err != nil {
			t.Fatal(err)
		}

		want, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}

		if got := text(t, p); got != string(want) {
			t.Errorf("%s: the pipeline changed without an image named", name)
		}
	}
}
