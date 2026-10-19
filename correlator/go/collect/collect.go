// Package collect asks the running binaries of a batch's stack, built with
// the cover package, to write their coverage counters into a slice's
// directory, through files only: it writes a request and waits for one
// done marker per binary that said it was ready.
package collect

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The protocol's names, as the cover package writes and reads them.
const (
	EnvDir      = "PIPELINEGEN_COVER_DIR"
	requestFile = "request"
	readySuffix = ".ready"
	doneInfix   = ".done."
)

// Options say where the binaries talk and how long to wait for them.
type Options struct {
	// Dir is the directory the binaries talk through.
	Dir string
	// Stack says a stack of binaries runs: none ready is then a failure,
	// not a batch of plain test binaries that write their own coverage.
	Stack bool
	// Wait bounds the wait for every binary's answer (8 seconds when 0, under
	// the core's 10, so a silent binary is named before the core stops the call).
	Wait time.Duration
	// Stale is how long a ready file may go untouched before its binary
	// counts as exited when it doesn't answer (2 seconds when 0).
	Stale time.Duration
}

// Report says what happened to each binary.
type Report struct {
	Answered []string
	// Gone are binaries whose ready file had gone stale and that didn't
	// answer: they exited, and their ready files are removed.
	Gone []string
}

// ErrSnapshot wraps binaries that didn't hand over their counters.
var ErrSnapshot = errors.New("cover snapshot")

const pollEvery = 20 * time.Millisecond

// Snapshot asks every ready binary in o.Dir to write its counters into
// target and clear them, and waits for all of them.
func Snapshot(ctx context.Context, o Options, target string) (Report, error) {
	if o.Wait == 0 {
		o.Wait = 8 * time.Second
	}

	if o.Stale == 0 {
		o.Stale = 2 * time.Second
	}

	ready, err := readyBinaries(o.Dir, o.Stale)
	if err != nil {
		return Report{}, err
	}

	if len(ready) == 0 {
		if o.Stack {
			return Report{}, fmt.Errorf("%w: no binary is ready in %s (built with -tags pipelinegen_cover, %s set?)",
				ErrSnapshot, o.Dir, EnvDir)
		}

		return Report{}, nil
	}

	abs, err := filepath.Abs(target)
	if err != nil {
		return Report{}, fmt.Errorf("%w: %w", ErrSnapshot, err)
	}

	if err := os.MkdirAll(abs, 0o750); err != nil {
		return Report{}, fmt.Errorf("%w: %w", ErrSnapshot, err)
	}

	seq := currentSeq(o.Dir) + 1
	if err := writeFile(filepath.Join(o.Dir, requestFile), strconv.Itoa(seq)+" "+abs+"\n"); err != nil {
		return Report{}, err
	}

	answers := wait(ctx, o, ready, seq)

	return settle(o, ready, answers, seq)
}

type binary struct {
	who   string
	stale bool
}

func readyBinaries(dir string, stale time.Duration) ([]binary, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSnapshot, err)
	}

	var out []binary

	for _, e := range entries {
		who, ok := strings.CutSuffix(e.Name(), readySuffix)
		if !ok || e.IsDir() {
			continue
		}

		info, err := e.Info()
		if err != nil {
			continue // removed meanwhile
		}

		out = append(out, binary{who: who, stale: time.Since(info.ModTime()) > stale})
	}

	return out, nil
}

func currentSeq(dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, requestFile)) //nolint:gosec // the directory the stack's runner names
	if err != nil {
		return 0
	}

	s, _, _ := strings.Cut(string(b), " ")

	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}

	return n
}

func wait(ctx context.Context, o Options, ready []binary, seq int) map[string]string {
	answers := map[string]string{}
	deadline := time.Now().Add(o.Wait)

	for {
		for _, b := range ready {
			if _, ok := answers[b.who]; ok {
				continue
			}

			if a, err := os.ReadFile(doneFile(o.Dir, b.who, seq)); err == nil {
				answers[b.who] = strings.TrimSpace(string(a))
			}
		}

		if len(answers) == len(ready) || time.Now().After(deadline) || ctx.Err() != nil {
			return answers
		}

		time.Sleep(pollEvery)
	}
}

func doneFile(dir, who string, seq int) string {
	return filepath.Join(dir, who+doneInfix+strconv.Itoa(seq))
}

// settle turns the answers into the report: a binary that answered with an
// error, or a live one that didn't answer, fails the snapshot by name.
func settle(o Options, ready []binary, answers map[string]string, seq int) (Report, error) {
	var (
		r      Report
		failed []string
	)

	for _, b := range ready {
		a, ok := answers[b.who]

		switch {
		case ok && a == "ok":
			r.Answered = append(r.Answered, b.who)
		case ok:
			failed = append(failed, b.who+": "+strings.TrimPrefix(a, "error "))
		case b.stale:
			// its ready file went untouched before the request, and it
			// didn't answer within the wait: it has exited
			r.Gone = append(r.Gone, b.who)
			_ = os.Remove(filepath.Join(o.Dir, b.who+readySuffix)) //nolint:errcheck // gone either way
		default:
			failed = append(failed, b.who+": no answer within "+o.Wait.String())
		}

		_ = os.Remove(doneFile(o.Dir, b.who, seq)) //nolint:errcheck // a leftover marker harms nothing
	}

	if len(failed) > 0 {
		slices.Sort(failed)

		return r, fmt.Errorf("%w: %s", ErrSnapshot, strings.Join(failed, "; "))
	}

	return r, nil
}

// writeFile writes a file whole or not at all: binaries polling for it
// never see half of it.
func writeFile(path, content string) error {
	tmp := path + ".tmp" + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return fmt.Errorf("%w: %w", ErrSnapshot, err)
	}

	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("%w: %w", ErrSnapshot, err)
	}

	return nil
}
