package driver

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"
)

const mustRunTest = "(must_run)"

type held struct {
	file string
	re   *regexp.Regexp
}

type holds []held

func holdsOf(set map[string]string) (holds, error) {
	var out holds

	for _, file := range slices.Sorted(maps.Keys(set)) {
		place := fmt.Sprintf("must_run[%q]", file)

		// A key no slice can name would hold no file to its tests.
		switch {
		case !strings.HasSuffix(file, "_test.go") || !inTree(file) || file == "":
			return nil, fmt.Errorf("%w: %s: %q is no test file of the tree, as the slices name it (dir/name_test.go)", errSettings, place, file)
		case set[file] == "":
			return nil, fmt.Errorf("%w: %s: an empty pattern holds every test of every file; write the tests' names, or .* if every test is meant", errSettings, place)
		}

		re, err := regexp.Compile(set[file])
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", errSettings, place, err)
		}

		out = append(out, held{file: filepath.ToSlash(filepath.Clean(file)), re: re})
	}

	return out, nil
}

func filesOf(sl plugin.DriverSlice) []string {
	if len(sl.Sources) > 0 {
		return sl.Sources
	}

	return []string{sl.Name}
}

// A pattern holds the tests of its own file and of no other.
func (h holds) match(sl plugin.DriverSlice, test string) (held, bool) {
	files := filesOf(sl)

	for _, e := range h {
		if slices.Contains(files, e.file) && e.re.MatchString(test) {
			return e, true
		}
	}

	return held{}, false
}

// unheld fails a file whose must_run tests were renamed, moved or removed,
// so nothing would run them.
func (h holds) unheld(sl plugin.DriverSlice) []plugin.Failure {
	files := filesOf(sl)

	var out []plugin.Failure

	for _, e := range h {
		if !slices.Contains(files, e.file) || slices.ContainsFunc(sl.Tests, e.re.MatchString) {
			continue
		}

		out = append(out, plugin.Failure{Test: mustRunTest, Tail: fmt.Sprintf(
			"%s holds no test matching %s, and must_run says it does: a test renamed, moved or removed needs must_run changed with it\n",
			e.file, e.re)})
	}

	return out
}

func (e held) skipped(test string, out []string) plugin.Failure {
	tail := last(out)
	if tail != "" && !strings.HasSuffix(tail, "\n") {
		tail += "\n"
	}

	return plugin.Failure{Test: test, Tail: tail + fmt.Sprintf("%s in %s skipped, and must_run says it must run: %s holds %s\n", test, e.file, e.file, e.re)}
}

// sayMissing only warns: in a proposed run the setting is trusted while the
// tree is the change's, so a change that moves the file couldn't pass.
func (j *job) sayMissing() {
	for _, e := range j.set.held {
		_, err := os.Stat(filepath.Join(j.req.Tree, filepath.FromSlash(e.file)))
		if errors.Is(err, fs.ErrNotExist) {
			sayf(j.log, "go-driver: must_run names %s, and the tree has no such file: its pattern %s holds no test now; change must_run to the file's new name\n", e.file, e.re)
		}
	}
}
