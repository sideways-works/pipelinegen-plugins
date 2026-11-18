package collect

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/sideways-works/pipelinegen-plugins/correlator/sql/pglog"
)

var ErrCannotTell = errors.New("cannot tell")

// Part is what a part's window held: its own distinct statements and those
// of setup databases.
type Part struct {
	Own, Setup []Statement
}

func HasWindow(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, WindowFile))

	return err == nil
}

// Read reads the statements of the window collect noted in dir, streaming the
// log a line at a time. A statement that began before the window's start is
// no part's; a range that isn't jsonlog or changed since, or a line stamped
// after the noted clock, can't tell (ErrCannotTell).
func Read(cfg Config, dir string) (Part, error) {
	var w window

	b, err := os.ReadFile(filepath.Join(dir, WindowFile)) //nolint:gosec // the part's own directory, from pipelinegen
	if err != nil {
		return Part{}, fmt.Errorf("reading the window: %w", err)
	}

	if err := json.Unmarshal(b, &w); err != nil {
		return Part{}, fmt.Errorf("reading the window %s: %w: %s", filepath.Join(dir, WindowFile), errNotJSON, pglog.JSONKind(err))
	}

	own, setup := counter{}, counter{}

	var cur span

	keep := func(s pglog.Statement) {
		at := Place{File: cur.File, From: cur.At, Line: s.Line, Offset: cur.At + s.Offset}

		switch {
		// it began before the slice's start, as far as the log's
		// milliseconds can tell: it was running when the start was marked,
		// so it is the work of whatever ran before
		case !w.From.IsZero() && !s.Began.Add(time.Millisecond).After(w.From):
		case matches(cfg.SetupDatabases, s.Database):
			setup.add(s, at)
		case len(cfg.Databases) == 0 || matches(cfg.Databases, s.Database):
			own.add(s, at)
		}
	}

	for _, sp := range w.Spans {
		cur = sp

		latest, err := readSpan(dir, sp, w, keep)
		if err != nil {
			return Part{}, err
		}

		// a line stamped after what this machine's clock said when collect
		// noted the window comes from a server whose clock runs ahead, by
		// more than the second the log's writer can lag
		if latest.After(w.ReadAt.Add(time.Second)) {
			return Part{}, fmt.Errorf("%w: %s holds a line stamped %s, later than this machine's clock (%s) when collect noted the window: "+
				"the server's clock runs ahead of this machine's, and a slice's statements are chosen by this machine's clock. "+
				"Run the server on the machine that runs the batch, or in a container on it, so both read the same clock",
				ErrCannotTell, sp.File, latest.Format(stampLayout), w.ReadAt.UTC().Format(stampLayout))
		}
	}

	return Part{Own: own.list(), Setup: setup.list()}, nil
}

func readSpan(dir string, sp span, w window, keep func(pglog.Statement)) (time.Time, error) {
	name := filepath.Join(dir, sp.Path)

	got, n, err := rangeHead(name, sp.Offset, sp.HeadLen)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %s, kept as %s: %w", ErrCannotTell, sp.File, name, err)
	}

	if n != sp.HeadLen || got != sp.Head {
		return time.Time{}, fmt.Errorf("%w: %s, kept as %s, no longer holds at byte %d what collect noted there: "+
			"the file was emptied or written over between the slice's end and observe; keep the server's log files "+
			"until the batch has ended (log_truncate_on_rotation = off)", ErrCannotTell, sp.File, name, sp.At)
	}

	f, err := os.Open(name) //nolint:gosec // our link to the log the repository names in its config, or a copy of it
	if err != nil {
		return time.Time{}, fmt.Errorf("reading the log: %w", err)
	}

	defer f.Close() //nolint:errcheck // read only

	latest, err := pglog.Each(io.NewSectionReader(f, sp.Offset, sp.Size), w.From, w.End, keep)
	if err == nil {
		return latest, nil
	}

	where := sp.File
	if sp.At > 0 {
		where = fmt.Sprintf("%s, read from byte %d", sp.File, sp.At)
	}

	if errors.Is(err, pglog.ErrNotJSONLog) {
		return latest, fmt.Errorf("%w: the log looks cut (a line begins or ends mid-record), or isn't jsonlog: %s: %w", ErrCannotTell, where, err)
	}

	return latest, fmt.Errorf("%s: %w", where, err)
}

type counter map[key]*seen

type key struct {
	text   string
	failed bool
}

type seen struct {
	n  int
	at Place
}

func (c counter) add(s pglog.Statement, at Place) {
	k := key{s.Text, s.Failed}
	if c[k] == nil {
		c[k] = &seen{at: at}
	}

	c[k].n++
}

func (c counter) list() []Statement {
	out := make([]Statement, 0, len(c))
	for k, v := range c {
		out = append(out, Statement{Text: k.text, Count: v.n, Failed: k.failed, At: v.at})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Text != out[j].Text {
			return out[i].Text < out[j].Text
		}

		return !out[i].Failed && out[j].Failed
	})

	return out
}
