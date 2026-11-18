package driver

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cacheFile(t *testing.T, dir, rel string, n int, when time.Time) {
	t.Helper()

	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), n<<20), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
}

func present(dir, rel string) bool {
	_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))

	return err == nil
}

func TestTheBuildCacheStaysWithinItsCap(t *testing.T) {
	root := t.TempDir()
	cache, mods := filepath.Join(root, "go-build"), filepath.Join(root, "mod")
	now := time.Now()

	cacheFile(t, cache, "aa/unused-a", 1, now.Add(-49*time.Hour))
	cacheFile(t, cache, "bb/older-d", 1, now.Add(-3*time.Hour))
	cacheFile(t, cache, "cc/newer-d", 1, now.Add(-time.Hour))
	cacheFile(t, cache, "dd/newest-d", 1, now)
	cacheFile(t, mods, "example.com/m@v1/m.go", 2, now.Add(-49*time.Hour))

	var log bytes.Buffer

	j := &job{set: Settings{CacheMB: 4}, env: []string{"GOCACHE=/elsewhere", "GOCACHE=" + cache}, log: &log}

	j.trimBuildCache()

	if !present(cache, "aa/unused-a") || log.Len() != 0 {
		t.Fatalf("a cache within its cap changed: %s", log.String())
	}

	j.set.CacheMB = 3
	j.trimBuildCache()

	if present(cache, "aa/unused-a") || !present(cache, "bb/older-d") || !present(cache, "dd/newest-d") {
		t.Fatalf("over a cap of 3 MB the unused entry goes first, alone: %s", log.String())
	}

	j.set.CacheMB = 2
	j.trimBuildCache()

	if present(cache, "bb/older-d") || !present(cache, "cc/newer-d") || !present(cache, "dd/newest-d") {
		t.Fatalf("over a cap of 2 MB the least lately used goes next: %s", log.String())
	}

	if !present(mods, "example.com/m@v1/m.go") {
		t.Fatal("the module cache was touched")
	}

	if !strings.Contains(log.String(), "held 3 MB, over its cap of 2 MB: 1 MB of its least lately used entries removed") {
		t.Errorf("the log: %s", log.String())
	}
}

func TestNoCapTrimsNothing(t *testing.T) {
	cache := t.TempDir()
	cacheFile(t, cache, "aa/unused", 2, time.Now().Add(-49*time.Hour))

	(&job{env: []string{"GOCACHE=" + cache}}).trimBuildCache()
	(&job{set: Settings{CacheMB: 1}, env: []string{"GOCACHE=off"}}).trimBuildCache()

	if !present(cache, "aa/unused") {
		t.Error("a cache without a cap was trimmed")
	}
}
