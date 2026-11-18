// Package observe turns the statements a slice ran into the ids of the
// database objects it touched, named as pg-sbom names them.
package observe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sideways-works/pipelinegen-sbom-pg/pgid"
	"github.com/sideways-works/pipelinegen-sbom-pg/sqlref"

	"github.com/sideways-works/pipelinegen-correlator-sql/collect"
)

type Config struct {
	// SearchPath is the schemas an unqualified name is looked up in, as
	// the tests' connections set it. Default: public.
	SearchPath []string `json:"search_path"`
	// Collect is collect's setup, which says which databases count when
	// a part's window is read.
	Collect collect.Config `json:"-"`
}

// ErrCannotTell says statements could not be read, or were never written,
// so what the slice touched is not known.
var ErrCannotTell = errors.New("cannot tell")

type Observation struct {
	// Touched is every id, the setup's included.
	Touched []string
	// SetupLike is the part of Touched that only setup touched, setup
	// databases' statements included.
	SetupLike []string
}

// Touched reads the statements collect wrote into dirs (dirs[0] the batch's
// setup directory, the rest the slice's parts) and answers the ids of the
// relations, rows (pg:data:) and routines they touched. cache, when set,
// keeps parsed statements between calls.
func Touched(cfg Config, dirs []string, cache string) (Observation, error) {
	path := cfg.SearchPath
	if len(path) == 0 {
		path = []string{"public"}
	}

	// the setup is told by its place, never by its name: a request
	// without a part of the slice has nothing that shows a recording
	if len(dirs) < 2 {
		return Observation{}, fmt.Errorf("%w: the request names no part of the slice", ErrCannotTell)
	}

	own, setup := map[string]bool{}, map[string]bool{}
	r := reader{path: path, cache: cache}

	for i, dir := range dirs {
		isSetup := i == 0

		into := own
		if isSetup {
			into = setup
		}

		skip, err := present(dir, isSetup)
		if err != nil {
			return Observation{}, err
		}

		if skip {
			continue
		}

		if err := r.part(cfg.Collect, dir, into, setup); err != nil {
			return Observation{}, err
		}
	}

	if len(r.unread) > 0 {
		return Observation{}, fmt.Errorf("%w: %d statements don't parse; %s", ErrCannotTell, len(r.unread), locate(r.unread[0]))
	}

	var obs Observation

	for id := range own {
		obs.Touched = append(obs.Touched, id)
	}

	for id := range setup {
		if !own[id] {
			obs.Touched = append(obs.Touched, id)
			obs.SetupLike = append(obs.SetupLike, id)
		}
	}

	sort.Strings(obs.Touched)
	sort.Strings(obs.SetupLike)

	return obs, nil
}

// present tells whether dir holds statements to read: only a statements file
// collect finished says what a slice touched. A setup directory may be
// missing.
func present(dir string, isSetup bool) (skip bool, err error) {
	if _, err := os.Stat(filepath.Join(dir, collect.UnconfiguredFile)); err == nil {
		return false, fmt.Errorf("%w: collect had no log to read (config: log)", ErrCannotTell)
	}

	if why, err := os.ReadFile(filepath.Join(dir, collect.UnboundedFile)); err == nil { //nolint:gosec // the slice's own directory, from pipelinegen
		return false, fmt.Errorf("%w: collect couldn't tell this part's statements from another slice's: %.800s", ErrCannotTell, strings.TrimSpace(string(why)))
	}

	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		if isSetup {
			return true, nil
		}

		return false, fmt.Errorf("%w: %s is missing", ErrCannotTell, dir)
	}

	if _, err := os.Stat(filepath.Join(dir, collect.StatementsFile)); errors.Is(err, fs.ErrNotExist) && !collect.HasWindow(dir) {
		if isSetup {
			return true, nil
		}

		return false, fmt.Errorf("%w: %s holds no window and no statements file", ErrCannotTell, dir)
	}

	return false, nil
}

type reader struct {
	path   []string
	cache  string
	unread []collect.Statement
}

