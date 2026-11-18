package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

const (
	// UnitFiles trims the least lately changed files first (Go's build cache).
	UnitFiles = "files"
	// UnitEntries trims whole top-level entries, oldest newest-file first.
	UnitEntries = "entries"
	// UnitWhole drops the whole cache, for one a partial removal corrupts, such
	// as Go's module cache.
	UnitWhole = "whole"
)

const stampName = ".pipelinegen-used"

const day = 24 * time.Hour

// trimNeeds is the time beyond the reporting minute a trim needs to start.
const trimNeeds = time.Minute

// Cache is a directory kept between builds, trimmed to its caps before the
// step's script runs.
type Cache struct {
	// Dir is relative to the driver's cache.
	Dir string `json:"dir"`
	// Env names a variable the script gets the directory's path in.
	Env string `json:"env"`
	// MB caps the size in megabytes (0: no cap).
	MB int `json:"mb"`
	// Age removes what has gone unused for longer: a Go duration or days (14d).
	Age string `json:"age"`
	// Unit defaults to UnitFiles.
	Unit string `json:"unit"`

	maxAge time.Duration
}

var errAge = errors.New("no positive duration, such as 6h or 14d")

func parseAge(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.ParseFloat(n, 64)
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("%q is %w", s, errAge)
		}

		return time.Duration(days * float64(day)), nil
	}

	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%q is %w", s, errAge)
	}

	return d, nil
}

func (*Cache) settingNoun() string { return "a cache" }

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func cacheSettings(caches []Cache) error {
	seen := map[string]bool{}

	for i := range caches {
		c := &caches[i]

		if c.Unit == "" {
			c.Unit = UnitFiles
		}

		clean := filepath.ToSlash(filepath.Clean(c.Dir))

		switch {
		case c.Dir == "":
			return fmt.Errorf("%w: caches[%d] has no dir", errSettings, i)
		case filepath.IsAbs(c.Dir) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../"):
			return fmt.Errorf("%w: caches[%d].dir: %q is no directory inside the driver's cache", errSettings, i, c.Dir)
		case seen[clean]:
			return fmt.Errorf("%w: caches[%d].dir: two caches are %q", errSettings, i, clean)
		case c.Env != "" && !envName.MatchString(c.Env):
			return fmt.Errorf("%w: caches[%d].env (%s): %q is no variable's name", errSettings, i, c.Dir, c.Env)
		case c.MB < 0:
			return fmt.Errorf("%w: caches[%d].mb (%s): %d is less than 0", errSettings, i, c.Dir, c.MB)
		case c.MB == 0 && c.Age == "":
			return fmt.Errorf("%w: caches[%d] (%s) has neither mb nor age: nothing would ever trim it", errSettings, i, c.Dir)
		case !slices.Contains([]string{UnitFiles, UnitEntries, UnitWhole}, c.Unit):
			return fmt.Errorf("%w: caches[%d].unit (%s): %q is none of files, entries and whole", errSettings, i, c.Dir, c.Unit)
		}

		for d := range seen {
			if strings.HasPrefix(clean, d+"/") || strings.HasPrefix(d, clean+"/") {
				return fmt.Errorf("%w: caches[%d].dir: caches %q and %q overlap", errSettings, i, d, clean)
			}
		}

		if c.Age != "" {
			age, err := parseAge(c.Age)
			if err != nil {
				return fmt.Errorf("%w: caches[%d].age (%s): %w", errSettings, i, c.Dir, err)
			}

			c.maxAge = age
		}

		c.Dir = clean
		seen[clean] = true
	}

	return nil
}

func cacheVars(req plugin.DriverRequest, set Settings) map[string]string {
	out := map[string]string{}

	for _, c := range set.Caches {
		if c.Env != "" {
			out[c.Env] = filepath.Join(req.Cache, filepath.FromSlash(c.Dir))
		}
	}

	return out
}

// removeAll makes directories writable first: Go's module cache is
// read-only.
var removeAll = func(path string) error {
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error { //nolint:errcheck // what can't be made writable fails the removal, which says so
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o700) //nolint:errcheck,gosec // the removal says what it couldn't remove
		}

		return nil
	})

	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove: %w", err)
	}

	return nil
}

// trimCaches runs before the script, so a trim never takes a set-up's or a
// slice's time. A failed removal is logged and the step goes on.
func trimCaches(ctx context.Context, req plugin.DriverRequest, set Settings, log io.Writer) {
	if len(set.Caches) == 0 {
		return
	}

	ctx, cancel, ok := trimTime(ctx, req)
	defer cancel()

	if !ok {
		sayf(log, "shell-driver: the step's time is nearly gone: its caches are not trimmed this time\n")

		for _, c := range set.Caches {
			if dir := makeCache(req, c, log); dir != "" && c.Unit == UnitWhole {
				stamp(dir, time.Now(), log)
			}
		}

		return
	}

	now := time.Now()

	for _, c := range set.Caches {
		dir := makeCache(req, c, log)
		if dir == "" || !firstInBatch(req, c) {
			continue
		}

		trimCache(ctx, dir, c, now, log)

		if c.Unit == UnitWhole {
			stamp(dir, now, log)
		}
	}
}

func trimTime(ctx context.Context, req plugin.DriverRequest) (context.Context, context.CancelFunc, bool) {
	if req.Deadline == "" {
		return ctx, func() {}, true
	}

	at, err := time.Parse(time.RFC3339, req.Deadline)
	if err != nil {
		// The step itself refuses a deadline it can't read.
		return ctx, func() {}, false
	}

	until := at.Add(-deadlineMargin - trimNeeds)
	if time.Until(until) <= 0 {
		return ctx, func() {}, false
	}

	ctx, cancel := context.WithDeadline(ctx, until)

	return ctx, cancel, true
}

