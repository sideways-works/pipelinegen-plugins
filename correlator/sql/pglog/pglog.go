// Package pglog reads the statements a PostgreSQL server logged, from its
// JSON log (log_destination = jsonlog): the plain statement log
// (log_statement = all), the plans auto_explain writes for every statement
// including those run inside functions, triggers and foreign key checks,
// and the text of statements that failed.
package pglog

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Statement is one logged statement.
type Statement struct {
	Time time.Time
	// Began is when it began as far as the line tells: a line logged at the
	// statement's end with its duration began that long before.
	Began    time.Time
	Database string
	// Line and Offset are where its record is, counted from where the
	// reader began: the line's number from 1, and its first byte.
	Line   int
	Offset int64
	Text   string
	// Failed: logged because it raised an error. Its tables count too: a
	// test that expects a constraint to refuse a row depends on the table.
	Failed bool
}

type record struct {
	Timestamp string `json:"timestamp"`
	Database  string `json:"dbname"`
	Severity  string `json:"error_severity"`
	Message   string `json:"message"`
	Statement string `json:"statement"`
}

// timeLayout is how jsonlog writes timestamps in UTC.
const timeLayout = "2006-01-02 15:04:05.000"

// ErrNotUTC says the server logs in another time zone. Go reads a zone
// abbreviation it doesn't know as UTC, which would shift every statement
// out of its slice and leave the slice touching nothing, so any zone but
// UTC is refused.
var ErrNotUTC = errors.New("the server must log in UTC (log_timezone = 'UTC')")

// ErrBadTime says a record's timestamp isn't one PostgreSQL writes.
var ErrBadTime = errors.New("a timestamp PostgreSQL doesn't write (YYYY-MM-DD HH:MM:SS.mmm UTC)")

// parseTime reads a record's timestamp. Its errors never quote it: a line
// that isn't what it should be may hold anything, a value a test used
// among it.
func parseTime(ts string) (time.Time, error) {
	local, ok := strings.CutSuffix(ts, " UTC")
	if !ok {
		return time.Time{}, ErrNotUTC
	}

	at, err := time.Parse(timeLayout, local)
	if err != nil {
		return time.Time{}, ErrBadTime
	}

	return at, nil
}

// JSONKind says what is wrong with JSON that doesn't decode, by the kind
// of the error and where, never by what it holds: Go's own messages quote
// a character of it, and a log line may hold a value a test used.
func JSONKind(err error) string {
	var (
		syntax *json.SyntaxError
		typ    *json.UnmarshalTypeError
	)

	switch {
	case errors.As(err, &syntax):
		return fmt.Sprintf("JSON syntax error at byte %d", syntax.Offset)
	case errors.As(err, &typ):
		return fmt.Sprintf("the field %s holds a JSON %s, at byte %d", typ.Field, typ.Value, typ.Offset)
	default:
		return "it ends before its JSON value does"
	}
}

// ErrNotJSONLog says a line was not a jsonlog record.
var ErrNotJSONLog = errors.New("not a PostgreSQL JSON log line (log_destination must be jsonlog)")

// maxLine bounds one record: a plan of a statement with a long text can be
// large, but never this large.
const maxLine = 64 << 20

// Read returns the statements in r logged in [from, to); a zero bound is
// open. A line that isn't a jsonlog record fails the read with
// ErrNotJSONLog: skipping it could drop a statement.
func Read(r io.Reader, from, to time.Time) ([]Statement, error) {
	w, err := Scan(r, from, to)

	return w.Statements, err
}

// Window is what Scan read.
type Window struct {
	Statements []Statement
	// Latest is the latest timestamp of any record read, in the window or
	// not: a server whose clock runs ahead of the machine's shows here.
	Latest time.Time
}

// Scan reads r as Read does, and also says the latest timestamp read.
func Scan(r io.Reader, from, to time.Time) (Window, error) {
	var out []Statement

	latest, err := Each(r, from, to, func(s Statement) { out = append(out, s) })
	if err != nil {
		return Window{}, err
	}

	return Window{Statements: out, Latest: latest}, nil
}

