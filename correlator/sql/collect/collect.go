// Package collect cuts a PostgreSQL server's JSON log into the parts each
// slice of a batch wrote, without connecting to the server; observe reads
// them through Read.
package collect

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen-plugins/correlator/sql/pglog"
)

type Config struct {
	// Log is the server's JSON log: a file, or the directory the server
	// writes its *.json files into (log_directory), whose files are read
	// in name order.
	Log string `json:"log"`
	// SetupDatabases are patterns (path.Match) of databases whose
	// statements belong to setup wherever they appear: a template the
	// tests copy their databases from is migrated inside the first test.
	SetupDatabases []string `json:"setup_databases"`
	// Databases, when set, are the only databases counted; the server's
	// maintenance database is usually left out this way.
	Databases []string `json:"databases"`
	// Quiet is how long the log must not grow before a slice's part is
	// read: the server writes its log through a separate process, a few
	// milliseconds behind the statements. Default 150ms.
	Quiet Duration `json:"quiet"`
}

type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("a duration such as \"150ms\": %w", err)
	}

	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("a duration such as \"150ms\": %w", err)
	}

	*d = Duration(v)

	return nil
}

const (
	// StartFile marks where the slice began.
	StartFile = "start.json"
	// WindowFile holds the part's window: its times and the byte ranges
	// of the log that hold its lines.
	WindowFile = "window.json"
	// LogDir holds the log's bytes kept with the recording: links to the
	// server's files in the setup's directory, or a copy of a range.
	LogDir = "log"
	// StatementsFile holds a slice's statements, one JSON object per
	// line, where a recording lists them itself.
	StatementsFile = "statements.jsonl"
	// SetupFile holds statements of setup databases logged during the slice.
	SetupFile = "setup-statements.jsonl"
	// UnconfiguredFile says collect ran without a log to read: what the
	// slice touched can't be told.
	UnconfiguredFile = "unconfigured"
	// UnboundedFile says slice-start came after the slice had already run,
	// and holds why: the statements between its start and end are not the
	// slice's.
	UnboundedFile = "unbounded"
)

// ErrUnconfigured says the config names no log. collect then succeeds
// with nothing to do and leaves UnconfiguredFile, so observe answers
// "can't tell" instead of "touched nothing".
var ErrUnconfigured = errors.New("config: log names no file or directory")

// errNotJSON is a file of the recording that isn't the JSON it should be;
// what it holds is never quoted.
var errNotJSON = errors.New("not the JSON it should be")

// ErrNoLog says the configured log holds nothing to read: collect fails,
// which errors the slice instead of recording that it ran no SQL.
var ErrNoLog = errors.New("no log to read")

// Statement is one distinct statement of a slice. Statements are told
// apart by their text alone: tests that each copy their own database run
// the same statements in many databases.
type Statement struct {
	Text   string `json:"text"`
	Count  int    `json:"count"`
	Failed bool   `json:"failed,omitempty"`
	At     Place  `json:"-"`
}

// Place is where a record is in the server's log: the file, the byte its
// range was read from, the record's line counted from there, and its
// first byte in the file. It locates a record without quoting it.
type Place struct {
	File   string
	From   int64
	Line   int
	Offset int64
}

// mark is where the log stood at a moment: the time, and the file the
// server was writing with its size and the hash of its first bytes, so a
// file emptied and written again in place is read from its beginning.
type mark struct {
	Time    time.Time `json:"time"`
	File    string    `json:"file"`
	Offset  int64     `json:"offset"`
	Head    string    `json:"head,omitempty"`
	HeadLen int64     `json:"head_len,omitempty"`
}

const headLen = 4096

// Clock lets tests decide what time it is.
type Clock func() time.Time

// Start notes where the log stands now, in dir. When the slice's part
// directory, the parent of dir, already holds a file of the slice's run,
// the slice ran before this call: the part is marked unbounded instead.
func Start(cfg Config, dir string, now Clock) error {
	if cfg.Log == "" {
		return unconfigured(dir)
	}

	m := mark{Time: now().Truncate(time.Millisecond)}

	files, err := logFiles(cfg.Log)
	if err != nil {
		return err
	}

	if len(files) > 0 {
		last := files[len(files)-1]

		info, err := os.Stat(last)
		if err != nil {
			return fmt.Errorf("reading the log: %w", err)
		}

		m.File, m.Offset = last, info.Size()

		if m.Head, m.HeadLen, err = rangeHead(last, 0, min(m.Offset, headLen)); err != nil {
			return err
		}
	}

	if why, err := ranBefore(dir, m.Time); err != nil || why != "" {
		if err != nil {
			return err
		}

		return cantTell(dir, UnboundedFile, why)
	}

	return writeJSON(filepath.Join(dir, StartFile), m)
}

