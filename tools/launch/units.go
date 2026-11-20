package launch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen-plugins/tools/commitmsg"
)

// Kinds are the plugin kinds, each a directory at the root holding its plugins.
var Kinds = []string{"connector", "correlator", "driver", "runner", "sbom"}

var errVersions = errors.New("versions")

// Units are the directories, slash-separated from the root, that test and lint on their own:
// every plugin (<kind>/<name>), every template (templates/<kind>) and tools.
func Units(root string) ([]string, error) {
	var units []string

	dirs, err := filepath.Glob(filepath.Join(root, "*", "*"))
	if err != nil {
		return nil, fmt.Errorf("units: %w", err)
	}

	for _, d := range dirs {
		rel, _ := filepath.Rel(root, d) //nolint:errcheck // under root
		rel = filepath.ToSlash(rel)

		kind, _, _ := strings.Cut(rel, "/")
		if (kind == "templates" || slices.Contains(Kinds, kind)) && (exists(filepath.Join(d, "go.mod")) || exists(filepath.Join(d, "package.json"))) {
			units = append(units, rel)
		}
	}

	slices.Sort(units)

	return append(units, "tools"), nil
}

// Modules are the Go modules of a unit, slash-separated from it: its root's and each nested one's,
// fixtures under testdata left out.
func Modules(dir string) ([]string, error) {
	var mods []string

	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() && (d.Name() == "testdata" || d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".") && p != dir) {
			return filepath.SkipDir
		}

		if !d.IsDir() && d.Name() == "go.mod" {
			rel, _ := filepath.Rel(dir, filepath.Dir(p)) //nolint:errcheck // under dir
			mods = append(mods, filepath.ToSlash(rel))
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("modules: %w", err)
	}

	slices.Sort(mods)

	return mods, nil
}

func exists(p string) bool {
	_, err := os.Lstat(p)

	return err == nil
}

func (l *Launch) unit(dir string) (string, error) {
	abs := filepath.Join(l.workspace, filepath.FromSlash(dir))
	if !exists(filepath.Join(abs, "go.mod")) && !exists(filepath.Join(abs, "package.json")) {
		return "", fmt.Errorf("%s is neither a Go module nor a Node package", dir) //nolint:err113 // said once, to the person
	}

	return abs, nil
}

// Test runs the tests of the unit dir: go test in each of its Go modules, or the Node package's
// own test script after a clean install.
func (l *Launch) Test(ctx context.Context, dir string) error {
	abs, err := l.unit(dir)
	if err != nil {
		return err
	}

	sayf(l.Stdout, "== test %s\n", dir)

	if exists(filepath.Join(abs, "package.json")) {
		if err := l.npmCI(ctx, abs); err != nil {
			return err
		}

		return l.run(ctx, abs, "npm", "test")
	}

	mods, err := Modules(abs)
	if err != nil {
		return err
	}

	var errs []error

	for _, m := range mods {
		errs = append(errs, l.run(ctx, filepath.Join(abs, m), l.goTool, "test", "./..."))
	}

	return errors.Join(errs...)
}

// Lint lints the unit dir: golangci-lint with the root's .golangci.yml over each of its Go modules,
// with a cache of its own so no result of a run with another version or config is replayed, or the
// Node package's own lint script.
func (l *Launch) Lint(ctx context.Context, dir string) error {
	abs, err := l.unit(dir)
	if err != nil {
		return err
	}

	sayf(l.Stdout, "== lint %s\n", dir)

	if exists(filepath.Join(abs, "package.json")) {
		if err := l.npmCI(ctx, abs); err != nil {
			return err
		}

		return l.run(ctx, abs, "npm", "run", "lint")
	}

	cache, done, err := l.scratch("plugins-lint-cache-")
	if err != nil {
		return err
	}
	defer done()

	mods, err := Modules(abs)
	if err != nil {
		return err
	}

	var errs []error

	env := append(l.env(), "GOLANGCI_LINT_CACHE="+cache)
	config := filepath.Join(l.workspace, ".golangci.yml")

	for _, m := range mods {
		cmd := l.command(ctx, filepath.Join(abs, m), env, l.lint, "run", "--config", config, "./...")
		if err := cmd.Run(); err != nil {
			errs = append(errs, fmt.Errorf("golangci-lint in %s: %w", filepath.ToSlash(filepath.Join(dir, m)), err))
		}
	}

	return errors.Join(errs...)
}

