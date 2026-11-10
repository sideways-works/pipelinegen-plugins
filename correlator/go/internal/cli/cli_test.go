package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	if err := os.CopyFS(filepath.Join(root, "shop"), os.DirFS(filepath.Join("..", "..", "graph", "testdata", "shop"))); err != nil {
		t.Fatal(err)
	}

	return root
}

func run(t *testing.T, name string, req map[string]any) (int, string, string) {
	t.Helper()

	in, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer

	code := Run([]string{name}, bytes.NewReader(in), &out, &errOut, "v1.2.3")

	return code, out.String(), errOut.String()
}

func request(t *testing.T, root string, extra map[string]any) map[string]any {
	t.Helper()

	req := map[string]any{"protocol": 1, "work": t.TempDir(), "cache": t.TempDir(), "tree": root}
	maps.Copy(req, extra)

	return req
}

func readLines[T any](t *testing.T, path string) []T {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // read only

	var out []T

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)

	for sc.Scan() {
		var v T
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			t.Fatal(err)
		}

		out = append(out, v)
	}

	return out
}

func TestDescribe(t *testing.T) {
	code, out, _ := run(t, "describe", nil)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}

	if strings.Contains(out, `"namespaces"`) {
		t.Errorf("a correlator owns no namespace: %s", out)
	}

	for _, want := range []string{`"kind":"correlator"`, `"version":"v1.2.3"`, `"bind":"per-set"`, `"detect":"per-file"`, `"candidates":["**/*.go","**/go.mod","**/go.sum"]`, `"name":"method-sets","off_at":5`, `"needs":["go\u003e=go.mod"]`, `"fill":{"gomodcache":{"files":["go.mod","go.sum"],"run":["go","mod","download"]}}`} {
		if !strings.Contains(out, want) {
			t.Errorf("describe %s lacks %s", out, want)
		}
	}
}

func TestBind(t *testing.T) {
	root := fixture(t)
	req := request(t, root, map[string]any{
		"narrowings": []string{"method-sets", "pure-initialisers", "referenced-new", "module-bumps"},
		"widths":     []string{"executed"},
		"slices": []map[string]any{
			{"suite": "backend", "name": "shop/orders/create_test.go", "sources": []string{"shop/orders/create_test.go"},
				"list": []string{"go:shop/orders#func:Create"}},
			{"suite": "backend", "name": "new_test.go", "sources": []string{"shop/orders/new_test.go"}},
		},
	})

	code, out, errOut := run(t, "bind", req)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}

	if !strings.Contains(out, `"files":["units.jsonl","bind.jsonl","unbound.jsonl"]`) {
		t.Errorf("answer %s", out)
	}

	work, ok := req["work"].(string)
	if !ok {
		t.Fatal("no work directory")
	}

	ids := readLines[string](t, filepath.Join(work, unitsFile))
	lines := readLines[bindLine](t, filepath.Join(work, bindFile))

	if len(lines) != 2 || lines[0].Width != "executed" || !lines[1].Everything {
		t.Fatalf("bind lines %+v", lines)
	}

	bound := map[string]string{}

	for _, u := range lines[0].Units {
		idx, okIdx := u[0].(float64)
		kind, okKind := u[1].(string)

		if !okIdx || !okKind {
			t.Fatalf("unit %v", u)
		}

		bound[ids[int(idx)]] = kind
	}

	if bound["go:shop/orders#func:Create"] != "executed" || bound["go:shop/money#func:Gross"] != "" ||
		bound["go:shop/money#const:Rate"] != "" || bound["go:shop/orders#func:TestCreate@test"] != "test" {
		t.Errorf("bound %v", bound)
	}

	unbound := readLines[unboundLine](t, filepath.Join(work, unboundFile))
	if !slices.ContainsFunc(unbound, func(u unboundLine) bool {
		return u.Group == "shop/money" && slices.Contains(u.Names, "go:shop/money#func:Unused")
	}) {
		t.Errorf("unbound %+v lacks Unused", unbound)
	}
}

func TestObserve(t *testing.T) {
	root := fixture(t)

	if code, _, _ := run(t, "observe", request(t, root, map[string]any{"dirs": []string{filepath.Join(t.TempDir(), "gone")}})); code != exitCannotTell {
		t.Errorf("missing coverage: exit %d, want %d", code, exitCannotTell)
	}

	cov := t.TempDir()

	cmd := exec.CommandContext(t.Context(), "go", "test", "-run", "TestCancel", "-coverpkg=./...",
		"-coverprofile="+filepath.Join(cov, "a.coverprofile"), "./orders")
	cmd.Dir = filepath.Join(root, "shop")

	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go test: %v\n%s", err, b)
	}

	code, out, errOut := run(t, "observe", request(t, root, map[string]any{"slice": "x", "dirs": []string{cov}}))
	if code != 0 || !strings.Contains(out, `"go:shop/orders#func:Cancel"`) {
		t.Errorf("observe: exit %d, %s %s", code, out, errOut)
	}
}