// ranBefore says why the slice's part already ran when a file of the slice's
// own run is already in its part directory at slice-start, or "" when none
// is. Other collectors' directories below it are no such evidence.
func ranBefore(dir string, start time.Time) (string, error) {
	part := filepath.Dir(dir)

	ents, err := os.ReadDir(part)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("reading the slice's directory: %w", err)
	}

	for _, e := range ents {
		if !e.Type().IsRegular() {
			continue
		}

		info, err := e.Info()
		if err != nil {
			continue
		}

		return fmt.Sprintf("slice-start came after the slice had already run: %s was already in %s at slice-start "+
			"(written %s, the start %s), so the statements logged between this start and its end are another slice's. "+
			"A recording run's run step that bundles its slices without marking them (batch: true in a suite without stack: true) "+
			"starts and ends each slice only when its driver reports the end; run that step with batch: false",
			e.Name(), part, info.ModTime().UTC().Format(stampLayout), start.UTC().Format(stampLayout)), nil
	}

	return "", nil
}

const stampLayout = "2006-01-02 15:04:05.000 UTC"

// End notes the slice's window in dir: its times and the byte ranges of the
// log's files that hold it. It reads no statements, so it takes the same
// short time whatever the log's size.
func End(cfg Config, dir, slice string, now Clock) error {
	if cfg.Log == "" {
		return unconfigured(dir)
	}

	if marked(dir) {
		return nil
	}

	var m mark

	b, err := os.ReadFile(filepath.Join(dir, StartFile)) //nolint:gosec // the slice's own directory, from pipelinegen
	if err != nil {
		return fmt.Errorf("the slice has no start mark (was collect called with slice-start?): %w", err)
	}

	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("reading the start mark: %w: %s", errNotJSON, pglog.JSONKind(err))
	}

	return note(cfg, dir, setupDir(dir, slice), m, now)
}

func marked(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, UnboundedFile))

	return err == nil
}

// Setup notes everything logged before now as the setup's window: what the
// batch's setup ran before its first slice.
func Setup(cfg Config, dir string, now Clock) error {
	if cfg.Log == "" {
		return unconfigured(dir)
	}

	return note(cfg, dir, dir, mark{}, now)
}

// setupDir is the batch's setup directory of this plugin, where the log's
// files are linked once for every slice of the batch: dir is
// <coverage>/<slice>/<plugin>, or <coverage>/.pipelinegen/part-<k>/<slice>/<plugin>
// for a slice's later part. When dir is laid out otherwise, the files are
// linked into dir itself.
func setupDir(dir, slice string) string {
	part := filepath.Dir(dir)

	root, ok := strings.CutSuffix(part, string(filepath.Separator)+filepath.FromSlash(slice))
	if slice == "" || !ok {
		return dir
	}

	if parts := filepath.Dir(root); filepath.Base(parts) == ".pipelinegen" && strings.HasPrefix(filepath.Base(root), "part-") {
		root = filepath.Dir(parts)
	}

	return filepath.Join(root, "_setup", filepath.Base(dir))
}

func unconfigured(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("making %s: %w", dir, err)
	}

	if err := os.WriteFile(filepath.Join(dir, UnconfiguredFile), nil, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", UnconfiguredFile, err)
	}

	return nil
}

func cantTell(dir, marker, why string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("making %s: %w", dir, err)
	}

	if err := os.Remove(filepath.Join(dir, WindowFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", WindowFile, err)
	}

	if err := os.WriteFile(filepath.Join(dir, marker), []byte(why+"\n"), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", marker, err)
	}

	return nil
}

type window struct {
	// From and End bound the part: a line logged at From or later and
	// before End is the part's. From is zero for the setup.
	From time.Time `json:"from"`
	End  time.Time `json:"end"`
	// ReadAt is this machine's clock when the ranges were taken: a line
	// stamped later than that, by more than the log's writer can lag,
	// comes from a server whose clock runs ahead.
	ReadAt time.Time `json:"read_at"`
	Spans  []span    `json:"spans"`
}

type span struct {
	File string `json:"file"`
	// Path is where its bytes are kept with the recording, relative to
	// the part's directory: a link to the server's file, or a copy of the
	// range alone where no link can be made.
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
	At     int64  `json:"at"`
	// Head is the hash of the range's first HeadLen bytes, so a file
	// emptied and written again before observe reads it shows.
	Head    string `json:"head"`
	HeadLen int64  `json:"head_len"`
}

