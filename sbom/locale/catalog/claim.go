package catalog

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Claimable tells whether a tree path is a regular file laid out as a catalog
// whose content is a catalog or no JSON at all. A file that isn't JSON is
// claimed so its units fail loudly; JSON of another shape stays unclaimed.
func Claimable(tree, p string) (bool, error) {
	if _, ok := Locate(p); !ok {
		return false, nil
	}

	full := filepath.Join(tree, filepath.FromSlash(p))

	info, err := os.Lstat(full)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("reading %s: %w", p, err)
	}

	if !info.Mode().IsRegular() {
		return false, nil
	}

	b, err := os.ReadFile(full) //nolint:gosec // a file of the tree asked about
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", p, err)
	}

	_, err = Parse(b)

	return !errors.Is(err, ErrNotCatalog), nil
}
