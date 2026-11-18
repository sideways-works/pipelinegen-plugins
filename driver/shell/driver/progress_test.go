package driver

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

func quickProgress(t *testing.T) {
	t.Helper()

	was := progressEvery
	progressEvery = 100 * time.Millisecond

	t.Cleanup(func() { progressEvery = was })
}

func TestASilentCheckSaysItStillRuns(t *testing.T) {
	quickProgress(t)

	conf := checksConf(map[string]any{"name": "slow lint", "run": "echo begun\nsleep 0.5\nexit 1"})

	got, log := runOf(t, request(t, conf, "a"))

	if !strings.Contains(log, `shell-driver: check "slow lint" of a still runs after`) {
		t.Errorf("no progress line:\n%s", log)
	}

	if tail := tailOf(got["a"], "slow lint"); strings.Contains(tail, "still runs") || !strings.Contains(tail, "begun") {
		t.Errorf("the tail: %q", tail)
	}

	// The threshold is longer than a shell may take to start on a busy machine,
	// which is silence too.
	progressEvery = time.Second
	conf = checksConf(map[string]any{"name": "chatty", "run": "i=0; while [ $i -lt 40 ]; do echo $i; i=$((i+1)); sleep 0.05; done"})

	_, log = runOf(t, request(t, conf, "a"))
	if strings.Contains(log, "still runs") {
		t.Errorf("a command that writes got a progress line:\n%s", log)
	}
}

func TestASilentScriptSaysItStillRuns(t *testing.T) {
	quickProgress(t)

	_, log := runOf(t, request(t, map[string]any{"script": "sleep 0.5"}, "a"))
	if !strings.Contains(log, "shell-driver: the script of a still runs after") {
		t.Errorf("no progress line for a slice's script:\n%s", log)
	}

	req := request(t, map[string]any{"script": "sleep 0.5"})
	req.Prepared = t.TempDir()

	var out bytes.Buffer
	if err := prepare(t.Context(), req, &out); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "shell-driver: the prepare script still runs after") {
		t.Errorf("no progress line for a prepare:\n%s", out.String())
	}

	// A set-up's script writes to the log itself, beside the progress lines.
	var setupLog lockedLog

	stop := make(chan struct{})
	close(stop)

	err := setup(t.Context(), request(t, map[string]any{"script": "sleep 0.5"}), func(map[string]string) error { return nil }, stop, &setupLog)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(setupLog.String(), "shell-driver: the set-up script still runs after") {
		t.Errorf("no progress line for a set-up:\n%s", setupLog.String())
	}
}

type lockedLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.buf.Write(p) //nolint:wrapcheck // a buffer's write never fails
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.buf.String()
}
