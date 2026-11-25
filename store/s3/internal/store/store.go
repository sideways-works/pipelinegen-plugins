// Package store keeps pipelinegen's records in an S3 bucket.
//
// Each version of a record is an object of its own, <prefix>v/<version>, written once and never
// changed: the record's name, the writer's time and its files, gzipped JSON. A record's head,
// <prefix>h/<name>, names its newest version; a write replaces a head only while its ETag is the one
// the write read (If-Match), and creates one only where none is (If-None-Match: *), so two writers
// of one record never both win. A delete writes a head that says so, guarded alike: S3 promises no
// conditional delete everywhere.
package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/sideways-works/pipelinegen-plugins/store/s3/internal/s3"
)

// ErrStore is a store that can't be read or written as asked.
var ErrStore = errors.New("s3-store")

// parallel is how many objects a call reads at once.
const parallel = 32

// Store is one bucket's records under a prefix.
type Store struct {
	Client *s3.Client
	Prefix string
	// Heads caches each head's record by the ETag a listing gives, so a listing reads only the
	// heads that changed; nil caches nothing.
	Heads *HeadCache
}

// Record is a record's newest version.
type Record struct {
	Name    string    `json:"name"`
	Version string    `json:"version"`
	Time    time.Time `json:"time"`
}

// head is a head object's body.
type head struct {
	Record

	Deleted bool `json:"deleted,omitempty"`
}

// version is a version object's body.
type version struct {
	Name  string            `json:"name"`
	Time  time.Time         `json:"time"`
	Files map[string][]byte `json:"files"`
}

func (s *Store) headKey(name string) string { return s.Prefix + "h/" + name }

func (s *Store) versionKey(v string) string { return s.Prefix + "v/" + v }

// readHead is a record's head and its ETag; no head is none.
func (s *Store) readHead(ctx context.Context, name string) (head, string, bool, error) {
	data, etag, ok, err := s.Client.Get(ctx, s.headKey(name))
	if err != nil || !ok {
		return head{}, "", false, err
	}

	var h head
	if err := json.Unmarshal(data, &h); err != nil {
		return head{}, "", false, fmt.Errorf("%w: the head of %s: %w", ErrStore, name, err)
	}

	return h, etag, true, nil
}

