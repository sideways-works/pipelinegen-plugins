package collect

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen-plugins/correlator/go/observe"
)

func serverDir(t *testing.T) string {
	t.Helper()

	dir, err := filepath.Abs(filepath.Join("testdata", "server"))
	if err != nil {
		t.Fatal(err)
	}

	return dir
}

func buildServer(t *testing.T, mode string) string {
	t.Helper()

	bin := filepath.Join(t.TempDir(), "server")

	cmd := exec.CommandContext(t.Context(), "go", "build", "-tags", "pipelinegen_cover", "-cover",
		"-covermode="+mode, "-coverpkg=example.com/server", "-o", bin, ".")
	cmd.Dir = serverDir(t)

	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off")

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	return bin
}

type server struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

func start(t *testing.T, bin, dir, name string) *server {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), bin)

	cmd.Env = append(os.Environ(), EnvDir+"="+dir, "PIPELINEGEN_COVER_NAME="+name)
	cmd.Stderr = os.Stderr

	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}

	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	s := &server{cmd: cmd, in: in, out: bufio.NewReader(out)}

	t.Cleanup(func() {
		_ = in.Close() //nolint:errcheck // the test is over
		_ = cmd.Wait() //nolint:errcheck // as above
	})

	// it is ready once it answers a line: its init ran before main
	s.do(t, "hello")

	return s
}

func (s *server) do(t *testing.T, line string) {
	t.Helper()

	if _, err := io.WriteString(s.in, line+"\n"); err != nil {
		t.Fatal(err)
	}

	if _, err := s.out.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
}

func TestEachSliceGetsItsOwnCounters(t *testing.T) {
	bin := buildServer(t, "atomic")
	ctl, out := t.TempDir(), t.TempDir()
	srv := start(t, bin, ctl, "server")
	o := Options{Dir: ctl, Stack: true}

	snap := func(name string) string {
		dir := filepath.Join(out, name)

		r, err := Snapshot(t.Context(), o, dir)
		if err != nil {
			t.Fatal(err)
		}

		if len(r.Answered) != 1 || !strings.HasPrefix(r.Answered[0], "server-") {
			t.Errorf("report %+v", r)
		}

		return dir
	}

	setup := snap("_setup")

	srv.do(t, "a")

	sliceA := snap("a")

	srv.do(t, "b")

	sliceB := snap("b")

	for dir, want := range map[string][2]string{sliceA: {"go:.#func:a", "go:.#func:b"}, sliceB: {"go:.#func:b", "go:.#func:a"}} {
		got, err := observe.Observe(t.Context(), serverDir(t), "", []string{setup, dir})
		if err != nil {
			t.Fatal(err)
		}

		if !slices.Contains(got.Touched, want[0]) || slices.Contains(got.Touched, want[1]) || slices.Contains(got.SetupLike, want[0]) {
			t.Errorf("%s touched %v, want %s and not %s", filepath.Base(dir), got.Touched, want[0], want[1])
		}
	}

	// the setup's snapshot reads on its own
	got, err := observe.Observe(t.Context(), serverDir(t), "", []string{setup})
	if err != nil || !slices.Contains(got.Touched, "go:.#func:main") {
		t.Errorf("setup alone: %v %v, want main", got.Touched, err)
	}

	if left, _ := filepath.Glob(filepath.Join(ctl, "*"+doneInfix+"*")); len(left) != 0 { //nolint:errcheck // a fixed pattern
		t.Errorf("done markers left behind: %v", left)
	}
}

func TestABinaryWhoseCountersCantBeClearedStops(t *testing.T) {
	bin := buildServer(t, "set")
	ctl := t.TempDir()
	srv := start(t, bin, ctl, "server")

	_, err := Snapshot(t.Context(), Options{Dir: ctl, Stack: true}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "covermode=atomic") {
		t.Errorf("err = %v, want a refusal naming -covermode=atomic", err)
	}

	if err := srv.cmd.Wait(); err == nil {
		t.Error("the binary went on")
	}
}

func TestNoBinaryReady(t *testing.T) {
	if r, err := Snapshot(t.Context(), Options{Dir: t.TempDir()}, t.TempDir()); err != nil || len(r.Answered) != 0 {
		t.Errorf("plain test binaries: %+v %v, want nothing to do", r, err)
	}

	_, err := Snapshot(t.Context(), Options{Dir: t.TempDir(), Stack: true}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "pipelinegen_cover") {
		t.Errorf("a stack with no binary ready: %v, want a failure naming the build tag", err)
	}
}

func TestSilentBinaries(t *testing.T) {
	ctl := t.TempDir()

	// worker-7 is live (its ready file is fresh) and never answers;
	// cron-3's ready file is old: it exited
	if err := os.WriteFile(filepath.Join(ctl, "worker-7"+readySuffix), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	old := filepath.Join(ctl, "cron-3"+readySuffix)
	if err := os.WriteFile(old, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	then := time.Now().Add(-time.Minute)
	if err := os.Chtimes(old, then, then); err != nil {
		t.Fatal(err)
	}

	r, err := Snapshot(t.Context(), Options{Dir: ctl, Stack: true, Wait: 300 * time.Millisecond}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "worker-7") || strings.Contains(err.Error(), "cron-3") {
		t.Errorf("err = %v, want it to name worker-7 alone", err)
	}

	if !slices.Equal(r.Gone, []string{"cron-3"}) {
		t.Errorf("gone %v, want cron-3", r.Gone)
	}

	if _, err := os.Stat(old); err == nil {
		t.Error("the exited binary's ready file stayed")
	}
}