func (r *reader) part(cfg collect.Config, dir string, into, setup map[string]bool) error {
	if collect.HasWindow(dir) {
		part, err := collect.Read(cfg, dir)
		if errors.Is(err, collect.ErrCannotTell) {
			return fmt.Errorf("%w: %w", ErrCannotTell, err)
		}

		if err != nil {
			return err
		}

		r.add(part.Own, into)
		r.add(part.Setup, setup)

		return nil
	}

	if err := r.read(filepath.Join(dir, collect.StatementsFile), into); err != nil {
		return err
	}

	if err := r.read(filepath.Join(dir, collect.SetupFile), setup); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	return nil
}

func (r *reader) read(name string, ids map[string]bool) error {
	if _, err := os.Stat(name); err != nil {
		return fmt.Errorf("reading statements: %w", err)
	}

	sts, err := collect.ReadStatements(name)
	if err != nil {
		return err
	}

	r.add(sts, ids)

	return nil
}

func (r *reader) add(sts []collect.Statement, ids map[string]bool) {
	for _, s := range sts {
		refs, err := refsOf(s.Text, r.cache)
		if err != nil {
			r.unread = append(r.unread, s)

			continue
		}

		for _, ref := range refs {
			add(ids, ref, r.path)
		}
	}
}

// locate says where a statement is and which it is, without its text: the
// log holds the values the tests used, and a reason travels further than
// the build. Whoever may read the build's log finds it there by its place,
// its length and its hash.
func locate(s collect.Statement) string {
	sum := sha256.Sum256([]byte(s.Text))
	which := fmt.Sprintf("%d bytes long, SHA-256 %s", len(s.Text), hex.EncodeToString(sum[:]))

	if s.At.File == "" {
		return "the first is " + which
	}

	// a recording that lists its statements itself
	if filepath.Base(s.At.File) == collect.StatementsFile {
		return fmt.Sprintf("the first is entry %d of %s, %s", s.At.Line, s.At.File, which)
	}

	return fmt.Sprintf("the first is in the record at byte %d of %s (line %d of its range read from byte %d), %s",
		s.At.Offset, s.At.File, s.At.Line, s.At.From, which)
}

func add(ids map[string]bool, r sqlref.Ref, path []string) {
	schemas := []string{r.Schema}
	if r.Schema == "" {
		schemas = path
	}

	for _, schema := range schemas {
		if r.Function {
			ids[pgid.Routine(schema, r.Name)] = true

			continue
		}

		ids[pgid.Of(pgid.Relation, schema, r.Name)] = true

		// the rows the migrations or the seed left matter to whoever
		// reads them; a statement that only inserts depends on the
		// table's definition alone
		if r.Read {
			ids[pgid.Of(pgid.Data, schema, r.Name)] = true
		}
	}
}

// refsOf parses a statement, through the cache: most statements repeat
// from slice to slice, and starting the parser costs more than reading a
// small file.
func refsOf(text, cache string) ([]sqlref.Ref, error) {
	if cache == "" {
		return parse(text)
	}

	sum := sha256.Sum256([]byte(text))
	key := hex.EncodeToString(sum[:])
	name := filepath.Join(cache, "statements-v1", key[:2], key+".json")

	if b, err := os.ReadFile(name); err == nil { //nolint:gosec // our own cache
		var refs []sqlref.Ref
		if json.Unmarshal(b, &refs) == nil {
			return refs, nil
		}
	}

	refs, err := parse(text)
	if err != nil {
		return nil, err
	}

	// a cache that can't be written only costs time
	if b, err := json.Marshal(refs); err == nil {
		writeAtomic(name, b)
	}

	return refs, nil
}

func parse(text string) ([]sqlref.Ref, error) {
	refs, err := sqlref.Refs(text)
	if err != nil {
		return nil, fmt.Errorf("statement: %w", err)
	}

	return refs, nil
}

// writeAtomic writes a cache file under a temporary name and renames it,
// so a concurrent reader never sees half of it. Failing only costs time,
// so it reports nothing.
func writeAtomic(name string, b []byte) {
	if os.MkdirAll(filepath.Dir(name), 0o750) != nil {
		return
	}

	tmp, err := os.CreateTemp(filepath.Dir(name), ".tmp-*")
	if err != nil {
		return
	}

	_, werr := tmp.Write(b)
	cerr := tmp.Close()

	if werr != nil || cerr != nil || os.Rename(tmp.Name(), name) != nil {
		if os.Remove(tmp.Name()) != nil {
			return
		}
	}
}