// firstInBatch notes the trim in the work directory, which every call in the
// batch shares, so a run step trims once per batch, not once per slice.
func firstInBatch(req plugin.DriverRequest, c Cache) bool {
	if req.Work == "" {
		return true
	}

	mark := filepath.Join(req.Work, ".caches-trimmed", strings.ReplaceAll(c.Dir, "/", "%"))
	if _, err := os.Stat(mark); err == nil {
		return false
	}

	if err := os.MkdirAll(filepath.Dir(mark), 0o750); err == nil {
		_ = os.WriteFile(mark, nil, 0o600) //nolint:errcheck // without the mark the next call trims again, which is only slower
	}

	return true
}

func makeCache(req plugin.DriverRequest, c Cache, log io.Writer) string {
	dir := filepath.Join(req.Cache, filepath.FromSlash(c.Dir))

	if err := os.MkdirAll(dir, 0o750); err != nil {
		sayf(log, "shell-driver: cache %s: %v\n", c.Dir, err)

		return ""
	}

	return dir
}

func stamp(dir string, now time.Time, log io.Writer) {
	p := filepath.Join(dir, stampName)

	err := os.WriteFile(p, nil, 0o600)
	if err == nil {
		err = os.Chtimes(p, now, now)
	}

	if err != nil {
		sayf(log, "shell-driver: cache %s: its use isn't noted: %v\n", dir, err)
	}
}

type unit struct {
	path string
	size int64
	used time.Time
}

func trimCache(ctx context.Context, dir string, c Cache, now time.Time, log io.Writer) {
	units, err := unitsOf(ctx, dir, c.Unit)
	if err != nil {
		sayf(log, "shell-driver: cache %s: not trimmed: %v\n", c.Dir, err)

		return
	}

	var size int64
	for _, u := range units {
		size += u.size
	}

	slices.SortStableFunc(units, func(a, b unit) int { return a.used.Compare(b.used) })

	limit := int64(c.MB) << 20

	var (
		freed   int64
		removed int
		failed  []string
	)

	for _, u := range units {
		old := c.maxAge > 0 && now.Sub(u.used) > c.maxAge
		over := c.MB > 0 && size-freed > limit

		if !old && !over {
			break
		}

		if ctx.Err() != nil {
			failed = append(failed, "stopped: the step's time is nearly gone")

			break
		}

		if err := removeAll(u.path); err != nil {
			failed = append(failed, err.Error())

			continue
		}

		freed += u.size
		removed++
	}

	sayf(log, "shell-driver: cache %s: %d MB%s; %d MB in %d %s removed\n",
		c.Dir, size>>20, capsOf(c), freed>>20, removed, unitWord(c.Unit, removed))

	if len(failed) > 0 {
		sayf(log, "shell-driver: cache %s: %d not removed; first: %s\n", c.Dir, len(failed), failed[0])
	}
}

func capsOf(c Cache) string {
	var caps []string
	if c.MB > 0 {
		caps = append(caps, fmt.Sprintf("cap %d MB", c.MB))
	}

	if c.Age != "" {
		caps = append(caps, "unused for "+c.Age+" goes")
	}

	return " (" + strings.Join(caps, ", ") + ")"
}

func unitWord(u string, n int) string {
	word := map[string]string{UnitFiles: "file", UnitEntries: "entry", UnitWhole: "whole cache"}[u]
	if n == 1 {
		return word
	}

	if u == UnitEntries {
		return "entries"
	}

	return word + "s"
}

// unitsOf dates an entry by its newest file or directory, so a script that
// touches an entry it reused marks it used. A whole cache goes by its stamp.
func unitsOf(ctx context.Context, dir, kind string) ([]unit, error) {
	switch kind {
	case UnitFiles:
		var out []unit

		err := walkFiles(ctx, dir, func(p string, info fs.FileInfo) {
			if info.Mode().IsRegular() {
				out = append(out, unit{path: p, size: info.Size(), used: info.ModTime()})
			}
		})

		return out, err
	case UnitEntries:
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("cache: %w", err)
		}

		out := make([]unit, 0, len(entries))

		for _, e := range entries {
			u, err := summed(ctx, filepath.Join(dir, e.Name()))
			if err != nil {
				return nil, err
			}

			out = append(out, u)
		}

		return out, nil
	default:
		u, err := summed(ctx, dir)
		if err != nil {
			return nil, err
		}

		if info, err := os.Stat(filepath.Join(dir, stampName)); err == nil {
			u.used = info.ModTime()
		}

		if u.size == 0 {
			return nil, nil
		}

		return []unit{u}, nil
	}
}

func summed(ctx context.Context, path string) (unit, error) {
	u := unit{path: path}

	err := walkFiles(ctx, path, func(_ string, info fs.FileInfo) {
		if info.Mode().IsRegular() {
			u.size += info.Size()
		}

		if info.ModTime().After(u.used) {
			u.used = info.ModTime()
		}
	})

	return u, err
}

func walkFiles(ctx context.Context, root string, fn func(string, fs.FileInfo)) error {
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		info, err := d.Info()
		if err != nil {
			return err //nolint:wrapcheck // wrapped once below
		}

		fn(p, info)

		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("cache: %w", err)
	}

	return nil
}
