package driver

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Go marks a cache entry used by its modification time, at most an hour
// late.
const unusedFor = 24 * time.Hour

type cacheEntry struct {
	path string
	size int64
	used time.Time
}

const trimNeeds = time.Minute

// trimCaches runs before the first go test, so a trim never takes the
// minute kept to report.
func (j *job) trimCaches() {
	if j.set.CacheMB <= 0 && j.set.ModcacheMB <= 0 {
		return
	}

	if !j.until.IsZero() && time.Until(j.until) < trimNeeds {
		sayf(j.log, "go-driver: the step's time is nearly gone: the caches are not trimmed this time\n")

		return
	}

	j.trimBuildCache()
	j.trimModCache()
}

// trimmable excludes the tree and the prepared directory: a module cache
// there is the batch's only copy of its modules.
func (j *job) trimmable(dir string) bool {
	for _, root := range []string{j.req.Tree, j.req.Prepared} {
		if root == "" {
			continue
		}

		if rel, err := filepath.Rel(root, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			sayf(j.log, "go-driver: %s is inside %s: it is never trimmed\n", dir, root)

			return false
		}
	}

	return true
}

func (j *job) trimModCache() {
	if j.set.ModcacheMB <= 0 {
		return
	}

	dir := envValue(j.env, "GOMODCACHE")
	if dir == "" || !j.trimmable(dir) {
		return
	}

	size, err := cacheSize(dir)
	if err != nil || size <= int64(j.set.ModcacheMB)<<20 {
		return
	}

	if err := removeAll(dir); err != nil {
		sayf(j.log, "go-driver: the module cache %s held %d MB, over its cap of %d MB; removing it failed: %v\n", dir, size>>20, j.set.ModcacheMB, err)

		return
	}

	sayf(j.log, "go-driver: the module cache held %d MB, over its cap of %d MB: removed whole\n", size>>20, j.set.ModcacheMB)
}

// removeAll makes directories writable first, as Go leaves its module
// cache read-only.
func removeAll(dir string) error {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error { //nolint:errcheck // what stays read-only fails the removal, which says so
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o700) //nolint:errcheck,gosec // the removal says what it couldn't remove
		}

		return nil
	})

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("module cache: %w", err)
	}

	return nil
}

func (j *job) trimBuildCache() {
	if j.set.CacheMB <= 0 {
		return
	}

	dir := envValue(j.env, "GOCACHE")
	if dir == "" || dir == "off" || !j.trimmable(dir) {
		return
	}

	limit := int64(j.set.CacheMB) << 20

	size, err := cacheSize(dir)
	if err != nil || size <= limit {
		return
	}

	freed, err := trimTo(dir, limit, time.Now())
	if err != nil {
		sayf(j.log, "go-driver: the build cache %s held %d MB, over its cap of %d MB; trimming it failed: %v\n", dir, size>>20, j.set.CacheMB, err)

		return
	}

	sayf(j.log, "go-driver: the build cache held %d MB, over its cap of %d MB: %d MB of its least lately used entries removed\n", size>>20, j.set.CacheMB, freed>>20)
}

func envValue(env []string, name string) string {
	for _, kv := range slices.Backward(env) {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			return v
		}
	}

	return ""
}

func cacheSize(dir string) (int64, error) {
	var size int64

	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return fmt.Errorf("build cache: %w", err)
			}

			size += info.Size()
		}

		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("build cache: %w", err)
	}

	return size, nil
}

func trimTo(dir string, limit int64, now time.Time) (int64, error) {
	var (
		entries []cacheEntry
		size    int64
	)

	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !d.Type().IsRegular() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("build cache: %w", err)
		}

		entries = append(entries, cacheEntry{path: p, size: info.Size(), used: info.ModTime()})
		size += info.Size()

		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("build cache: %w", err)
	}

	slices.SortStableFunc(entries, func(a, b cacheEntry) int { return a.used.Compare(b.used) })

	var freed int64

	for _, e := range entries {
		if size-freed <= limit && now.Sub(e.used) < unusedFor {
			break
		}

		if err := os.Remove(e.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return freed, fmt.Errorf("build cache: %w", err)
		}

		freed += e.size
	}

	return freed, nil
}
