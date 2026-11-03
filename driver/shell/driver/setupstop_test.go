package driver

import (
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNothingASetupStartedOutlivesItsStop(t *testing.T) {
	dir := t.TempDir()
	conf := map[string]any{
		// The script ends once the sleeper runs sleep, so the log can name it.
		"script": "setsid sleep 300 >/dev/null 2>&1 &\necho $! >" + dir + "/sleeper\n" +
			"i=0; until tr '\\000' ' ' </proc/$!/cmdline | grep -q '^sleep 300' || [ $i -ge 500 ]; do i=$((i+1)); sleep 0.01; done",
		"stop": "echo the stop fails\nexit 1",
	}

	stop := make(chan struct{})
	close(stop)

	var log lockedLog

	err := setup(t.Context(), request(t, conf), func(map[string]string) error { return nil }, stop, &log)
	if err == nil || !strings.Contains(err.Error(), "stop") {
		t.Errorf("a stop that exits 1 didn't fail the set-up: %v", err)
	}

	// A killed process is gone once its reaper waited for it.
	deadline := time.Now().Add(5 * time.Second)
	for alive(t, dir+"/sleeper") && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}

	if alive(t, dir+"/sleeper") {
		t.Error("the set-up's setsid'd sleeper outlived its stop")
	}

	if !strings.Contains(log.String(), "shell-driver: the set-up left 1 process running when it ended: sleep 300") {
		t.Errorf("the log doesn't name what was stopped:\n%s", log.String())
	}
}

func TestASetupThatLeftNothingSaysNothing(t *testing.T) {
	stop := make(chan struct{})
	close(stop)

	var log lockedLog

	err := setup(t.Context(), request(t, map[string]any{"script": "true", "stop": "true"}), func(map[string]string) error { return nil }, stop, &log)
	if err != nil || strings.Contains(log.String(), "left") {
		t.Errorf("%v\n%s", err, log.String())
	}
}

func TestAForkingLeftoverLeavesNothing(t *testing.T) {
	// The loop ends by itself after five seconds and its sleepers after ten, so
	// not even a test process that dies leaves it behind.
	conf := map[string]any{
		"script": "setsid sh -c 'end=$(($(date +%s) + 5)); while [ $(date +%s) -lt $end ]; do setsid sleep 10 & done' >/dev/null 2>&1 &\nsleep 0.3",
		"stop":   "true",
	}

	t.Cleanup(func() { killDescendants(t) })

	stop := make(chan struct{})
	close(stop)

	var log lockedLog

	if err := setup(t.Context(), request(t, conf), func(map[string]string) error { return nil }, stop, &log); err != nil {
		t.Fatal(err)
	}

	if left := running(descendants(os.Getpid())); len(left) > 0 {
		t.Errorf("%d processes outlived the set-up's stop: %v\n%s", len(left), left, log.String())
	}
}

func killDescendants(t *testing.T) {
	t.Helper()

	for range 1000 {
		left := descendants(os.Getpid())
		if len(left) == 0 {
			return
		}

		for _, p := range left {
			_ = syscall.Kill(p, syscall.SIGKILL) //nolint:errcheck // gone already is stopped
		}

		for _, p := range left {
			var ws syscall.WaitStatus

			_, _ = syscall.Wait4(p, &ws, 0, nil) //nolint:errcheck // reaped already, or another's child
		}
	}

	t.Errorf("the test's processes outlived 1000 rounds of kills: %v", descendants(os.Getpid()))
}

func running(pids []int) []string {
	var out []string

	for _, p := range pids {
		if isRunning(p) {
			out = append(out, commandOf(p))
		}
	}

	return out
}