// note writes the part's window: every log file written since the start, up
// to its size once the log went quiet.
func note(cfg Config, dir, links string, from mark, now Clock) error {
	quiet := time.Duration(cfg.Quiet)
	if quiet <= 0 {
		quiet = 150 * time.Millisecond
	}

	// the statements of the slice were logged before it ended; the end is
	// rounded up to the log's millisecond
	end := now().Truncate(time.Millisecond).Add(time.Millisecond)

	if err := waitQuiet(cfg.Log, quiet); err != nil {
		return err
	}

	w := window{From: from.Time, End: end, ReadAt: wallClock()}

	files, err := logFiles(cfg.Log)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("making %s: %w", dir, err)
	}

	for _, f := range files {
		sp, ok, err := spanOf(f, from)
		if err != nil {
			return err
		}

		if !ok {
			continue
		}

		if sp, err = keep(sp, dir, links); err != nil {
			return err
		}

		w.Spans = append(w.Spans, sp)
	}

	// the next window starts after this one ends, never in its last
	// millisecond
	if wait := time.Until(end); wait > 0 {
		time.Sleep(wait)
	}

	return writeJSON(filepath.Join(dir, WindowFile), w)
}

func spanOf(f string, from mark) (span, bool, error) {
	info, err := os.Stat(f)
	if err != nil {
		return span{}, false, fmt.Errorf("reading the log: %w", err)
	}

	sp := span{File: f, Size: info.Size()}

	switch {
	case from.File == "":
	case f == from.File:
		sp.Offset = from.Offset

		same, err := sameHead(f, from)
		if err != nil {
			return span{}, false, err
		}

		// a log that shrank, or whose beginning is no longer what it
		// was at the start, was emptied or replaced: read it whole
		if sp.Offset > sp.Size || !same {
			sp.Offset = 0
		}
	case info.ModTime().Before(from.Time.Add(-mtimeSlack)):
		return span{}, false, nil
	}

	sp.Size -= sp.Offset
	sp.At = sp.Offset

	return sp, sp.Size > 0, nil
}

// keep makes the span's bytes part of the recording, since the observing step
// can't reach the server's log directory: a hard link in links, once per
// batch, which survives rotation, or else a copy of the range into dir.
func keep(sp span, dir, links string) (span, error) {
	info, err := os.Stat(sp.File)
	if err != nil {
		return sp, fmt.Errorf("reading the log: %w", err)
	}

	link, err := linkInto(sp.File, info, links)
	if err == nil {
		if sp.Path, err = filepath.Rel(dir, link); err != nil {
			return sp, fmt.Errorf("the log's link: %w", err)
		}
	} else {
		// a copy takes time in proportion to the window: say so, once per
		// copy, where the batch's log shows it
		sayf("sql-correlator: %s can't be linked into the coverage output (%v), so %d MB of it are copied; "+
			"a log directory inside the coverage output (config: log: coverage/<dir>) is linked instead, at no cost\n",
			sp.File, err, sp.Size>>20)

		sp.Path, err = copyRange(sp, dir)
		if err != nil {
			return sp, err
		}

		sp.Offset = 0
	}

	sp.Head, sp.HeadLen, err = rangeHead(filepath.Join(dir, sp.Path), sp.Offset, min(sp.Size, headLen))

	return sp, err
}

// linkInto links the server's file into links under a name of its own:
// the file's base name with its inode, so a name the server reuses for a
// new file gets a new link. A link already there is the same file.
func linkInto(file string, info fs.FileInfo, links string) (string, error) {
	name := filepath.Base(file)
	if ino, ok := inode(info); ok {
		name = strconv.FormatUint(ino, 10) + "-" + name
	}

	link := filepath.Join(links, LogDir, name)

	if have, err := os.Stat(link); err == nil && os.SameFile(have, info) {
		return link, nil
	}

	if err := os.MkdirAll(filepath.Dir(link), 0o750); err != nil {
		return "", fmt.Errorf("making %s: %w", filepath.Dir(link), err)
	}

	if err := linkFile(file, link); err != nil {
		return "", fmt.Errorf("linking the log: %w", err)
	}

	return link, nil
}

// copyRange copies the span's bytes alone into dir; the kernel copies
// them without passing them through this process where it can.
func copyRange(sp span, dir string) (string, error) {
	src, err := os.Open(sp.File)
	if err != nil {
		return "", fmt.Errorf("reading the log: %w", err)
	}

	defer src.Close() //nolint:errcheck // read only

	if _, err := src.Seek(sp.Offset, io.SeekStart); err != nil {
		return "", fmt.Errorf("reading the log: %w", err)
	}

	name := filepath.Join(LogDir, fmt.Sprintf("%d-%s", sp.Offset, filepath.Base(sp.File)))

	if err := os.MkdirAll(filepath.Join(dir, LogDir), 0o750); err != nil {
		return "", fmt.Errorf("making %s: %w", LogDir, err)
	}

	dst, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) //nolint:gosec // the part's own directory, from pipelinegen
	if err != nil {
		return "", fmt.Errorf("copying the log: %w", err)
	}

	_, cerr := io.Copy(dst, io.LimitReader(src, sp.Size))
	if err := errors.Join(cerr, dst.Close()); err != nil {
		return "", fmt.Errorf("copying the log: %w", err)
	}

	return name, nil
}

