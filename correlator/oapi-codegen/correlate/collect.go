package correlate

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Collect actions, in the order a batch sends them.
const (
	ActionSetupEnd   = "setup-end"
	ActionSliceStart = "slice-start"
	ActionSliceEnd   = "slice-end"
)

var errAction = errors.New("unknown collect action")

// Collect cuts the stack's request log at a slice's edges: at a slice's start
// the log is emptied, and at the end of setup and of each slice it is copied
// into dir as requests.txt and emptied. A batch runs one slice at a time, so
// nothing lands between the copy and the truncate. Without a log it does
// nothing.
func Collect(log, action, dir string) error {
	if log == "" {
		return nil
	}

	switch action {
	case ActionSliceStart:
		return truncate(log)
	case ActionSetupEnd, ActionSliceEnd:
		if err := copyLog(log, dir); err != nil {
			return err
		}

		return truncate(log)
	default:
		return fmt.Errorf("%w: %q", errAction, action)
	}
}

func copyLog(log, dir string) error {
	in, err := os.Open(log) //nolint:gosec // the log the config names
	if err != nil {
		// A stack that never started its log can't say what a slice
		// requested; failing errors the slice, as the protocol asks.
		return fmt.Errorf("collect: %w", err)
	}
	defer in.Close() //nolint:errcheck // read only

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("collect: %w", err)
	}

	out, err := os.Create(filepath.Join(dir, RequestsFile)) //nolint:gosec // the slice's own directory
	if err != nil {
		return fmt.Errorf("collect: %w", err)
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close() //nolint:errcheck // the copy's error is the one to report

		return fmt.Errorf("collect: %w", err)
	}

	if err := out.Close(); err != nil {
		return fmt.Errorf("collect: %w", err)
	}

	return nil
}

func truncate(log string) error {
	if err := os.Truncate(log, 0); err != nil {
		return fmt.Errorf("collect: %w", err)
	}

	return nil
}
