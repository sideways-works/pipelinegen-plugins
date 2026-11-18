package graph

import (
	"slices"
	"testing"
)

func rlRoot(t *testing.T) string {
	t.Helper()

	root := t.TempDir()

	for rel, src := range map[string]string{
		"rl/go.mod":         "module example.com/rl\n\ngo 1.24\n",
		"rl/log/record.go":  "//go:build pipelinegen_cover\n\npackage log\n\nfunc Log() string { return \"rec\" }\n",
		"rl/log/release.go": "//go:build !pipelinegen_cover\n\npackage log\n\nfunc Log() string { return \"rel\" }\n",
		"rl/log/serve.go":   "package log\n\nfunc Serve() string { return Log() }\n\nfunc Other() int { return 1 }\n",
		"rl/use/use.go":     "package use\n\nimport \"example.com/rl/log\"\n\nfunc A() string { return log.Serve() }\n\nfunc B() int { return log.Other() }\n",
	} {
		write(t, root, rel, src)
	}

	return root
}

func rlSlices() []Slice {
	return []Slice{
		{Suite: "backend", Name: "serve", HasList: true, List: []string{"go:rl/use#func:A", "go:rl/log#func:Serve", "go:rl/log#func:Log"}},
		{Suite: "backend", Name: "other", HasList: true, List: []string{"go:rl/use#func:B", "go:rl/log#func:Other"}},
	}
}

func TestAFileTheBuildLeavesOut(t *testing.T) {
	both := []string{"other", "serve"}

	cases := map[string]struct {
		file, old, repl string
		want            []string
	}{
		"the compiled file, which one slice ran":   {"rl/log/record.go", `"rec"`, `"rec!"`, []string{"serve"}},
		"another compiled declaration":             {"rl/log/serve.go", "return 1", "return 2", []string{"other"}},
		"the left-out file":                        {"rl/log/release.go", `"rel"`, `"rel!"`, both},
		"a declaration only the left-out file has": {"rl/log/release.go", "\"rel\" }\n", "\"rel\" }\n\nfunc Only() {}\n", both},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			base, head := rlRoot(t), rlRoot(t)
			edit(t, head, c.file, c.old, c.repl)

			if got := bothWays(t, base, head, atLevel2(), rlSlices()); !slices.Equal(got, c.want) {
				t.Errorf("selects %v, want %v", got, c.want)
			}
		})
	}

	if p := load(t, rlRoot(t)).Package("rl/log"); p.Whole != "" {
		t.Errorf("rl/log is bound whole: %s", p.Whole)
	}
}
