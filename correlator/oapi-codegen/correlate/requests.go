package correlate

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// RequestsFile is the file a slice's requests are kept in, one per line:
// the method, a space, and the URL or path ("GET /api/notes/n1?full=1").
const RequestsFile = "requests.txt"

// ErrNoData marks an observation without evidence that recording ran: a slice
// part with neither a requests file nor a HAR file, or no slice part at all.
// An empty requests file is evidence, and answers empty.
var ErrNoData = errors.New("no request log")

// ErrNoContract marks a recording observed against a tree with no contract:
// answering "touched nothing" would empty the slice's list.
var ErrNoContract = errors.New("no contract in the tree to map the requests to")

// Observe reads the requests files and HAR files a slice left in dirs and
// returns the operations they reached, sorted. dirs[0] is the batch's setup
// directory, which may hold nothing; each of the slice's parts needs evidence
// of a recording.
func (m *Model) Observe(dirs []string) ([]string, error) {
	if len(dirs) < 2 {
		return nil, fmt.Errorf("%w: the request names no slice part", ErrNoData)
	}

	if err := m.Config.checkLines(); err != nil {
		return nil, err
	}

	r := m.NewRouter(m.bases())
	pt := m.patterns()
	seen := map[string]bool{}

	var unknown error

	add := func(method, raw, logFile string) {
		if logFile != "" && m.Config.RequestLogLines == LinesPatterns {
			id, err := pt.match(method, raw, logFile, r)
			if err != nil && unknown == nil {
				unknown = err
			}

			if id != "" {
				seen[id] = true
			}

			return
		}

		if id, ok := r.Match(method, raw); ok {
			seen[id] = true
		}
	}

	if err := readDir(dirs[0], add); err != nil && !errors.Is(err, ErrNoData) {
		return nil, err
	}

	for _, dir := range dirs[1:] {
		if err := readDir(dir, add); err != nil {
			return nil, err
		}
	}

	if len(m.Config.Documents) == 0 {
		return nil, ErrNoContract
	}

	if unknown != nil {
		return nil, unknown
	}

	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}

	slices.Sort(out)

	return out, nil
}

func readDir(dir string, add func(method, raw, logFile string)) error {
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrNoData, dir)
		}

		return fmt.Errorf("observe: %w", err)
	}

	recorded := false

	logFile := filepath.Join(dir, RequestsFile)

	f, err := os.Open(logFile) //nolint:gosec // the slice's own directory
	if err == nil {
		recorded = true
		err = readLines(f, func(method, raw string) { add(method, raw, logFile) })

		if cerr := f.Close(); err == nil {
			err = cerr
		}

		if err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("observe: %w", err)
	}

	hars, err := filepath.Glob(filepath.Join(dir, "*.har"))
	if err != nil {
		return fmt.Errorf("observe: %w", err)
	}

	for _, h := range hars {
		recorded = true

		if err := readHAR(h, func(method, raw string) { add(method, raw, "") }); err != nil {
			return err
		}
	}

	if !recorded {
		return fmt.Errorf("%w: neither %s nor a HAR file in %s", ErrNoData, RequestsFile, dir)
	}

	return nil
}

func readLines(rd io.Reader, add func(method, raw string)) error {
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)

	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 {
			add(fields[0], fields[1])
		}
	}

	if err := sc.Err(); err != nil {
		return fmt.Errorf("observe: %w", err)
	}

	return nil
}

type har struct {
	Log struct {
		Entries []struct {
			Request struct {
				Method string `json:"method"`
				URL    string `json:"url"`
			} `json:"request"`
		} `json:"entries"`
	} `json:"log"`
}

func readHAR(p string, add func(method, raw string)) error {
	data, err := os.ReadFile(p) //nolint:gosec // a file the slice left in its own directory
	if err != nil {
		return fmt.Errorf("observe: %w", err)
	}

	var h har
	if err := json.Unmarshal(data, &h); err != nil {
		return fmt.Errorf("observe: %s: %w", p, err)
	}

	for _, e := range h.Log.Entries {
		add(e.Request.Method, e.Request.URL)
	}

	return nil
}

func (m *Model) bases() map[string]string {
	out := map[string]string{}

	for _, d := range m.Config.Documents {
		if d.Base != "" {
			out[d.Path] = d.Base
		}
	}

	return out
}
