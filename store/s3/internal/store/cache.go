package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// HeadCache keeps the heads a call read, by key and the ETag S3 gave them, in a file of the plugin's
// cache between calls: a listing then reads only the heads whose ETag changed. An entry is right as
// long as its ETag is the object's, so a cache another call wrote over, or lost, only costs reads.
type HeadCache struct {
	path string

	mu      sync.Mutex
	entries map[string]cached
	changed bool
}

type cached struct {
	ETag string `json:"etag"`
	Head head   `json:"head"`
}

// LoadHeads reads the cache in dir; an empty dir caches nothing between calls.
func LoadHeads(dir string) *HeadCache {
	c := &HeadCache{entries: map[string]cached{}}
	if dir == "" {
		return c
	}

	c.path = filepath.Join(dir, "heads.json")

	data, err := os.ReadFile(c.path)
	if err == nil {
		// a cache that can't be read is an empty one
		_ = json.Unmarshal(data, &c.entries) //nolint:errcheck // see above
	}

	if c.entries == nil {
		c.entries = map[string]cached{}
	}

	return c
}

func (c *HeadCache) get(key, etag string) (head, bool) {
	if c == nil {
		return head{}, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.entries[key]

	return e.Head, ok && e.ETag == etag && etag != ""
}

func (c *HeadCache) put(key, etag string, h head) {
	if c == nil || etag == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[key] = cached{ETag: etag, Head: h}
	c.changed = true
}

// Save writes the cache back, replacing the file whole.
func (c *HeadCache) Save() error {
	if c == nil || c.path == "" || !c.changed {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := json.Marshal(c.entries)
	if err != nil {
		return fmt.Errorf("%w: heads: %w", ErrStore, err)
	}

	if err := os.MkdirAll(filepath.Dir(c.path), 0o750); err != nil {
		return fmt.Errorf("%w: heads: %w", ErrStore, err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(c.path), "heads-*.json")
	if err != nil {
		return fmt.Errorf("%w: heads: %w", ErrStore, err)
	}

	_, werr := tmp.Write(data)

	if err := errors.Join(werr, tmp.Close()); err != nil {
		_ = os.Remove(tmp.Name()) //nolint:errcheck // the write's error is the one to tell

		return fmt.Errorf("%w: heads: %w", ErrStore, err)
	}

	if err := os.Rename(tmp.Name(), c.path); err != nil {
		return fmt.Errorf("%w: heads: %w", ErrStore, err)
	}

	return nil
}