func TestCollect(t *testing.T) {
	t.Setenv("PIPELINEGEN_COVER_DIR", "")

	// a batch of plain test binaries: nothing to ask
	code, out, errOut := run(t, "collect", request(t, "", map[string]any{"action": "slice-end", "dir": t.TempDir()}))
	if code != 0 || !strings.Contains(out, `"answered":[]`) {
		t.Errorf("no stack: exit %d %s %s", code, out, errOut)
	}

	code, _, errOut = run(t, "collect", request(t, "", map[string]any{
		"action": "slice-end", "dir": t.TempDir(), "config": map[string]any{"stack": true, "cover_dir": t.TempDir()},
	}))
	if code != exitFailed || !strings.Contains(errOut, "pipelinegen_cover") {
		t.Errorf("a stack with nothing ready: exit %d %s", code, errOut)
	}

	// the core may say a stack runs in the request itself
	code, _, errOut = run(t, "collect", request(t, "", map[string]any{
		"action": "setup-end", "dir": t.TempDir(), "stack": true, "config": map[string]any{"cover_dir": t.TempDir()},
	}))
	if code != exitFailed || !strings.Contains(errOut, "pipelinegen_cover") {
		t.Errorf("stack in the request, nothing ready: exit %d %s", code, errOut)
	}

	if code, _, _ := run(t, "collect", request(t, "", map[string]any{"action": "slice-start", "dir": "x"})); code != 0 {
		t.Errorf("slice-start: exit %d", code)
	}
}

func TestWhy(t *testing.T) {
	code, out, errOut := run(t, "why", request(t, fixture(t), map[string]any{"from": "go:shop/orders#func:Create", "to": "go:shop/money#const:Rate"}))
	if code != 0 || !strings.Contains(out, `{"unit":"go:shop/money#func:Gross","note":"referenced"}`) {
		t.Errorf("why: exit %d %s %s", code, out, errOut)
	}
}

func TestDetect(t *testing.T) {
	root := fixture(t)

	code, out, errOut := run(t, "detect", request(t, root, map[string]any{"paths": []string{"shop/money/money.go", "shop/go.mod", "MODULE.bazel", "shop/gone.go"}}))
	if code != 0 || strings.TrimSpace(out) != `{"claims":[{"path":"shop/money/money.go","role":"source"},{"path":"shop/go.mod","role":"manifest"}]}` {
		t.Errorf("detect: exit %d %s %s", code, out, errOut)
	}
}

func TestBindBindsABrokenPackageWide(t *testing.T) {
	root := fixture(t)
	if err := os.WriteFile(filepath.Join(root, "shop", "money", "broken.go"), []byte("package money\n\nfunc broken() { undefinedName() }\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	req := request(t, root, map[string]any{"widths": []string{"executed"}, "slices": []map[string]any{
		{"suite": "backend", "name": "shop/orders/create_test.go", "sources": []string{"shop/orders/create_test.go"},
			"list": []string{"go:shop/orders#func:Create"}},
	}})

	code, _, errOut := run(t, "bind", req)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}

	work, ok := req["work"].(string)
	if !ok {
		t.Fatal("no work directory")
	}

	lines := readLines[bindLine](t, filepath.Join(work, bindFile))
	if len(lines) == 0 || !slices.ContainsFunc(lines, func(l bindLine) bool { return l.Everything }) {
		t.Errorf("a slice reaching the broken package isn't bound to everything: %+v", lines)
	}
}

func TestUnboundAtEveryLevel(t *testing.T) {
	root := fixture(t)
	if err := os.WriteFile(filepath.Join(root, "shop", "money", "net.go"), []byte("package money\n\nfunc Net(a Amount) Amount { return a }\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	orders := filepath.Join(root, "shop", "orders", "orders.go")

	b, err := os.ReadFile(orders)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(orders, bytes.Replace(b, []byte("money.Hook()"), []byte("money.Hook()\n\t_ = money.Net(money.Amount{})"), 1), 0o600); err != nil {
		t.Fatal(err)
	}

	req := request(t, root, map[string]any{
		"narrowings": []string{"referenced-new", "unreached-new"},
		"widths":     []string{"executed"},
		"slices": []map[string]any{{"suite": "backend", "name": "shop/orders/create_test.go", "sources": []string{"shop/orders/create_test.go"},
			"list": []string{"go:shop/orders#func:Create"}}},
	})

	if code, _, errOut := run(t, "bind", req); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}

	work, _ := req["work"].(string) //nolint:errcheck // request sets it

	for _, l := range readLines[unboundLine](t, filepath.Join(work, unboundFile)) {
		if l.Group != "shop/money" {
			continue
		}

		if !slices.Contains(l.Names, "go:shop/money#func:Unused") || slices.Contains(l.Names, "go:shop/money#func:Net") ||
			!slices.Contains(l.Narrowed["referenced-new"], "go:shop/money#func:Net") {
			t.Errorf("unbound line %+v", l)
		}

		return
	}

	t.Error("no unbound line for shop/money while unreached-new is on")
}

func TestBindCantTellAnOlderToolchain(t *testing.T) {
	root := fixture(t)
	mod := filepath.Join(root, "shop", "go.mod")

	b, err := os.ReadFile(mod)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(mod, bytes.Replace(b, []byte("go 1.24"), []byte("go 1.999"), 1), 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := run(t, "bind", request(t, root, map[string]any{"widths": []string{"executed"}, "slices": []any{}}))
	if code != exitCannotTell || strings.TrimSpace(out) != `{"cacheable":false}` ||
		!strings.Contains(errOut, "toolchain: shop/go.mod asks for go1.999, the go command run there is go1.") {
		t.Errorf("exit %d %q %q, want %d, not cacheable, naming the toolchain", code, out, errOut, exitCannotTell)
	}
}
