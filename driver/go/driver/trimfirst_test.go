package driver

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

func runCalc(t *testing.T, conf map[string]any, gocache, modcache, deadline string) (plugin.DriverEvent, string) {
	t.Helper()

	req := plugin.DriverRequest{
		Protocol: plugin.Protocol, Work: t.TempDir(), Cache: t.TempDir(), Config: conf, Tree: fixture(t),
		Env:      map[string]string{"GOCACHE": gocache, "GOMODCACHE": modcache, "GOFLAGS": "-trimpath", "GOPROXY": "off"},
		Deadline: deadline,
		Slices:   []plugin.DriverSlice{{Name: "calc/add_test.go", Sources: []string{"calc/add_test.go"}, Tests: []string{"TestAdd"}}},
	}

	var (
		log bytes.Buffer
		got plugin.DriverEvent
	)

	err := run(t.Context(), req, func(ev plugin.DriverEvent) error {
		got = ev

		return nil
	}, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}

	return got, log.String()
}

func TestTheCachesAreTrimmedBeforeTheTests(t *testing.T) {
	gocache, modcache := t.TempDir(), t.TempDir()

	cacheFile(t, gocache, "aa/old-d", 2, time.Now().Add(-48*time.Hour))
	cacheFile(t, modcache, "cache/download/example.com/m/@v/v1.zip", 2, time.Now())
	cacheFile(t, modcache, "example.com/m@v1/m.go", 1, time.Now())

	if err := os.Chmod(filepath.Join(modcache, "example.com", "m@v1"), 0o500); err != nil { //nolint:gosec // read-only, as Go leaves it
		t.Fatal(err)
	}

	ev, log := runCalc(t, map[string]any{"cache_mb": 1, "modcache_mb": 2}, gocache, modcache, "")

	if !ev.Passed {
		t.Fatalf("%+v\n%s", ev, log)
	}

	if present(gocache, "aa/old-d") || present(modcache, "example.com") || present(modcache, "cache") {
		t.Errorf("the caches over their caps were kept:\n%s", log)
	}

	if !strings.Contains(log, "go-driver: the module cache held 3 MB, over its cap of 2 MB: removed whole") ||
		!strings.Contains(log, "go-driver: the build cache held 2 MB, over its cap of 1 MB") {
		t.Errorf("the log:\n%s", log)
	}

	// Trimmed after the tests, the build cache would be back within its cap.
	if size, err := cacheSize(gocache); err != nil || size <= 1<<20 {
		t.Errorf("the build cache was trimmed after the tests ran: %d bytes, %v", size, err)
	}
}

func TestNoTrimWhenTheStepsTimeIsNearlyGone(t *testing.T) {
	gocache, modcache := t.TempDir(), t.TempDir()
	cacheFile(t, gocache, "aa/old-d", 2, time.Now().Add(-48*time.Hour))
	cacheFile(t, modcache, "example.com/m@v1/m.go", 3, time.Now())

	deadline := time.Now().Add(time.Minute + 30*time.Second).UTC().Format(time.RFC3339)
	_, log := runCalc(t, map[string]any{"cache_mb": 1, "modcache_mb": 2}, gocache, modcache, deadline)

	if !present(gocache, "aa/old-d") || !present(modcache, "example.com/m@v1/m.go") || !strings.Contains(log, "go-driver: the step's time is nearly gone: the caches are not trimmed this time") {
		t.Errorf("the caches were trimmed with the time nearly gone:\n%s", log)
	}
}

func TestACacheInsideTheTreeOrPreparedIsNeverTrimmed(t *testing.T) {
	j := &job{req: plugin.DriverRequest{Prepared: "/b/prepared", Tree: "/b/source"}, log: io.Discard}

	for dir, want := range map[string]bool{"/b/prepared/gomod": false, "/b/source/.cache": false, "/b/cache/drivers/go/gomodcache": true, "/b/prepared-not": true} {
		if got := j.trimmable(dir); got != want {
			t.Errorf("%s: trimmable %v, want %v", dir, got, want)
		}
	}
}
