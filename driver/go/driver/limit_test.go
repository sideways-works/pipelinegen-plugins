package driver

import (
	"testing"
	"time"
)

func TestTheLimitIsPerFile(t *testing.T) {
	j := &job{limit: 10 * time.Minute}

	if got := j.timeout(3); got != 30*time.Minute {
		t.Errorf("three files: %s", got)
	}

	j.until = time.Now().Add(5 * time.Minute)

	if got := j.timeout(3); got > 5*time.Minute || got < 4*time.Minute {
		t.Errorf("three files before a deadline in 5 minutes: %s", got)
	}

	j = &job{until: time.Now().Add(-time.Minute)}

	if got := j.timeout(1); got != time.Second {
		t.Errorf("past the deadline: %s", got)
	}

	if got := (&job{}).timeout(4); got != 0 {
		t.Errorf("no limit and no deadline: %s", got)
	}
}
