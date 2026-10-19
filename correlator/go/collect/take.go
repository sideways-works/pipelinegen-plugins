package collect

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// covdir is the subdirectory a task writes a GOCOVERDIR into.
const covdir = "gocoverdir"

// ErrTake wraps coverage that couldn't be moved into the plugin's own
// directory.
var ErrTake = errors.New("take coverage")

// Take moves the Go coverage a task wrote into a slice's directory into own,
// the plugin's part of it: text coverprofiles and GOCOVERDIR files at the top
// of from and in gocoverdir/. Other files stay. It returns the files it
// moved, relative to from.
func Take(from, own string) ([]string, error) {
	var moved []string

	for _, sub := range []string{"", covdir} {
		entries, err := os.ReadDir(filepath.Join(from, sub))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}

		if err != nil {
			return moved, fmt.Errorf("%w: %w", ErrTake, err)
		}

		for _, e := range entries {
			if !e.Type().IsRegular() {
				continue
			}

			src := filepath.Join(from, sub, e.Name())

			dst, ok := target(src, own, e.Name())
			if !ok {
				continue
			}

			if err := move(src, dst); err != nil {
				return moved, err
			}

			moved = append(moved, filepath.ToSlash(filepath.Join(sub, e.Name())))
		}
	}

	return moved, nil
}

func target(src, own, name string) (string, bool) {
	switch {
	case strings.HasPrefix(name, "covmeta.") || strings.HasPrefix(name, "covcounters."):
		return filepath.Join(own, covdir, name), true
	case strings.HasSuffix(name, ".coverprofile") && profile(src):
		return filepath.Join(own, name), true
	default:
		return "", false
	}
}

func profile(p string) bool {
	f, err := os.Open(p) //nolint:gosec // a file of the slice's coverage directory
	if err != nil {
		return false
	}
	defer f.Close() //nolint:errcheck // read only

	line, err := bufio.NewReader(f).ReadString('\n')
	if err != nil && line == "" {
		return false
	}

	return strings.HasPrefix(line, "mode:")
}

// move renames src to dst. A file of the same name already there (an
// earlier part of the slice) keeps its place: the same content makes src
// a duplicate, other content gets a name of its own.
func move(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return fmt.Errorf("%w: %w", ErrTake, err)
	}

	orig := dst

	for n := 2; ; n++ {
		if _, err := os.Lstat(dst); errors.Is(err, fs.ErrNotExist) {
			break
		}

		if same(src, dst) {
			if err := os.Remove(src); err != nil {
				return fmt.Errorf("%w: %w", ErrTake, err)
			}

			return nil
		}

		dst = numbered(orig, n)
	}

	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("%w: %w", ErrTake, err)
	}

	return nil
}

// numbered gives a clashing name a number that keeps what readers match
// on: a coverprofile keeps its suffix, a counter file its
// covcounters.<hash>.<pid>.<time> shape (the number joins the time's
// digits; a name go tool covdata can't parse would be skipped as an
// orphan), any other file its prefix.
func numbered(dst string, n int) string {
	dir, name := filepath.Split(dst)

	switch {
	case strings.HasSuffix(name, ".coverprofile"):
		return filepath.Join(dir, strings.TrimSuffix(name, ".coverprofile")+"~"+strconv.Itoa(n)+".coverprofile")
	case strings.HasPrefix(name, "covcounters."):
		return filepath.Join(dir, name+strconv.Itoa(n))
	default:
		return filepath.Join(dir, name+"~"+strconv.Itoa(n))
	}
}

func same(a, b string) bool {
	x, err1 := os.ReadFile(a) //nolint:gosec // files of the slice's coverage directory
	y, err2 := os.ReadFile(b) //nolint:gosec // as above

	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

// partsRoot is the directory, inside a batch's coverage directory, where
// the core keeps a slice's later parts.
const partsRoot = ".pipelinegen"

// SliceDir is where a task wrote the coverage that own, the plugin's
// directory for one part of a slice, takes. A task writes every part into
// <root>/<slice>, while the core names part k's own directory under
// <root>/.pipelinegen/part-<k>/<slice>.
func SliceDir(own, slice string) string {
	parent := filepath.Dir(filepath.Clean(own))
	segs := strings.Split(filepath.ToSlash(slice), "/")

	p := parent

	for _, seg := range slices.Backward(segs) {
		if seg == "" {
			continue
		}

		if filepath.Base(p) != seg {
			return parent
		}

		p = filepath.Dir(p)
	}

	k, ok := strings.CutPrefix(filepath.Base(p), "part-")
	if n, err := strconv.Atoi(k); !ok || err != nil || n < 2 || filepath.Base(filepath.Dir(p)) != partsRoot {
		return parent
	}

	return filepath.Join(filepath.Dir(filepath.Dir(p)), filepath.FromSlash(slice))
}
