// Package cover lets a long-running Go binary hand over its coverage
// counters while it runs, so that several slices of tests run against one
// started stack each get the coverage of what they ran.
//
// A binary links it only in coverage builds, through one file of its own:
//
//	//go:build pipelinegen_cover
//
//	package main
//
//	import _ "github.com/sideways-works/pipelinegen-correlator-go/cover"
//
// and is built with `-tags pipelinegen_cover -cover -covermode=atomic`.
// When PIPELINEGEN_COVER_DIR names a directory, the binary talks through
// files in it, and only through files:
//
//   - at start it writes <name>-<pid>.ready, and touches it on every poll,
//     so the caller can tell a live binary from one that has exited;
//   - every 100 ms it reads the file "request", which holds a sequence
//     number and a target directory: "7 /work/coverage/slice";
//   - for a sequence number it hasn't seen, it writes its meta-data and
//     counters into the target, clears its counters, and writes
//     <name>-<pid>.done.<seq> holding "ok" or "error <why>".
//
// Files work whatever starts the binary, including a container that shares
// the directory as a volume, and never meet a signal the program handles.
//
// A binary built without -covermode=atomic can't clear its counters: it
// answers its first request with an error and exits. The mode can't be read
// earlier, since packages register their coverage after this one's init.
package cover

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/coverage"
	"strconv"
	"strings"
	"time"
)

// The protocol's names. go-correlator's collect call speaks the other side.
const (
	EnvDir = "PIPELINEGEN_COVER_DIR"
	// EnvName names the binary in file names and errors; its program
	// name when unset.
	EnvName     = "PIPELINEGEN_COVER_NAME"
	RequestFile = "request"
	ReadySuffix = ".ready"
	// DoneInfix sits between a binary's name and the sequence number it
	// answered.
	DoneInfix = ".done."
	Poll      = 100 * time.Millisecond
)

// counterModeAtomic is the meta-data header's counter mode for
// -covermode=atomic (internal/coverage's CtrModeAtomic).
const counterModeAtomic = 3

// modeOffset is where the counter mode sits in a meta-data file's header:
// after the magic, version, length, entry count, hash and string table.
const modeOffset = 48

var ErrMode = errors.New("cover: the binary must be built with -cover -covermode=atomic")

// The package is linked for this alone: a binary built for coverage starts
// answering requests without any call of its own.
func init() { //nolint:gochecknoinits // linking the package is how a binary opts in
	dir := os.Getenv(EnvDir)
	if dir == "" {
		return
	}

	if err := start(dir, name(), os.Getpid()); err != nil {
		fmt.Fprintln(os.Stderr, "cover:", err)
		os.Exit(2)
	}
}

func name() string {
	if n := os.Getenv(EnvName); n != "" {
		return n
	}

	return filepath.Base(os.Args[0])
}

type binary struct {
	dir   string
	who   string // <name>-<pid>
	last  int
	ready string
}

func start(dir, name string, pid int) error {
	if err := os.MkdirAll(dir, 0o750); err != nil { //nolint:gosec // the directory the stack's runner names
		return fmt.Errorf("cover: %w", err)
	}

	b := &binary{dir: dir, who: name + "-" + strconv.Itoa(pid)}
	b.ready = filepath.Join(dir, b.who+ReadySuffix)

	// a request already there was meant for the binaries before this one
	if seq, _, ok := ReadRequest(dir); ok {
		b.last = seq
	}

	if err := writeFile(b.ready, b.who+"\n"); err != nil {
		return err
	}

	go b.loop()

	return nil
}

func (b *binary) loop() {
	for range time.Tick(Poll) {
		b.step()
	}
}

func (b *binary) step() {
	now := time.Now()
	if err := os.Chtimes(b.ready, now, now); errors.Is(err, fs.ErrNotExist) {
		// the caller took this binary for gone; it is back
		_ = writeFile(b.ready, b.who+"\n") //nolint:errcheck // the next poll tries again
	}

	seq, target, ok := ReadRequest(b.dir)
	if !ok || seq <= b.last {
		return
	}

	b.last = seq

	answer := "ok"

	err := checkMode()
	if err == nil {
		err = snapshot(target)
	}

	if err != nil {
		answer = "error " + strings.ReplaceAll(err.Error(), "\n", " ")
	}

	if werr := writeFile(filepath.Join(b.dir, b.who+DoneInfix+strconv.Itoa(seq)), answer+"\n"); werr != nil {
		fmt.Fprintln(os.Stderr, "cover:", werr)
	}

	if errors.Is(err, ErrMode) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func ReadRequest(dir string) (int, string, bool) {
	b, err := os.ReadFile(filepath.Join(dir, RequestFile)) //nolint:gosec // the directory the stack's runner names
	if err != nil {
		return 0, "", false
	}

	s, target, ok := strings.Cut(strings.TrimSuffix(string(b), "\n"), " ")
	if !ok || target == "" {
		return 0, "", false
	}

	seq, err := strconv.Atoi(s)
	if err != nil {
		return 0, "", false
	}

	return seq, target, true
}

// checkMode fails unless the binary's counters are atomic, the only mode
// ClearCounters accepts.
func checkMode() error {
	var meta bytes.Buffer
	if err := coverage.WriteMeta(&meta); err != nil {
		return fmt.Errorf("%w: %w", ErrMode, err)
	}

	if b := meta.Bytes(); len(b) <= modeOffset || b[modeOffset] != counterModeAtomic {
		return ErrMode
	}

	return nil
}

// snapshot puts the binary's meta-data and counters into dir, so dir can
// be read on its own, and clears the counters for the next slice.
func snapshot(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}

	if err := coverage.WriteMetaDir(dir); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}

	if err := coverage.WriteCountersDir(dir); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}

	if err := coverage.ClearCounters(); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}

	return nil
}

// writeFile writes a file whole or not at all: a reader polling for it
// never sees half of it.
func writeFile(path, content string) error {
	tmp := path + ".tmp" + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil { //nolint:gosec // a file of the protocol's directory
		return fmt.Errorf("cover: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil { //nolint:gosec // as above
		return fmt.Errorf("cover: %w", err)
	}

	return nil
}
