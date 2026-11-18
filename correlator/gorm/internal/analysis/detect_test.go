package analysis

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalReplaces(t *testing.T) {
	root := t.TempDir()

	for name, content := range map[string]string{
		"svc/go.mod": "module svc\n\nrequire (\n\tgorm.io/gorm v1.31.2\n\tlib v0.0.0\n\tother v1.0.0\n)\n\n" +
			"replace lib => ../lib\n\nreplace other => example.com/fork v1.0.1\n",
		"lib/go.mod":   "module lib\n",
		"tools/go.mod": "module tools\n",
	} {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	mods, err := DetectModules(root)
	if err != nil || strings.Join(mods, ",") != "svc" {
		t.Fatalf("modules %v %v, want [svc]", mods, err)
	}

	dirs, err := LocalReplaces(root, mods)
	if err != nil || strings.Join(dirs, ",") != "lib" {
		t.Errorf("replaces %v %v, want [lib]", dirs, err)
	}
}