// List is every record under any of the prefixes.
func (s *Store) List(ctx context.Context, prefixes []string) ([]Record, error) {
	var objects []s3.Object

	for _, p := range prefixes {
		got, err := s.Client.Objects(ctx, s.headKey(p))
		if err != nil {
			return nil, err
		}

		objects = append(objects, got...)
	}

	recs := make([]*Record, len(objects))

	err := each(ctx, len(objects), func(ctx context.Context, i int) error {
		o := objects[i]
		name := strings.TrimPrefix(o.Key, s.Prefix+"h/")

		if h, ok := s.Heads.get(o.Key, o.ETag); ok {
			if !h.Deleted {
				recs[i] = &h.Record
			}

			return nil
		}

		h, etag, ok, err := s.readHead(ctx, name)
		if err != nil || !ok {
			return err
		}

		s.Heads.put(o.Key, etag, h)

		if !h.Deleted {
			recs[i] = &h.Record
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	out := make([]Record, 0, len(recs))

	for _, r := range recs {
		if r != nil && !seen[r.Name] {
			seen[r.Name] = true
			out = append(out, *r)
		}
	}

	return out, nil
}

// First is the first of the names that exists.
func (s *Store) First(ctx context.Context, names []string) (*Record, error) {
	for _, n := range names {
		h, _, ok, err := s.readHead(ctx, n)
		if err != nil {
			return nil, err
		}

		if ok && !h.Deleted {
			return &h.Record, nil
		}
	}

	return nil, nil //nolint:nilnil // none of the names is a record
}

// Read is each version's files, only those at paths when paths is set; a version the bucket
// lacks is left out.
func (s *Store) Read(ctx context.Context, versions, paths []string) (map[string]map[string][]byte, error) {
	files := make([]map[string][]byte, len(versions))

	err := each(ctx, len(versions), func(ctx context.Context, i int) error {
		data, _, ok, err := s.Client.Get(ctx, s.versionKey(versions[i]))
		if err != nil || !ok {
			return err
		}

		v, err := decode(data)
		if err != nil {
			return fmt.Errorf("%w: version %s: %w", ErrStore, versions[i], err)
		}

		files[i] = pick(v.Files, paths)

		return nil
	})
	if err != nil {
		return nil, err
	}

	out := map[string]map[string][]byte{}

	for i, f := range files {
		if f != nil {
			out[versions[i]] = f
		}
	}

	return out, nil
}

func pick(files map[string][]byte, paths []string) map[string][]byte {
	if paths == nil {
		return files
	}

	out := map[string][]byte{}

	for _, p := range paths {
		if data, ok := files[p]; ok {
			out[p] = data
		}
	}

	return out
}

// Update is one record's write, as pipelinegen's store.write names it.
type Update struct {
	Name   string
	Expect string
	Any    bool
	Delete bool
	Files  map[string][]byte
}

// ErrStale is an update whose record no longer held what it expected.
var ErrStale = errors.New("stale")

// StaleError names the record whose update was stale.
type StaleError struct{ Name string }

func (e *StaleError) Error() string { return e.Name + ": " + ErrStale.Error() }

func (e *StaleError) Is(target error) bool { return target == ErrStale }

// Write applies the updates in order, and stops at the first whose record holds other than it
// expects, returning ErrStale and the versions written before it.
func (s *Store) Write(ctx context.Context, updates []Update, at time.Time) (map[string]string, error) {
	out := map[string]string{}

	for _, u := range updates {
		v, err := s.write(ctx, u, at)
		if errors.Is(err, ErrStale) {
			return out, &StaleError{Name: u.Name}
		}

		if err != nil {
			return out, fmt.Errorf("%s: %w", u.Name, err)
		}

		if v != "" {
			out[u.Name] = v
		}
	}

	return out, nil
}

// tries bounds how often a write that applies whatever the record holds reads the head again
// after another writer moved it.
const tries = 10

func (s *Store) write(ctx context.Context, u Update, at time.Time) (string, error) {
	v := ""

	if !u.Delete {
		var err error
		if v, err = s.putVersion(ctx, u, at); err != nil {
			return "", err
		}
	}

	for range tries {
		cur, etag, exists, err := s.readHead(ctx, u.Name)
		if err != nil {
			return "", err
		}

		if err := admits(u, cur, exists); err != nil || u.Delete && (!exists || cur.Deleted) {
			return "", err
		}

		next, body, err := nextHead(u, v, at)
		if err != nil {
			return "", err
		}

		match := ""
		if exists {
			match = etag
		}

		newTag, err := s.Client.PutIf(ctx, s.headKey(u.Name), body, match)
		if err != nil {
			// an answer lost on the way, or a write sent twice, may hide that this very head landed:
			// each head is unique, so reading it says
			if got, _, ok, rerr := s.readHead(ctx, u.Name); rerr == nil && ok && got.Version == next.Version {
				return v, nil
			}
		}

		switch {
		case errors.Is(err, s3.ErrPrecondition) && !u.Any:
			return "", ErrStale
		case errors.Is(err, s3.ErrPrecondition), err != nil && transient(err):
			continue
		case err != nil:
			return "", err
		}

		s.Heads.put(s.headKey(u.Name), newTag, next)

		return v, nil
	}

	return "", fmt.Errorf("%w: %s moved under %d writes in a row", ErrStore, u.Name, tries)
}

// admits refuses an update expecting a version the record's head doesn't hold.
func admits(u Update, cur head, exists bool) error {
	live := exists && !cur.Deleted

	switch {
	case u.Any:
	case u.Expect == "" && live, u.Expect != "" && (!live || cur.Version != u.Expect):
		return ErrStale
	}

	return nil
}

// nextHead is the head an update writes, and its body.
func nextHead(u Update, v string, at time.Time) (head, []byte, error) {
	next := head{Deleted: u.Delete}
	next.Name, next.Version, next.Time = u.Name, v, at

	// a delete's head is unique too, so a retry can tell its own
	if u.Delete {
		tomb, err := newID()
		if err != nil {
			return head{}, nil, err
		}

		next.Version = tomb
	}

	body, err := json.Marshal(next)
	if err != nil {
		return head{}, nil, fmt.Errorf("%w: %w", ErrStore, err)
	}

	return next, body, nil
}

// putVersion writes the update's files as a new version, created once. Its key is random, so an
// object already there is this write's own, sent before an answer got lost.
func (s *Store) putVersion(ctx context.Context, u Update, at time.Time) (string, error) {
	v, err := newID()
	if err != nil {
		return "", err
	}

	body, err := encode(version{Name: u.Name, Time: at, Files: u.Files})
	if err != nil {
		return "", err
	}

	for try := 0; ; try++ {
		_, err := s.Client.PutIf(ctx, s.versionKey(v), body, "")

		switch {
		case err == nil, errors.Is(err, s3.ErrPrecondition) && try > 0:
			return v, nil
		case transient(err) && try < tries:
			continue
		}

		return "", err
	}
}

func newID() (string, error) {
	id := make([]byte, 20)
	if _, err := rand.Read(id); err != nil {
		return "", fmt.Errorf("%w: %w", ErrStore, err)
	}

	return hex.EncodeToString(id), nil
}

// transient is an error a request may not have when sent again: no answer, or the service's own
// failure (5xx); a refusal of the request itself is no such error.
func transient(err error) bool {
	if e, ok := errors.AsType[*s3.Error](err); ok {
		return e.Status >= 500
	}

	return !errors.Is(err, s3.ErrPrecondition)
}

func encode(v version) ([]byte, error) {
	var b bytes.Buffer

	zw := gzip.NewWriter(&b)
	if err := json.NewEncoder(zw).Encode(v); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}

	return b.Bytes(), nil
}

func decode(data []byte) (version, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return version{}, err //nolint:wrapcheck // Read names the version
	}

	plain, err := io.ReadAll(zr)
	if err != nil {
		return version{}, err //nolint:wrapcheck // Read names the version
	}

	var v version

	return v, json.Unmarshal(plain, &v) //nolint:wrapcheck // Read names the version
}

// each runs f for every index from 0 to n, parallel at a time, and returns the first error.
func each(ctx context.Context, n int, f func(context.Context, int) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)

	slots := make(chan struct{}, parallel)

	for i := range n {
		slots <- struct{}{}

		wg.Go(func() {
			defer func() { <-slots }()

			if err := f(ctx, i); err != nil {
				mu.Lock()
				if first == nil {
					first = err

					cancel()
				}
				mu.Unlock()
			}
		})
	}

	wg.Wait()

	return first
}
