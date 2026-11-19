package concourse

import (
	"testing"
)

type outcome string

const (
	succeeded outcome = "succeeded"
	failed    outcome = "failed"
	errored   outcome = "errored"
	aborted   outcome = "aborted"
	// timedOut is a task stopped by its own timeout, which Concourse shows as errored but whose Run returns
	// false and no error (exec.TaskStep), so for the steps around it the task failed.
	timedOut outcome = "timed out"
)

// evalStep runs a step by the rules of Concourse's atc/exec: a task's outcome is run's; a do stops at the
// first step that doesn't succeed; a try succeeds unless its step aborted (TryStep); an on_error hook runs
// when the step errored and leaves the step's outcome as it was (OnErrorStep); an ensure runs whatever the
// step did, and the step's outcome stands unless it succeeded and the hook didn't (EnsureStep).
func evalStep(t *testing.T, step M, run func(task M) outcome) outcome {
	t.Helper()

	var out outcome

	switch {
	case step["try"] != nil:
		if out = evalStep(t, asMap(step["try"]), run); out != aborted {
			out = succeeded
		}
	case step["do"] != nil:
		out = succeeded

		for _, s := range asSlice(step["do"]) {
			if out = evalStep(t, asMap(s), run); out != succeeded {
				break
			}
		}
	case step["task"] != nil:
		if out = run(step); out == timedOut {
			if step["timeout"] == nil {
				t.Fatalf("task %v timed out without a timeout of its own", step["task"])
			}

			out = failed
		}
	default:
		t.Fatalf("a step the evaluator doesn't know: %v", step)
	}

	if hook, ok := step["on_error"].(M); ok && out == errored {
		evalStep(t, hook, run)
	}

	if hook, ok := step["ensure"].(M); ok {
		if h := evalStep(t, hook, run); out == succeeded {
			out = h
		}
	}

	return out
}

// batchIteration is the step across runs once per batch in a suite job.
func batchIteration(t *testing.T, job string) M {
	t.Helper()

	spec, root := fixture(t)

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	var iteration M

	walkSteps(jobByName(t, p, job), func(step M) {
		if step["across"] != nil && step["fail_fast"] == false {
			iteration = M{"do": step["do"]}
		}
	})

	if iteration == nil {
		t.Fatalf("%s has no step across its batches", job)
	}

	return iteration
}

func TestAnErroredBatchIsRetriedOnceAndAFailedOneNever(t *testing.T) {
	for _, job := range []string{"main-backend", "main-e2e"} {
		for _, c := range []struct {
			name     string
			attempts []outcome
			want     outcome
		}{
			{"errored, then passed", []outcome{errored, succeeded}, succeeded},
			{"errored, then failed", []outcome{errored, failed}, failed},
			{"failed", []outcome{failed}, failed},
			{"passed", []outcome{succeeded}, succeeded},
			{"errored twice", []outcome{errored, errored}, failed},
			{"aborted", []outcome{aborted}, aborted},
			{"timed out", []outcome{timedOut}, failed},
			{"errored, then timed out", []outcome{errored, timedOut}, failed},
		} {
			ran := 0
			last := outcome("")

			got := evalStep(t, batchIteration(t, job), func(task M) outcome {
				switch task["task"] {
				case "batch":
					if ran == len(c.attempts) {
						t.Fatalf("%s %s: the batch ran a %d. time", job, c.name, ran+1)
					}

					last = c.attempts[ran]
					ran++

					return last
				case "batch-end":
					// the end passes only when the last attempt's slices passed: a failed test, or a batch that
					// never ran, leaves slices that didn't pass
					if last == succeeded {
						return succeeded
					}

					if last == aborted {
						return aborted
					}

					return failed
				}

				t.Fatalf("an unexpected task %v", task["task"])

				return ""
			})

			if got != c.want || ran != len(c.attempts) {
				t.Errorf("%s %s: the batch's step %s after %d attempts, want %s after %d", job, c.name, got, ran, c.want, len(c.attempts))
			}
		}
	}
}

func TestTheRetryIsTheSameTask(t *testing.T) {
	iteration := batchIteration(t, "main-e2e")
	step := asMap(asSlice(iteration["do"])[0])
	try := asMap(step["try"])
	again := asMap(try["on_error"])

	for _, k := range []string{"task", "file", "params", "tags", "timeout", "image"} {
		if text(t, M{"v": try[k]}) != text(t, M{"v": again[k]}) {
			t.Errorf("the retry's %s is %v, the batch's %v", k, again[k], try[k])
		}
	}

	if again["on_error"] != nil || again["attempts"] != nil || try["attempts"] != nil || step["on_abort"] != nil {
		t.Errorf("the retry runs more than once more, or on an abort: %v", step)
	}
}
