package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"

	"github.com/sideways-works/pipelinegen-correlator-locale/observe"
)

func call(t *testing.T, args []string, req any) (int, []byte, string) {
	t.Helper()

	in, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer

	code := run(args, bytes.NewReader(in), &out, &errOut)

	return code, out.Bytes(), errOut.String()
}

func TestDescribe(t *testing.T) {
	d := describe()
	if d.Kind != plugin.KindCorrelator || len(d.Namespaces) != 0 || !slices.Contains(d.Capabilities, plugin.CallObserve) {
		t.Errorf("%+v", d)
	}
}

func TestRecorderWritesTheScripts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rec")

	code, out, stderr := call(t, []string{"recorder", dir}, nil)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	for _, n := range []string{"core.mjs", "vitest-setup.mjs", "playwright.mjs"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil || !strings.Contains(string(out), n) {
			t.Errorf("%s: %v", n, err)
		}
	}

	if code, _, _ := call(t, []string{"recorder"}, nil); code != 2 {
		t.Errorf("no directory: exit %d", code)
	}
}

func collectAndObserve(t *testing.T, tree, sliceDir string, claimed []string) ([]string, int, string) {
	t.Helper()

	own := filepath.Join(sliceDir, "locale-correlator")

	code, _, stderr := call(t, []string{plugin.CallCollect}, plugin.CollectRequest{
		Protocol: 1, Action: plugin.CollectSliceEnd, Slice: "s", Dir: own,
	})
	if code != 0 {
		t.Fatalf("collect: exit %d: %s", code, stderr)
	}

	var cl []plugin.Claim
	for _, p := range claimed {
		cl = append(cl, plugin.Claim{Path: p, Role: roleCatalog})
	}

	code, out, stderr := call(t, []string{plugin.CallObserve}, plugin.ObserveRequest{
		Protocol: 1, Tree: tree, Slice: "s", Claimed: cl,
		Dirs: []string{filepath.Join(sliceDir, "no-setup"), own},
	})
	if code != 0 {
		return nil, code, stderr
	}

	var ans plugin.ObserveAnswer
	if err := json.Unmarshal(out, &ans); err != nil {
		t.Fatal(err)
	}

	return ans.Touched, code, stderr
}