// npmCI installs the Node package's locked dependencies, as the release build does: without their
// install scripts, and for every package of the npm workspace at the root when there is one.
func (l *Launch) npmCI(ctx context.Context, dir string) error {
	if exists(filepath.Join(l.workspace, "package.json")) {
		dir = l.workspace
	}

	return l.run(ctx, dir, "npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund")
}

// All tests or lints every unit, all of them even when one fails; the lint then runs Checks.
func (l *Launch) All(ctx context.Context, lint bool) error {
	units, err := Units(l.workspace)
	if err != nil {
		return err
	}

	if !lint {
		return l.each(units, func(dir string) error { return l.Test(ctx, dir) })
	}

	return errors.Join(l.each(units, func(dir string) error { return l.Lint(ctx, dir) }), l.Checks(ctx))
}

// Checks are the checks of the whole repository: .tool-versions and each .node-version against the
// pinned tools, pipelinegen's check of the repository and the subjects of the commits HEAD brings.
func (l *Launch) Checks(ctx context.Context) error {
	return errors.Join(l.versions(ctx), l.unitsListed(), CheckManifests(l.workspace), l.Check(ctx), l.Subjects(ctx))
}

var listedManifest = regexp.MustCompile(`(?m)^\s*-\s*manifest:\s*(\S+)\s*$`)

// CheckManifests fails unless repository-manifest.yaml lists exactly the plugins there are, each
// with its manifest.yaml, and each plugin's LICENSE is the root's byte for byte: a plugin's zip
// ships the LICENSE of its own directory, as a manifest's files can't leave it.
func CheckManifests(root string) error {
	data, err := os.ReadFile(filepath.Join(root, "repository-manifest.yaml")) //nolint:gosec // the repository's own file
	if err != nil {
		return fmt.Errorf("manifests: %w", err)
	}

	var listed []string
	for _, m := range listedManifest.FindAllStringSubmatch(string(data), -1) {
		listed = append(listed, path.Dir(m[1]))
	}

	slices.Sort(listed)

	units, err := Units(root)
	if err != nil {
		return err
	}

	plugins := slices.DeleteFunc(slices.Clone(units), func(u string) bool { return u == toolsUnit || strings.HasPrefix(u, "templates/") })

	var errs []error

	if !slices.Equal(listed, plugins) {
		errs = append(errs, fmt.Errorf("%w: repository-manifest.yaml lists %v, the plugins are %v", errManifests, listed, plugins))
	}

	license, err := os.ReadFile(filepath.Join(root, "LICENSE")) //nolint:gosec // the repository's own file
	if err != nil {
		return fmt.Errorf("manifests: %w", err)
	}

	for _, u := range units {
		if u == toolsUnit {
			continue
		}

		if !exists(filepath.Join(root, filepath.FromSlash(u), "manifest.yaml")) {
			errs = append(errs, fmt.Errorf("%w: %s has no manifest.yaml", errManifests, u))
		}

		if strings.HasPrefix(u, "templates/") {
			continue
		}

		own, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(u), "LICENSE")) //nolint:gosec // a plugin's own file
		if err != nil || !bytes.Equal(own, license) {
			errs = append(errs, fmt.Errorf("%w: %s/LICENSE is not the root's LICENSE: copy it there", errManifests, u))
		}
	}

	return errors.Join(errs...)
}

