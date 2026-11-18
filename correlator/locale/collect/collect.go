// Package collect gathers the recorders' files into locale-correlator's
// own directory of a slice, and cuts a log that a stack's processes share
// into the part each slice wrote.
package collect

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/sideways-works/pipelinegen-plugins/correlator/locale/observe"
)

var ErrCollect = errors.New("collect")

const markFile = ".log-offset"

const partsRoot = ".pipelinegen"

// SliceDir is the slice's coverage directory, which own (the plugin's
// directory of one part) takes from. A task writes every part into
// <root>/<slice>, while the core names part k's own directory under
// <root>/.pipelinegen/part-<k>/<slice>.
func SliceDir(own, slice string) string {
	parent := filepath.Dir(filepath.Clean(own))
	p := parent

	for _, seg := range slices.Backward(strings.Split(filepath.ToSlash(slice), "/")) {
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

// Take moves the recorders' files a slice's processes left in the slice's
// coverage directory (from) into the plugin's own directory of it (dir),
// and adds collect's own file, which says the slice ended while recording.
func Take(from, dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("%w: %w", ErrCollect, err)
	}

	entries, err := os.ReadDir(from)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %w", ErrCollect, err)
	}

	for _, e := range entries {
		n := e.Name()
		if !e.Type().IsRegular() || !strings.HasPrefix(n, observe.Prefix) || !strings.HasSuffix(n, observe.Suffix) {
			continue
		}

		if err := os.Rename(filepath.Join(from, n), filepath.Join(dir, n)); err != nil {
			return fmt.Errorf("%w: %w", ErrCollect, err)
		}
	}

	return write(filepath.Join(dir, observe.Prefix+"collect"+observe.Suffix), nil)
}

const (
	header  = `{"recorder":"locale-keys","version":1,"runtime":"collect"}` + "\n"
	endLine = `{"end":true}` + "\n"
)

func write(name string, lines []byte) error {
	var b bytes.Buffer

	b.WriteString(header)
	b.Write(lines)
	b.WriteString(endLine)

	if err := os.WriteFile(name, b.Bytes(), 0o600); err != nil {
		return fmt.Errorf("%w: %w", ErrCollect, err)
	}

	return nil
}

// Mark keeps where the log ends now, where the slice's part begins.
func Mark(log, dir string) error {
	size, err := whole(log)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("%w: %w", ErrCollect, err)
	}

	if err := os.WriteFile(filepath.Join(dir, markFile), []byte(strconv.FormatInt(size, 10)), 0o600); err != nil {
		return fmt.Errorf("%w: %w", ErrCollect, err)
	}

	return nil
}

func whole(log string) (int64, error) {
	b, err := os.ReadFile(log) //nolint:gosec // the log the config names
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}

	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrCollect, err)
	}

	return int64(bytes.LastIndexByte(b, '\n') + 1), nil
}

// ErrNoRecorder is a stack's log with no recorder's header at the setup's
// end: no process of the stack records its lookups, so its slices narrow
// nothing.
var ErrNoRecorder = errors.New("no process of the stack records its catalog lookups")

// Cut writes the log's lines since the slice's mark into a part of dir. At
// the setup's end it answers ErrNoRecorder, after writing the part, when the
// log holds no recorder's header.
func Cut(log, dir string, setup bool) error {
	var from int64

	if !setup {
		b, err := os.ReadFile(filepath.Join(dir, markFile)) //nolint:gosec // the mark slice-start wrote
		if err != nil {
			return fmt.Errorf("%w: no mark of the slice's start in %s: %w", ErrCollect, dir, err)
		}

		if from, err = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err != nil {
			return fmt.Errorf("%w: %w", ErrCollect, err)
		}

		if err := os.Remove(filepath.Join(dir, markFile)); err != nil {
			return fmt.Errorf("%w: %w", ErrCollect, err)
		}
	}

	to, err := whole(log)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("%w: %w", ErrCollect, err)
	}

	lines, err := span(log, from, to)
	if err != nil {
		return err
	}

	// the processes' headers say which recorder wrote; collect's file
	// carries their records
	var kept bytes.Buffer

	headers := 0

	for line := range bytes.Lines(lines) {
		var h observe.Header
		if json.Unmarshal(line, &h) == nil && h.Recorder != "" {
			headers++

			continue
		}

		kept.Write(line)
	}

	if err := write(filepath.Join(dir, observe.Prefix+"log"+observe.Suffix), kept.Bytes()); err != nil {
		return err
	}

	if setup && headers == 0 {
		return fmt.Errorf("%w: the log %s holds no recorder's header at the setup's end (a build without the recorder, "+
			"or without PIPELINEGEN_LOCALE_KEYS_LOG); the stack's lookups narrow nothing", ErrNoRecorder, log)
	}

	return nil
}

func span(log string, from, to int64) ([]byte, error) {
	if to <= from {
		return nil, nil
	}

	f, err := os.Open(log) //nolint:gosec // the log the config names
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCollect, err)
	}
	defer f.Close() //nolint:errcheck // read only

	b := make([]byte, to-from)
	if _, err := f.ReadAt(b, from); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: %w", ErrCollect, err)
	}

	return b, nil
}
