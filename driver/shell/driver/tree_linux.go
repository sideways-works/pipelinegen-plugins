package driver

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// prSetChildSubreaper is PR_SET_CHILD_SUBREAPER.
const prSetChildSubreaper = 36

// KeepDescendants makes the driver the reaper of what its scripts leave
// running, so it can stop them after the script ends.
func KeepDescendants() {
	_, _, _ = syscall.RawSyscall(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0) //nolint:errcheck,dogsled // without it, only the scripts' own children are found
}

func descendants(pid int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}

	children := map[int][]int{}

	for _, e := range entries {
		p, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}

		data, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}

		// The command, in parentheses, may hold spaces.
		_, after, found := strings.CutLast(string(data), ")")
		if !found {
			continue
		}

		fields := strings.Fields(after)
		if len(fields) < 2 {
			continue
		}

		if ppid, err := strconv.Atoi(fields[1]); err == nil {
			children[ppid] = append(children[ppid], p)
		}
	}

	var out []int

	todo := []int{pid}
	for len(todo) > 0 {
		p := todo[0]
		todo = todo[1:]

		for _, c := range children[p] {
			out = append(out, c)
			todo = append(todo, c)
		}
	}

	return out
}

// commandOf retries while a process is mid-exec and has no command line.
func commandOf(pid int) string {
	for range 50 {
		data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
		if err != nil {
			break
		}

		if len(data) == 0 {
			if !isRunning(pid) {
				break
			}

			time.Sleep(time.Millisecond)

			continue
		}

		line := strings.TrimSpace(strings.ReplaceAll(string(data), "\x00", " "))
		if len(line) > 80 {
			line = line[:77] + "..."
		}

		return line
	}

	return "pid " + strconv.Itoa(pid)
}

func isRunning(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}

	_, after, found := strings.CutLast(string(data), ")")

	return found && !strings.HasPrefix(strings.TrimSpace(after), "Z")
}