var errManifests = errors.New("manifests")

// toolsUnit is the unit of this repository's own tools, which is no plugin.
const toolsUnit = "tools"

var listed = regexp.MustCompile(`(?m)^\s*"([^"]+)",\s*$`)

// unitsListed fails unless tools/units.bzl lists exactly the plugins and templates there are: the
// root's //:checks takes their files from that list, so one missing would change unchecked.
func (l *Launch) unitsListed() error {
	data, err := os.ReadFile(filepath.Join(l.workspace, "tools", "units.bzl"))
	if err != nil {
		return fmt.Errorf("units: %w", err)
	}

	var got []string
	for _, m := range listed.FindAllStringSubmatch(string(data), -1) {
		got = append(got, m[1])
	}

	units, err := Units(l.workspace)
	if err != nil {
		return err
	}

	want := slices.DeleteFunc(units, func(u string) bool { return u == "tools" })
	if !slices.Equal(got, want) {
		return fmt.Errorf("units: tools/units.bzl lists %v, the plugins and templates are %v", got, want) //nolint:err113 // said once, to the person
	}

	return nil
}

func (l *Launch) versions(ctx context.Context) error {
	data, err := os.ReadFile(filepath.Join(l.workspace, ".tool-versions"))
	if err != nil {
		return fmt.Errorf("lint: %w", err)
	}

	pinned := map[string]string{}

	for tool, cmd := range map[string][]string{
		"golang":        {l.goTool, "env", "GOVERSION"},
		"golangci-lint": {l.lint, "version", "--short"},
		"nodejs":        {l.node, "--version"},
	} {
		if cmd[0] == "" {
			continue
		}

		out, err := l.output(ctx, cmd[0], cmd[1:]...)
		if err != nil {
			return err
		}

		pinned[tool] = strings.TrimLeft(strings.TrimPrefix(out, "go"), "v")
	}

	errs := []error{CheckVersions(string(data), pinned)}

	// a Node plugin's build is reproducible with the Node its .node-version names
	files, _ := filepath.Glob(filepath.Join(l.workspace, "*", "*", ".node-version")) //nolint:errcheck // a valid pattern
	for _, f := range files {
		named, err := os.ReadFile(f) //nolint:gosec // a plugin's own file
		if err != nil {
			return fmt.Errorf("lint: %w", err)
		}

		if pinned["nodejs"] != "" && strings.TrimPrefix(strings.TrimSpace(string(named)), "v") != pinned["nodejs"] {
			errs = append(errs, fmt.Errorf("%w: %s names %q, MODULE.bazel pins %s", errVersions, l.rel(f), strings.TrimSpace(string(named)), pinned["nodejs"]))
		}
	}

	return errors.Join(errs...)
}

func (l *Launch) output(ctx context.Context, name string, args ...string) (string, error) {
	var out bytes.Buffer

	cmd := l.command(ctx, l.workspace, l.env(), name, args...)
	cmd.Stdout = &out

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("launch: %s %s: %w", filepath.Base(name), strings.Join(args, " "), err)
	}

	return strings.TrimSpace(out.String()), nil
}

// CheckVersions fails unless toolVersions (asdf's .tool-versions) names each tool at the version
// Bazel pins for it.
func CheckVersions(toolVersions string, pinned map[string]string) error {
	named := map[string]string{}

	for line := range strings.SplitSeq(toolVersions, "\n") {
		if f := strings.Fields(line); len(f) >= 2 && !strings.HasPrefix(f[0], "#") {
			named[f[0]] = f[1]
		}
	}

	var errs []error

	for _, tool := range slices.Sorted(func(yield func(string) bool) {
		for t := range pinned {
			if !yield(t) {
				return
			}
		}
	}) {
		if named[tool] != pinned[tool] {
			errs = append(errs, fmt.Errorf("%w: .tool-versions names %s %q, MODULE.bazel pins %s", errVersions, tool, named[tool], pinned[tool]))
		}
	}

	return errors.Join(errs...)
}