func rangeHead(name string, offset, n int64) (string, int64, error) {
	f, err := os.Open(name) //nolint:gosec // the log the repository names in its config, or our link to it
	if err != nil {
		return "", 0, fmt.Errorf("reading the log: %w", err)
	}

	defer f.Close() //nolint:errcheck // read only

	h := sha256.New()

	got, err := io.Copy(h, io.NewSectionReader(f, offset, n))
	if err != nil {
		return "", 0, fmt.Errorf("reading the log: %w", err)
	}

	return hex.EncodeToString(h.Sum(nil)), got, nil
}

// wallClock is this machine's clock, which the log's timestamps are
// checked against.
var wallClock = time.Now

// linkFile makes a hard link; tests take it away.
var linkFile = os.Link

// Log takes what collect says for people: the batch's log.
var Log io.Writer = os.Stderr

// sayf writes a line for people; a failed write has nobody left to tell.
func sayf(format string, a ...any) {
	if _, err := fmt.Fprintf(Log, format, a...); err != nil {
		return
	}
}

// mtimeSlack is how much older than the start a file's modification time
// may be and the file still be read: some filesystems keep it to the
// second or two. Reading more costs time only, since the window is chosen
// by each line's own timestamp.
const mtimeSlack = 2 * time.Second

func sameHead(name string, from mark) (bool, error) {
	if from.Head == "" {
		return true, nil
	}

	got, n, err := rangeHead(name, 0, from.HeadLen)
	if err != nil {
		return false, err
	}

	return n == from.HeadLen && got == from.Head, nil
}

// waitQuiet waits until the log has not grown for the quiet time, or
// gives up after twenty times as long: a server that keeps logging
// (a background job polling) never goes quiet, and the time window still
// keeps its statements out of the slice.
func waitQuiet(log string, quiet time.Duration) error {
	deadline := time.Now().Add(20 * quiet)

	last, err := logSize(log)
	if err != nil {
		return err
	}

	stable := time.Now()

	for time.Now().Before(deadline) {
		time.Sleep(quiet / 5)

		size, err := logSize(log)
		if err != nil {
			return err
		}

		if size != last {
			last, stable = size, time.Now()

			continue
		}

		if time.Since(stable) >= quiet {
			return nil
		}
	}

	return nil
}

func logSize(log string) (int64, error) {
	files, err := logFiles(log)
	if err != nil {
		return 0, err
	}

	var total int64

	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			return 0, fmt.Errorf("reading the log: %w", err)
		}

		total += info.Size()
	}

	return total, nil
}

func logFiles(log string) ([]string, error) {
	if log == "" {
		return nil, ErrUnconfigured
	}

	info, err := os.Stat(log)
	if err != nil {
		return nil, fmt.Errorf("the server's log: %w", err)
	}

	if !info.IsDir() {
		return []string{log}, nil
	}

	files, err := filepath.Glob(filepath.Join(log, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("listing the log: %w", err)
	}

	// a directory without a JSON file is a server that doesn't write its
	// log there as JSON: reading it would say every slice ran no SQL
	if len(files) == 0 {
		return nil, fmt.Errorf("%w: %s holds no .json file; the server must run with log_destination=jsonlog and write into that directory", ErrNoLog, log)
	}

	// the default file names hold the time they were opened, so name
	// order is the order they were written in
	sort.Strings(files)

	return files, nil
}

func matches(patterns []string, name string) bool {
	for _, p := range patterns {
		// a malformed pattern matches nothing; Validate reports it
		if ok, err := path.Match(p, name); err == nil && ok {
			return true
		}
	}

	return false
}

func ReadStatements(name string) ([]Statement, error) {
	b, err := os.ReadFile(name) //nolint:gosec // a file collect wrote
	if err != nil {
		return nil, fmt.Errorf("reading statements: %w", err)
	}

	var out []Statement

	dec := json.NewDecoder(strings.NewReader(string(b)))
	for n := 1; dec.More(); n++ {
		at := dec.InputOffset()

		var s Statement
		if err := dec.Decode(&s); err != nil {
			return nil, fmt.Errorf("%s, entry %d: %w: %s", name, n, errNotJSON, pglog.JSONKind(err))
		}

		s.At = Place{File: name, Line: n, Offset: at}
		out = append(out, s)
	}

	return out, nil
}

func writeJSON(name string, v any) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		return fmt.Errorf("making %s: %w", filepath.Dir(name), err)
	}

	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("writing %s: %w", name, err)
	}

	if err := os.WriteFile(name, b, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", name, err)
	}

	return nil
}
