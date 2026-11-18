// Package gorecord records which keys of a message catalog a Go process
// looked up, for locale-correlator. Call it from a file only recording builds
// compile (a build tag), through the one function the lookups go through.
//
// It writes nothing unless the environment names where:
// PIPELINEGEN_LOCALE_KEYS_DIR, a directory the process writes a file of its
// own into, or PIPELINEGEN_LOCALE_KEYS_LOG, a file a stack's processes share.
// A line is written before the call returns, so a killed process loses
// nothing.
package gorecord

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	EnvDir = "PIPELINEGEN_LOCALE_KEYS_DIR"
	EnvLog = "PIPELINEGEN_LOCALE_KEYS_LOG"
)

// Version is the version of the record's lines.
const Version = 1

type header struct {
	Recorder string `json:"recorder"`
	Version  int    `json:"version"`
	Runtime  string `json:"runtime"`
	PID      int    `json:"pid"`
}

// Line is one record: a lookup of a key (Key, which may be "" for the
// whole namespace), a check that a namespace (Has) or a language (Lang)
// is there, or a catalog this process holds (Load).
type Line struct {
	Catalog string  `json:"c"`
	Lang    string  `json:"l,omitempty"`
	NS      *string `json:"n,omitempty"`
	Key     *string `json:"k,omitempty"`
	Has     bool    `json:"has,omitempty"`
	IsLang  bool    `json:"lang,omitempty"`
	Load    bool    `json:"load,omitempty"`
}

var (
	mu     sync.Mutex
	opened bool
	out    *os.File
	dedup  bool
	seen   = map[string]bool{}
	failed bool
)

// Load says this process holds the catalog (its directory from the
// repository's root, as locale-sbom names it) and records every lookup in
// it. Call it where the catalog is loaded.
func Load(catalog string) { write(Line{Catalog: catalog, Load: true}) }

// Key records a lookup of key in lang's namespace ns of the catalog: ns ""
// for a catalog with one file per language, key's parts joined by dots,
// "" for the whole namespace. Record misses too: a lookup that falls back
// to another language records both.
func Key(catalog, lang, ns, key string) {
	write(Line{Catalog: catalog, Lang: lang, NS: &ns, Key: &key})
}

// Has records a check that lang's namespace ns is there.
func Has(catalog, lang, ns string) { write(Line{Catalog: catalog, Lang: lang, NS: &ns, Has: true}) }

// Lang records a check that the catalog has lang.
func Lang(catalog, lang string) { write(Line{Catalog: catalog, Lang: lang, IsLang: true}) }

func write(l Line) {
	b, err := json.Marshal(l)
	if err != nil {
		return
	}

	mu.Lock()
	defer mu.Unlock()

	if !opened {
		open()
	}

	if out == nil {
		return
	}

	// a process of its own writes each record once; one sharing a log
	// writes each time, since the log is cut per slice
	if dedup {
		if seen[string(b)] {
			return
		}

		seen[string(b)] = true
	}

	put(b)
}

func open() {
	opened = true

	switch {
	case os.Getenv(EnvLog) != "":
		out = openLog(os.Getenv(EnvLog))
	case os.Getenv(EnvDir) != "":
		out, dedup = openDir(os.Getenv(EnvDir)), true
	}

	if out == nil {
		return
	}

	b, err := json.Marshal(header{Recorder: "locale-keys", Version: Version, Runtime: "go", PID: os.Getpid()})
	if err == nil {
		put(b)
	}
}

func openLog(p string) *os.File {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644) //nolint:gosec // the path the environment names
	if err != nil {
		complain(err)

		return nil
	}

	return f
}

func openDir(d string) *os.File {
	if err := os.MkdirAll(d, 0o755); err != nil { //nolint:gosec // a coverage directory others read
		complain(err)

		return nil
	}

	for n := 1; ; n++ {
		name := filepath.Join(d, fmt.Sprintf("locale-keys.go-%d-%d.jsonl", os.Getpid(), n))

		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) //nolint:gosec // the directory the environment names
		if os.IsExist(err) {
			continue
		}

		if err != nil {
			complain(err)

			return nil
		}

		return f
	}
}

// put writes one JSON line in one write, so lines of processes sharing a
// log never interleave.
func put(b []byte) {
	if _, err := out.Write(append(b, '\n')); err != nil {
		complain(err)

		out = nil
	}
}

// complain says once on stderr that recording failed. The recording then
// lacks this process's lookups; a part with no header at all can't tell.
func complain(err error) {
	if failed {
		return
	}

	failed = true

	fmt.Fprintf(os.Stderr, "gorecord: %s: recording the catalog lookups failed at %s\n", err, time.Now().Format(time.RFC3339))
}