// Check runs pipelinegen's check of the repository with the pipelinegen tools/go.mod pins.
func (l *Launch) Check(ctx context.Context) error {
	dir, done, err := l.scratch("plugins-check-")
	if err != nil {
		return err
	}
	defer done()

	exe, err := l.tool(ctx, dir, "github.com/sideways-works/pipelinegen/cmd/pipelinegen")
	if err != nil {
		return err
	}

	sayf(l.Stdout, "== pipelinegen check\n")

	return l.run(ctx, l.workspace, exe, "check")
}

// Subjects checks the subjects of the commits a build of HEAD brings: a merge's second parent's
// own, else HEAD alone.
func (l *Launch) Subjects(ctx context.Context) error {
	parents, err := l.output(ctx, "git", "-C", l.workspace, "rev-list", "--parents", "-n", "1", "HEAD")
	if err != nil {
		return err
	}

	revs := []string{"-1", "HEAD"}
	if len(strings.Fields(parents)) == 3 {
		revs = []string{"HEAD^1..HEAD^2"}
	}

	sayf(l.Stdout, "== commit subjects\n")

	return commitmsg.Commits(ctx, l.workspace, revs...)
}

// Hooks installs the commit-msg hook, built from tools/cmd/commit-msg, in the checkout's hooks.
func (l *Launch) Hooks(ctx context.Context) error {
	hooks, err := l.output(ctx, "git", "-C", l.workspace, "rev-parse", "--path-format=absolute", "--git-path", "hooks")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(hooks, 0o750); err != nil {
		return fmt.Errorf("hooks: %w", err)
	}

	hook := filepath.Join(hooks, "commit-msg")
	if err := l.run(ctx, filepath.Join(l.workspace, "tools"), l.goTool, "build", "-o", hook, "./cmd/commit-msg"); err != nil {
		return err
	}

	sayf(l.Stdout, "installed %s\n", hook)

	return nil
}

// Templates builds every template under templates/ and runs pipelinegen's conformance kit on it, the
// kit of the pipelinegen the template's go.mod requires (go tool pipelinegen), with its fixture,
// testdata/kit, and the config block testdata/kit/config.json when there is one.
func (l *Launch) Templates(ctx context.Context) error {
	dir, done, err := l.scratch("plugins-templates-")
	if err != nil {
		return err
	}
	defer done()

	templates, err := filepath.Glob(filepath.Join(l.workspace, "templates", "*", "go.mod"))
	if err != nil || len(templates) == 0 {
		return errors.New("templates: none under templates/") //nolint:err113 // said once, to the person
	}

	var rels []string
	for _, t := range templates {
		rels = append(rels, l.rel(filepath.Dir(t)))
	}

	return l.each(rels, func(rel string) error {
		abs := filepath.Join(l.workspace, filepath.FromSlash(rel))

		mains, err := filepath.Glob(filepath.Join(abs, "cmd", "*"))
		if err != nil || len(mains) != 1 {
			return fmt.Errorf("%s: a template holds one program, under cmd/", rel) //nolint:err113 // said once, to the person
		}

		exe := filepath.Join(dir, filepath.Base(mains[0]))

		sayf(l.Stdout, "== kit %s\n", rel)

		if err := l.run(ctx, abs, l.goTool, "build", "-o", exe, "./cmd/"+filepath.Base(mains[0])); err != nil {
			return err
		}

		args := []string{"tool", "pipelinegen", "plugin", cmdTest, exe, "--tree", filepath.Join(abs, "testdata", "kit")}
		// a runner's fixture is the config block the kit writes its pipelines with
		if config := filepath.Join(abs, "testdata", "kit", "config.json"); exists(config) {
			args = append(args, "--config", config)
		}

		return l.run(ctx, abs, l.goTool, args...)
	})
}
