package store

import (
	"crypto/md5" //nolint:gosec // an ETag, as S3 computes one, not a check of anything
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen-plugins/store/s3/internal/s3"
)

// fakeS3 is one bucket in memory, path style, honouring If-Match and If-None-Match on PUT as S3
// does: atomically, one object at a time.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	gets    atomic.Int64
	// lose, while above zero, applies a PUT and answers 500, as a reply lost on the way looks
	lose int
}

func etag(data []byte) string {
	sum := md5.Sum(data) //nolint:gosec // see the import

	return `"` + hex.EncodeToString(sum[:]) + `"`
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/bucket"), "/")

	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && key == "":
		f.list(w, r.URL.Query())
	case r.Method == http.MethodGet:
		f.gets.Add(1)

		data, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		w.Header().Set("ETag", etag(data))
		_, _ = w.Write(data) //nolint:errcheck // a test server's answer
	case r.Method == http.MethodPut:
		data, _ := io.ReadAll(r.Body) //nolint:errcheck // the test sends whole bodies
		cur, exists := f.objects[key]

		if m := r.Header.Get("If-Match"); m != "" && (!exists || etag(cur) != m) {
			w.WriteHeader(http.StatusPreconditionFailed)

			return
		}

		if r.Header.Get("If-None-Match") == "*" && exists {
			w.WriteHeader(http.StatusPreconditionFailed)

			return
		}

		f.objects[key] = data

		if f.lose > 0 {
			f.lose--
			w.WriteHeader(http.StatusInternalServerError)

			return
		}

		w.Header().Set("ETag", etag(data))
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *fakeS3) list(w http.ResponseWriter, q url.Values) {
	type content struct {
		Key  string `xml:"Key"`
		ETag string `xml:"ETag"`
	}

	var keys []string

	for k := range f.objects {
		if strings.HasPrefix(k, q.Get("prefix")) {
			keys = append(keys, k)
		}
	}

	slices.Sort(keys)

	// two keys a page, so listings go page by page
	start := 0
	if t := q.Get("continuation-token"); t != "" {
		start = slices.Index(keys, t)
	}

	page := struct {
		XMLName               xml.Name  `xml:"ListBucketResult"`
		Contents              []content `xml:"Contents"`
		IsTruncated           bool      `xml:"IsTruncated"`
		NextContinuationToken string    `xml:"NextContinuationToken,omitempty"`
	}{}

	for i := start; i < len(keys) && i < start+2; i++ {
		page.Contents = append(page.Contents, content{Key: keys[i], ETag: etag(f.objects[keys[i]])})
	}

	if start+2 < len(keys) {
		page.IsTruncated, page.NextContinuationToken = true, keys[start+2]
	}

	data, _ := xml.Marshal(page) //nolint:errcheck // a fixed shape
	_, _ = w.Write(data)         //nolint:errcheck // a test server's answer
}

func newStore(t *testing.T, f *fakeS3, cacheDir string) *Store {
	t.Helper()

	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	return &Store{
		Client: &s3.Client{Endpoint: u, Region: "us-east-1", Bucket: "bucket", PathStyle: true, Creds: s3.Credentials{AccessKeyID: "k", SecretAccessKey: "s"}},
		Prefix: "p/",
		Heads:  LoadHeads(cacheDir),
	}
}