func TestObserveIsTheSameEveryTime(t *testing.T) {
	tree, err := filepath.Abs(filepath.Join("..", "..", "testdata", "kit"))
	if err != nil {
		t.Fatal(err)
	}

	req := plugin.ObserveRequest{
		Protocol: 1, Tree: tree, Slice: "mail/booking_test.go",
		Claimed: []plugin.Claim{{Path: "mail/locales/en.json", Role: roleCatalog}, {Path: "mail/locales/de.json", Role: roleCatalog}},
		Dirs:    []string{filepath.Join(tree, "rec/_setup/locale-correlator"), filepath.Join(tree, "rec/mail/booking_test.go/locale-correlator")},
	}

	_, a, _ := call(t, []string{plugin.CallObserve}, req)

	golden := filepath.Join("testdata", "golden", "observe.json")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(golden), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(golden, a, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(a, want) {
		t.Errorf("observe differs from %s (UPDATE_GOLDEN=1 rewrites it):\n%s", golden, a)
	}

	for range 5 {
		if _, b, _ := call(t, []string{plugin.CallObserve}, req); !bytes.Equal(a, b) {
			t.Fatal("two answers differ")
		}
	}
}

func TestDetectClaimsCatalogsOnly(t *testing.T) {
	tree, err := filepath.Abs(filepath.Join("..", "..", "testdata", "kit"))
	if err != nil {
		t.Fatal(err)
	}

	code, out, _ := call(t, []string{plugin.CallDetect}, plugin.DetectRequest{
		Protocol: 1, Tree: tree,
		Paths: []string{"mail/locales/en.json", "mail/locales/shell.json", "web/locales/en/home.json", "../x/locales/en.json", "kit/part.sh"},
	})
	if code != 0 {
		t.Fatal(code)
	}

	var ans plugin.DetectAnswer
	if err := json.Unmarshal(out, &ans); err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, c := range ans.Claims {
		got = append(got, c.Path)
	}

	if !slices.Equal(got, []string{"mail/locales/en.json", "web/locales/en/home.json"}) {
		t.Errorf("claims %v", got)
	}
}

// jsWork lays out a JavaScript fixture with the recorders and the
// node_modules LOCALE_NODE_MODULES names.
func jsWork(t *testing.T, fixture string) string {
	t.Helper()

	mods := os.Getenv("LOCALE_NODE_MODULES")
	if mods == "" {
		t.Skip("LOCALE_NODE_MODULES names no node_modules with i18next and vitest")
	}

	src, err := filepath.Abs(filepath.Join("..", "..", "testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}

	w := copyTree(t, src)

	if code, _, stderr := call(t, []string{"recorder", filepath.Join(w, "recorder")}, nil); code != 0 {
		t.Fatal(stderr)
	}

	if err := os.Symlink(mods, filepath.Join(w, "node_modules")); err != nil {
		t.Fatal(err)
	}

	if err := os.Mkdir(filepath.Join(w, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}

	return w
}

func node(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "node", args...)
	cmd.Dir = dir

	cmd.Env = append(os.Environ(), env...)

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("node %v: %v\n%s", args, err, out)
	}
}

func TestVitestSetup(t *testing.T) {
	w := jsWork(t, "js")
	node(t, w, []string{"PIPELINEGEN_LOCALE_KEYS_OUT=" + filepath.Join(w, "coverage")}, "node_modules/vitest/vitest.mjs", "run")

	claimed := []string{"app/locales/en/home.json", "app/locales/de/home.json"}

	got, code, stderr := collectAndObserve(t, w, filepath.Join(w, "coverage", "app", "greet.test.mjs"), claimed)
	if code != 0 {
		t.Fatalf("observe: exit %d: %s", code, stderr)
	}

	want := []string{
		"locale:app/locales",
		"locale:app/locales#de/home",
		"locale:app/locales#de/home:cart.items_other",
		"locale:app/locales#de/home:greeting",
		"locale:app/locales#de/home:greeting_male",
		"locale:app/locales#de/home:nothing.here",
		"locale:app/locales#de/home:steps",
		"locale:app/locales#de/home:steps.0",
		"locale:app/locales#de/home:steps.1",
		"locale:app/locales#de/home:title",
		"locale:app/locales#en/home:greeting",
		"locale:app/locales#en/home:greeting_male",
		"locale:app/locales#en/home:nothing.here",
	}
	if !slices.Equal(got, want) {
		t.Errorf("touched\n%v\nwant\n%v", got, want)
	}

	got, code, _ = collectAndObserve(t, w, filepath.Join(w, "coverage", "app", "other.test.mjs"), claimed)
	if code != 0 || len(got) != 0 {
		t.Errorf("a test of its own texts: exit %d, %v", code, got)
	}

	got, code, stderr = collectAndObserve(t, w, filepath.Join(w, "coverage", "app", "transformed.test.mjs"), claimed)
	if code != 0 || !slices.Contains(got, "locale:app/locales#de/home:title") {
		t.Errorf("a bundle a build step changed: exit %d, %v %s", code, got, stderr)
	}

	_, code, stderr = collectAndObserve(t, w, filepath.Join(w, "coverage", "app", "merged.test.mjs"), claimed)
	if code != plugin.ExitCantTell || !strings.Contains(stderr, "matches no catalog file") {
		t.Errorf("a bundle with a key added: exit %d, %s", code, stderr)
	}
}

func TestRowProbesAreI18nexts(t *testing.T) {
	w := jsWork(t, "rows")

	if err := os.WriteFile(filepath.Join(w, "vitest.config.mjs"),
		[]byte(`export default { test: { include: ["rows.test.mjs"], setupFiles: ["./recorder/vitest-setup.mjs"] } };`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for row, want := range rowProbes {
		if strings.Contains(want[0], `"c":`) {
			continue // the app hook's form, which the browser test covers
		}

		out := filepath.Join(w, "cov", row)
		node(t, w, []string{"ROW=" + row, "PIPELINEGEN_LOCALE_KEYS_OUT=" + out}, "node_modules/vitest/vitest.mjs", "run")

		files, err := filepath.Glob(filepath.Join(out, "rows.test.mjs", observe.Prefix+"*"))
		if err != nil || len(files) != 1 {
			t.Fatalf("%s: files %v", row, files)
		}

		b, err := os.ReadFile(files[0])
		if err != nil {
			t.Fatal(err)
		}

		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if got := lines[1 : len(lines)-1]; !slices.Equal(got, want) {
			t.Errorf("%s: i18next probed\n%v\nrows_test.go holds\n%v", row, got, want)
		}
	}
}

// It starts one headless Chromium; LOCALE_BROWSER=1 allows it.
func TestPlaywrightFixture(t *testing.T) {
	if os.Getenv("LOCALE_BROWSER") != "1" {
		t.Skip("LOCALE_BROWSER=1 allows a browser")
	}

	w := jsWork(t, "js")
	node(t, w, []string{"LOCALE_OUT=" + filepath.Join(w, "coverage")}, "node_modules/@playwright/test/cli.js", "test")

	claimed := []string{"app/locales/en/home.json", "app/locales/de/home.json"}

	got, code, stderr := collectAndObserve(t, w, filepath.Join(w, "coverage", "e2e", "german.spec.mjs"), claimed)
	if code != 0 {
		t.Fatalf("observe: exit %d: %s", code, stderr)
	}

	for _, id := range []string{
		"locale:app/locales", "locale:app/locales#de/home:title", "locale:app/locales#de/home:cart.items_one",
		"locale:app/locales#de/home:greeting", "locale:app/locales#en/home:greeting", "locale:app/locales#en/home:title",
	} {
		if !slices.Contains(got, id) {
			t.Errorf("no %s in %v", id, got)
		}
	}

	_, code, stderr = collectAndObserve(t, w, filepath.Join(w, "coverage", "e2e", "plain.spec.mjs"), claimed)
	if code != plugin.ExitCantTell || !strings.Contains(stderr, "without the recorder") {
		t.Errorf("the page without the hook: exit %d: %s", code, stderr)
	}
}

func TestKit(t *testing.T) {
	kit := os.Getenv("PIPELINEGEN")
	if kit == "" {
		t.Skip("PIPELINEGEN names no pipelinegen binary")
	}

	bin := filepath.Join(t.TempDir(), "locale-correlator")

	if out, err := exec.CommandContext(t.Context(), "go", "build", "-trimpath", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	tree, err := filepath.Abs(filepath.Join("..", "..", "testdata", "kit"))
	if err != nil {
		t.Fatal(err)
	}

	out, err := exec.CommandContext(t.Context(), kit, "plugin", "test", bin, "--tree", tree).CombinedOutput()
	if err != nil {
		t.Fatalf("the kit failed: %v\n%s", err, out)
	}

	t.Logf("%s", out)
}

func TestProductionBuildHoldsNoHook(t *testing.T) {
	w := jsWork(t, "vite")

	if _, err := os.Stat(filepath.Join(w, "node_modules", "vite")); err != nil {
		t.Skip("no vite in LOCALE_NODE_MODULES")
	}

	// the same app without the hook's line
	bare := jsWork(t, "vite")
	main := filepath.Join(bare, "src", "main.js")

	src, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}

	var kept []string

	for line := range strings.SplitSeq(string(src), "\n") {
		if !strings.Contains(line, "__pipelinegenLocales") {
			kept = append(kept, line)
		}
	}

	if len(kept) != strings.Count(string(src), "\n") {
		t.Fatal("the fixture has no single line with the hook")
	}

	if err := os.WriteFile(main, []byte(strings.Join(kept, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}

	node(t, w, nil, "node_modules/vite/bin/vite.js", "build", "--outDir", "dist-prod")
	node(t, bare, nil, "node_modules/vite/bin/vite.js", "build", "--outDir", "dist-prod")
	node(t, w, []string{"VITE_PIPELINEGEN_LOCALES=1"}, "node_modules/vite/bin/vite.js", "build", "--outDir", "dist-test")

	prod, without := files(t, filepath.Join(w, "dist-prod")), files(t, filepath.Join(bare, "dist-prod"))
	if len(prod) < 2 {
		t.Fatalf("the production build wrote %d files", len(prod))
	}

	if maps.Equal(prod, files(t, filepath.Join(w, "dist-test"))) {
		t.Error("the build with the flag is the production build")
	}

	if !maps.Equal(prod, without) {
		t.Errorf("the production build differs from the build without the hook:\n%v\n%v", slices.Sorted(maps.Keys(prod)), slices.Sorted(maps.Keys(without)))
	}

	check := func(dist string) error {
		return exec.CommandContext(t.Context(), "sh", filepath.Join(w, "check-build.sh"), filepath.Join(w, dist)).Run()
	}

	if err := check("dist-prod"); err != nil {
		t.Errorf("the production build: %v", err)
	}

	if err := check("dist-test"); err == nil {
		t.Error("the test build passed the check")
	}
}

func files(t *testing.T, dir string) map[string]string {
	t.Helper()

	out := map[string]string{}

	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		b, err := os.ReadFile(p) //nolint:gosec // a build's output
		if err != nil {
			return fmt.Errorf("reading the build: %w", err)
		}

		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return fmt.Errorf("naming %s: %w", p, err)
		}

		out[filepath.ToSlash(rel)] = string(b)

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return out
}

func TestLaterPartsAreTaken(t *testing.T) {
	cov := t.TempDir()
	slice := "e2e/a.spec.ts"
	write := func(name, key string) {
		t.Helper()

		dir := filepath.Join(cov, slice)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}

		text := `{"recorder":"locale-keys","version":1,"runtime":"go"}` + "\n" + `{"c":"mail/locales","l":"de","n":"","k":"` + key + `"}` + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	own1 := filepath.Join(cov, slice, "locale-uses")
	own2 := filepath.Join(cov, ".pipelinegen", "part-2", slice, "locale-uses")

	for i, own := range []string{own1, own2} {
		write("locale-keys.go-1-"+strconv.Itoa(i)+".jsonl", "part"+strconv.Itoa(i+1))

		if code, _, stderr := call(t, []string{plugin.CallCollect}, plugin.CollectRequest{
			Protocol: 1, Action: plugin.CollectSliceEnd, Slice: slice, Dir: own, Stack: true,
		}); code != 0 {
			t.Fatalf("collect: %s", stderr)
		}
	}

	code, out, stderr := call(t, []string{plugin.CallObserve}, plugin.ObserveRequest{
		Protocol: 1, Tree: t.TempDir(), Slice: slice,
		Claimed: []plugin.Claim{{Path: "mail/locales/de.json", Role: roleCatalog}},
		Dirs:    []string{filepath.Join(cov, "_setup", "locale-uses"), own1, own2},
	})
	if code != 0 {
		t.Fatalf("observe: %s", stderr)
	}

	for _, k := range []string{"part1", "part2"} {
		if !strings.Contains(string(out), "#de/-:"+k) {
			t.Errorf("no %s in %s", k, out)
		}
	}
}

func TestLogOnlyForAStack(t *testing.T) {
	cov := t.TempDir()
	cfg := map[string]any{"log": filepath.Join(cov, "locale-keys.log")}
	setup := filepath.Join(cov, "_setup", "locale-uses")
	own := filepath.Join(cov, "s", "locale-uses")

	for _, r := range []plugin.CollectRequest{
		{Protocol: 1, Config: cfg, Action: plugin.CollectSetupEnd, Slice: "_setup", Dir: setup},
		{Protocol: 1, Config: cfg, Action: plugin.CollectSliceStart, Slice: "s", Dir: own},
		{Protocol: 1, Config: cfg, Action: plugin.CollectSliceEnd, Slice: "s", Dir: own},
	} {
		if code, _, stderr := call(t, []string{plugin.CallCollect}, r); code != 0 || stderr != "" {
			t.Fatalf("%s: exit %d: %s", r.Action, code, stderr)
		}
	}

	code, out, stderr := call(t, []string{plugin.CallObserve}, plugin.ObserveRequest{
		Protocol: 1, Tree: t.TempDir(), Slice: "s", Dirs: []string{setup, own},
	})
	if code != 0 || !strings.Contains(string(out), `"touched":[]`) {
		t.Errorf("exit %d: %s %s", code, out, stderr)
	}
}

func TestStackWithoutRecorderSaysSo(t *testing.T) {
	cov := t.TempDir()
	cfg := map[string]any{"log": filepath.Join(cov, "locale-keys.log")}

	code, _, stderr := call(t, []string{plugin.CallCollect}, plugin.CollectRequest{
		Protocol: 1, Config: cfg, Action: plugin.CollectSetupEnd, Slice: "_setup", Dir: filepath.Join(cov, "_setup", "locale-uses"), Stack: true,
	})
	if code != 0 || !strings.Contains(stderr, "no process of the stack records") {
		t.Errorf("exit %d: %s", code, stderr)
	}
}
