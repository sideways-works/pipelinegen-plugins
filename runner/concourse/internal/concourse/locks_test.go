package concourse

import (
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

// An older tool's spec may still hold a lock: the pipeline takes none, and
// the answer says so.
func TestAnOlderSpecsLockTakesNoStep(t *testing.T) {
	for _, per := range []string{"", "batch"} {
		spec, root := fixture(t)
		for i := range spec.Suites {
			spec.Suites[i].Lock = &plugin.SpecLock{Name: "lock-" + spec.Suites[i].Name, Minutes: 30, Slots: 2, Per: per}
		}

		p, err := Generate(spec, root)
		if err != nil {
			t.Fatal(err)
		}

		s := text(t, p)
		for _, unwanted := range []string{"pipelinegen lock", "pipelinegen unlock", "- lock\n", "task: lock", "task: unlock", "PIPELINEGEN_LOCK", "begun", "--started"} {
			if strings.Contains(s, unwanted) {
				t.Errorf("per %q: the pipeline has %q", per, unwanted)
			}
		}

		plain, root := fixture(t)

		want, err := Generate(plain, root)
		if err != nil {
			t.Fatal(err)
		}

		if s != text(t, want) {
			t.Errorf("per %q: the lock changed the pipeline", per)
		}

		notices := strings.Join(Notices(spec, nil), "\n")
		for _, su := range spec.Suites {
			if !strings.Contains(notices, "suite "+su.Name+": the spec holds a lock, which this runner no longer takes") {
				t.Errorf("per %q: no notice for %s in %q", per, su.Name, notices)
			}
		}
	}

	if n := Notices(fixtureSpec(t), nil); len(n) != 0 {
		t.Errorf("a spec without a lock: %v", n)
	}

	if d := describe(); slices.ContainsFunc(d.Capabilities, func(c string) bool { return strings.Contains(c, "lock") }) {
		t.Errorf("describe still offers a lock: %v", d.Capabilities)
	}
}

func fixtureSpec(t *testing.T) plugin.Spec {
	t.Helper()

	s, _ := fixture(t)

	return s
}