func TestAWriteIsListedAndReadAndAGuardIsCompareAndSwap(t *testing.T) {
	ctx := t.Context()
	s := newStore(t, &fakeS3{objects: map[string][]byte{}}, "")
	at := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	got, err := s.Write(ctx, []Update{
		{Name: "refs/pipelinegen/x/one", Files: map[string][]byte{"state.json": []byte("1"), "recording.txt": []byte("r")}},
		{Name: "refs/pipelinegen/x/two", Files: map[string][]byte{"state.json": []byte("2")}},
		{Name: "refs/pipelinegen/x/three", Files: map[string][]byte{"state.json": []byte("3")}},
	}, at)
	if err != nil || len(got) != 3 {
		t.Fatalf("write: %v %v", got, err)
	}

	recs, err := s.List(ctx, []string{"refs/pipelinegen/x/"})
	if err != nil || len(recs) != 3 {
		t.Fatalf("list, page by page: %v %v", recs, err)
	}

	for _, r := range recs {
		if r.Version != got[r.Name] || !r.Time.Equal(at) {
			t.Fatalf("listed %+v, written %s at %v", r, got[r.Name], at)
		}
	}

	files, err := s.Read(ctx, []string{got["refs/pipelinegen/x/one"]}, []string{"recording.txt"})
	if err != nil || len(files[got["refs/pipelinegen/x/one"]]) != 1 || string(files[got["refs/pipelinegen/x/one"]]["recording.txt"]) != "r" {
		t.Fatalf("read one path: %v %v", files, err)
	}

	one := "refs/pipelinegen/x/one"

	if _, err := s.Write(ctx, []Update{{Name: one, Files: map[string][]byte{"v": []byte("x")}}}, at); !errors.Is(err, ErrStale) {
		t.Fatalf("a second create: %v", err)
	}

	next, err := s.Write(ctx, []Update{{Name: one, Expect: got[one], Files: map[string][]byte{"v": []byte("2")}}}, at)
	if err != nil {
		t.Fatal(err)
	}

	stale, err := s.Write(ctx, []Update{
		{Name: "refs/pipelinegen/x/two", Any: true, Files: map[string][]byte{"v": []byte("y")}},
		{Name: one, Expect: got[one], Files: map[string][]byte{"v": []byte("x")}},
		{Name: "refs/pipelinegen/x/three", Any: true, Files: map[string][]byte{"v": []byte("z")}},
	}, at)

	var se *StaleError
	if !errors.As(err, &se) || se.Name != one || len(stale) != 1 {
		t.Fatalf("a batch stale in its middle: %v %v; the update before it stands, those after don't go", stale, err)
	}

	if _, err := s.Write(ctx, []Update{{Name: one, Expect: next[one], Delete: true}}, at); err != nil {
		t.Fatal(err)
	}

	if r, err := s.First(ctx, []string{one, "refs/pipelinegen/x/three"}); err != nil || r == nil || r.Name != "refs/pipelinegen/x/three" {
		t.Fatalf("first after a delete: %+v %v", r, err)
	}

	if _, err := s.Write(ctx, []Update{{Name: one, Files: map[string][]byte{"v": []byte("again")}}}, at); err != nil {
		t.Fatalf("a record deleted is created again: %v", err)
	}
}

func TestAListingReadsOnlyTheHeadsThatChanged(t *testing.T) {
	ctx := t.Context()
	f := &fakeS3{objects: map[string][]byte{}}
	cache := t.TempDir()
	at := time.Now().UTC()

	w := newStore(t, f, "")

	var ups []Update
	for _, n := range []string{"a", "b", "c", "d"} {
		ups = append(ups, Update{Name: "refs/pipelinegen/r/" + n, Files: map[string][]byte{"f": []byte(n)}})
	}

	if _, err := w.Write(ctx, ups, at); err != nil {
		t.Fatal(err)
	}

	first := newStore(t, f, cache)
	if recs, err := first.List(ctx, []string{"refs/pipelinegen/r/"}); err != nil || len(recs) != 4 {
		t.Fatalf("first listing: %v %v", recs, err)
	}

	if err := first.Heads.Save(); err != nil {
		t.Fatal(err)
	}

	if _, err := w.Write(ctx, []Update{{Name: "refs/pipelinegen/r/a", Any: true, Files: map[string][]byte{"f": []byte("a2")}}}, at); err != nil {
		t.Fatal(err)
	}

	before := f.gets.Load()

	again := newStore(t, f, cache)
	if recs, err := again.List(ctx, []string{"refs/pipelinegen/r/"}); err != nil || len(recs) != 4 {
		t.Fatalf("second listing: %v %v", recs, err)
	}

	if read := f.gets.Load() - before; read != 1 {
		t.Fatalf("the second listing read %d heads, want the 1 that changed", read)
	}
}

func TestAWriteWhoseAnswerGotLostIsNoStaleOne(t *testing.T) {
	ctx := t.Context()
	f := &fakeS3{objects: map[string][]byte{}}
	s := newStore(t, f, "")
	at := time.Now().UTC()
	name := "refs/pipelinegen/lost"

	// the version and the head both land, and both answers are lost: the write still holds
	f.lose = 2

	got, err := s.Write(ctx, []Update{{Name: name, Files: map[string][]byte{"v": []byte("1")}}}, at)
	if err != nil || got[name] == "" {
		t.Fatalf("a create whose answers got lost: %v %v", got, err)
	}

	if r, err := s.First(ctx, []string{name}); err != nil || r == nil || r.Version != got[name] {
		t.Fatalf("the record after it: %+v %v", r, err)
	}

	f.lose = 1

	if _, err := s.Write(ctx, []Update{{Name: name, Expect: got[name], Delete: true}}, at); err != nil {
		t.Fatalf("a delete whose answer got lost: %v", err)
	}

	if r, err := s.First(ctx, []string{name}); err != nil || r != nil {
		t.Fatalf("after the delete: %+v %v", r, err)
	}
}
