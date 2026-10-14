package cover

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadRequest(t *testing.T) {
	dir := t.TempDir()

	if _, _, ok := ReadRequest(dir); ok {
		t.Error("no request read as one")
	}

	if err := writeFile(filepath.Join(dir, RequestFile), "7 /work/cov erage\n"); err != nil {
		t.Fatal(err)
	}

	if seq, target, ok := ReadRequest(dir); !ok || seq != 7 || target != "/work/cov erage" {
		t.Errorf("ReadRequest = %d %q %v", seq, target, ok)
	}
}

func TestStartSkipsTheStandingRequest(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(filepath.Join(dir, RequestFile), "3 "+t.TempDir()+"\n"); err != nil {
		t.Fatal(err)
	}

	b := &binary{dir: dir, who: "api-1", ready: filepath.Join(dir, "api-1"+ReadySuffix)}
	if seq, _, ok := ReadRequest(dir); ok {
		b.last = seq
	}

	if err := writeFile(b.ready, "api-1\n"); err != nil {
		t.Fatal(err)
	}

	b.step()

	if matches, _ := filepath.Glob(filepath.Join(dir, "*"+DoneInfix+"*")); len(matches) != 0 { //nolint:errcheck // a fixed pattern
		t.Errorf("answered the standing request: %v", matches)
	}

	// a ready file removed by the caller comes back on the next poll
	if err := os.Remove(b.ready); err != nil {
		t.Fatal(err)
	}

	b.step()

	if got, err := os.ReadFile(b.ready); err != nil || !strings.HasPrefix(string(got), "api-1") {
		t.Errorf("ready file: %q %v", got, err)
	}
}