// Each reads r as Read does, handing each statement to f as it goes, and
// returns the latest timestamp read.
func Each(r io.Reader, from, to time.Time, f func(Statement)) (time.Time, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), maxLine)

	var (
		latest time.Time
		offset int64
	)

	for n := 1; sc.Scan(); n++ {
		line := sc.Bytes()
		at0 := offset
		offset += int64(len(line)) + 1

		if len(line) == 0 {
			continue
		}

		var rec record
		if err := json.Unmarshal(line, &rec); err != nil {
			return latest, fmt.Errorf("line %d (byte %d): %w: %s", n, at0, ErrNotJSONLog, JSONKind(err))
		}

		// every jsonlog record has one; an object without it is a piece
		// of something else
		if rec.Timestamp == "" {
			return latest, fmt.Errorf("line %d (byte %d): %w: no timestamp", n, at0, ErrNotJSONLog)
		}

		at, err := parseTime(rec.Timestamp)
		if err != nil {
			return latest, fmt.Errorf("line %d (byte %d): %w", n, at0, err)
		}

		if at.After(latest) {
			latest = at
		}

		if (!from.IsZero() && at.Before(from)) || (!to.IsZero() && !at.Before(to)) {
			continue
		}

		for _, s := range statements(rec) {
			s.Time = at
			s.Began = at.Add(-s.ran)
			s.Line, s.Offset = n, at0
			f(s.Statement)
		}
	}

	if err := sc.Err(); err != nil {
		return latest, fmt.Errorf("reading the log: %w", err)
	}

	return latest, nil
}

type timed struct {
	Statement

	ran time.Duration
}

func statements(rec record) []timed {
	var out []timed

	if text, ran, ok := messageStatement(rec.Message); ok {
		out = append(out, timed{Statement{Database: rec.Database, Text: text}, ran})
	}

	// an error's record carries the statement that raised it
	if rec.Statement != "" && rec.Severity != "LOG" {
		out = append(out, timed{Statement{Database: rec.Database, Text: rec.Statement, Failed: true}, 0})
	}

	return out
}

// messageStatement finds the statement in a LOG message: "statement: ...",
// "execute <name>: ...", "duration: ... statement: ..." or an
// auto_explain plan, and how long it ran when the message says.
func messageStatement(msg string) (string, time.Duration, bool) {
	var ran time.Duration

	if rest, ok := strings.CutPrefix(msg, "duration: "); ok {
		took, after, ok := strings.Cut(rest, "  ")
		if !ok {
			return "", 0, false
		}

		ran = duration(took)
		msg = after
	}

	text, ok := statementOf(msg)

	return text, ran, ok
}

// duration reads "250.500 ms"; anything else is no duration, and the
// statement counts as begun when it was logged.
func duration(s string) time.Duration {
	ms, ok := strings.CutSuffix(s, " ms")
	if !ok {
		return 0
	}

	v, err := strconv.ParseFloat(ms, 64)
	if err != nil || v < 0 {
		return 0
	}

	return time.Duration(v * float64(time.Millisecond))
}

func statementOf(msg string) (string, bool) {
	switch {
	case strings.HasPrefix(msg, "statement: "):
		return strings.TrimPrefix(msg, "statement: "), true
	case strings.HasPrefix(msg, "execute "):
		_, text, ok := strings.Cut(msg, ": ")

		return text, ok
	case strings.HasPrefix(msg, "plan:"):
		return planQuery(strings.TrimPrefix(msg, "plan:"))
	default:
		return "", false
	}
}

// planQuery reads the query text of an auto_explain plan in JSON format.
func planQuery(plan string) (string, bool) {
	var p struct {
		Text string `json:"Query Text"` //nolint:tagliatelle // auto_explain's own key
	}

	if err := json.Unmarshal([]byte(plan), &p); err != nil || p.Text == "" {
		return "", false
	}

	return p.Text, true
}
