package runstep

import (
	"flag"
	"path/filepath"
)

// sweep runs none of the changes' code. The planning job misses a head the
// pull requests' check orders behind ones already planned; the sweep plans
// it, and hands on planned heads whose suites never began after
// missed_after. The gate opens when the list wrote either directory.
func (t *Task) sweep([]string) int {
	run, ok := t.run()
	if !ok {
		return 2
	}

	if code := t.call(t.Env, tool, "unplanned", "--run", run, "--root", "config", "--out", "sweep", "--cache", "write-cache"); code != 0 {
		return code
	}

	gate := "[]\n"
	if t.exists("sweep/plan") || t.exists("sweep/handoff") {
		gate = `["1"]` + "\n"
	}

	return t.writeFile("gate/gate.json", gate)
}

// sweepPlan runs the changes' code, so it holds no credential that writes;
// the prepare command runs the base's scripts. A head that can't be planned
// leaves the others theirs; the next sweep tries it again.
func (t *Task) sweepPlan([]string) int {
	for _, pr := range t.dirs("sweep/plan") {
		if code := t.remove("sweep/head", "plans/head"); code != 0 {
			return code
		}

		if code := t.move(filepath.Join("sweep/plan", pr), "sweep/head"); code != 0 {
			return code
		}

		if t.planHead() {
			if code := t.move("plans/head", filepath.Join("plans", pr)); code != 0 {
				return code
			}

			continue
		}

		t.sayf("the sweep: pull request %s: not planned", pr)
	}

	return 0
}

func (t *Task) planHead() bool {
	if t.prepare(t.Env, "base") != 0 {
		return false
	}

	run, ok := t.run()

	return ok && t.call(t.withoutPlanner(t.Env), tool, planCmd, "--run", run, "--trigger", "sweep/head", "--root", "sweep/head",
		"--cache", "plan-cache", "--out", "plans/head") == 0
}

// sweepWrite runs none of the changes' code. It hands on each head whose
// plan it wrote, and each planned head whose suites never began; a shut
// gate means planned meanwhile by its planning job, or left out.
func (t *Task) sweepWrite(args []string) int {
	fs := flag.NewFlagSet("sweep-write", flag.ContinueOnError)
	fs.SetOutput(t.Stderr)
	connector := fs.Bool("connector", false, "hand on a connector's run.json")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	handoff := t.handoff
	if *connector {
		handoff = t.handoffRun
	}

	var list []string

	for _, pr := range t.dirs("plans") {
		if code := t.remove("sweep/head", "plans/head"); code != 0 {
			return code
		}

		if code := t.move(filepath.Join("sweep/plan", pr), "sweep/head"); code != 0 {
			return code
		}

		if code := t.move(filepath.Join("plans", pr), "plans/head"); code != 0 {
			return code
		}

		if code := t.remove("handoffs/.gate"); code != 0 {
			return code
		}

		run, ok := t.run()
		if !ok {
			return 2
		}

		switch {
		case t.call(t.Env, tool, planCmd, "--run", run, "--trigger", "sweep/head", "--root", "sweep/head",
			"--cache", "write-cache", "--from", "plans/head", "--gate", "handoffs/.gate") != 0:
			t.sayf("the sweep: pull request %s: its plan isn't written", pr)
		case t.holds("handoffs/.gate/gate.json", `"1"`):
			if code := handoff("sweep/head", filepath.Join("handoffs", pr)); code != 0 {
				return code
			}

			list = append(list, pr)
		}
	}

	if code := t.remove("handoffs/.gate"); code != 0 {
		return code
	}

	for _, pr := range t.dirs("sweep/handoff") {
		if code := handoff(filepath.Join("sweep/handoff", pr), filepath.Join("handoffs", pr)); code != 0 {
			return code
		}

		list = append(list, pr)
	}

	if code := t.writeFile("handoffs/list.json", "["+quoted(list)+"]\n"); code != 0 {
		return code
	}

	t.outf("the sweep hands off [%s]", quoted(list))

	return 0
}
