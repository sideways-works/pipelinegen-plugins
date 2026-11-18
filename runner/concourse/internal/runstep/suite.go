package runstep

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
)

// start works out the suite's tree and gate, and deals its batches when the
// gate opens. Nothing here starts Bazel.
func (t *Task) start([]string) int {
	if !t.must("PIPELINEGEN_RUN", "PIPELINEGEN_SUITE") {
		return 2
	}

	run, suite := t.getenv("PIPELINEGEN_RUN"), t.getenv("PIPELINEGEN_SUITE")

	if code := t.call(t.Env, tool, "tree", "--run", run, "--suite", suite, "--trigger", "trigger", "--out", "source", "--gate", "gate"); code != 0 {
		return code
	}

	if !t.holds("gate/gate.json", `"1"`) {
		return t.writeFile("batches/batches.json", "[]\n")
	}

	return t.last(t.Env, "batches", "--run", run, "--suite", suite, "--trigger", "trigger", "--out", "batches", "--root", "source")
}

// exitUnrecorded is a report's exit when the run's records could not be
// written: it alone fails the job, as the suite's statuses show any other
// failure.
const exitUnrecorded = 4

// end runs in the job's ensure, on an abort too. A build aborted while its
// start task waited for a worker never had the tools: fetching them here
// still ends its suite, so a continuous run's next round need not wait for
// it.
func (t *Task) end([]string) int {
	if !t.isDir("trigger") {
		t.sayf("no trigger: the build broke before it could report")

		return 0
	}

	if !t.executable(tool) {
		t.sayf("no tools: the build broke before its start task had them; fetching them to end the suite")

		if code := t.fetchTools(t.Env); code != 0 || !t.executable(tool) {
			t.sayf("the tools could not be fetched (exit %d): the build broke before it could report", code)

			// the job's on_error or on_abort hook already posted its statuses
			return 0
		}
	}

	switch code := t.call(t.Env, tool, "report", "suite", "--root", "trigger", "--batches", "batches"); code {
	case 0:
	case exitUnrecorded:
		return exitUnrecorded
	default:
		t.sayf("the suite's end exited %d; its statuses say why", code)
	}

	return 0
}

var versionCommit = regexp.MustCompile(`^.*"commit":"([0-9a-f]*)".*$`)

// reportError runs in a broken or aborted build's hooks. With its tools and
// trigger the tool ends the suite, so the run's round can end; otherwise
// the suite's and the run's statuses are posted as errored, and the end
// task, which runs next, fetches the tools to end it.
func (t *Task) reportError([]string) int {
	if t.executable(tool) && t.isDir("trigger") {
		code := t.call(t.Env, tool, "report", "suite", "--root", "trigger", "--batches", "batches")

		switch code {
		case 0, 1:
			return 0
		case exitUnrecorded:
			return exitUnrecorded
		}

		t.sayf("the suite's end exited %d: posting its status as errored", code)
	}

	commit := ""

	switch {
	case t.isFile("trigger/.git/resource/version.json"):
		data, err := os.ReadFile(t.path("trigger/.git/resource/version.json"))
		if err != nil {
			t.sayf("%v", err)

			return 1
		}

		flat := strings.NewReplacer("\n", "", " ", "").Replace(string(data))
		if m := versionCommit.FindStringSubmatch(flat); m != nil {
			commit = m[1]
		}
	case t.isDir("trigger/.git"):
		var out bytes.Buffer

		cmd := t.command(t.Env, t.Dir, "git", "-C", "trigger", "rev-parse", "HEAD")
		cmd.Stdout = &out

		if code := t.exited("git", cmd.Run()); code != 0 {
			return code
		}

		commit = strings.TrimRight(out.String(), "\n")
	}

	if commit == "" {
		t.sayf("no trigger: nothing to post to")

		return 0
	}

	if !t.must("REPOSITORY", "SUITE_CONTEXT", "RUN_CONTEXT", "TARGET_URL") {
		return 2
	}

	url := t.api() + "/repos/" + t.getenv("REPOSITORY") + "/statuses/" + commit

	for _, context := range []string{t.getenv("SUITE_CONTEXT"), t.getenv("RUN_CONTEXT")} {
		if code := t.postStatus(url, context); code != 0 {
			return code
		}
	}

	return 0
}

func (t *Task) api() string {
	if api := t.getenv("PIPELINEGEN_GITHUB_API"); api != "" {
		return api
	}

	return "https://api.github.com"
}

// bearer is the authorization header's value: the token comes from the
// environment and is never an argument of a command.
func (t *Task) bearer(purpose string) string {
	token := t.getenv(purpose)
	if token == "" {
		token = t.getenv(githubToken)
	}

	return "Bearer " + token
}

type status struct {
	State       string `json:"state"`
	Context     string `json:"context"`
	Description string `json:"description"`
	TargetURL   string `json:"target_url"`
}

func (t *Task) postStatus(url, context string) int {
	var body bytes.Buffer

	enc := json.NewEncoder(&body)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(status{
		State: "error", Context: context, Description: "the build broke before it could report", TargetURL: t.getenv("TARGET_URL"),
	}); err != nil {
		t.sayf("%v", err)

		return 1
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(bytes.TrimSuffix(body.Bytes(), []byte("\n")))) //nolint:noctx // a task's limit ends it
	if err != nil {
		t.sayf("%v", err)

		return 1
	}

	req.Header.Set("Authorization", t.bearer("PIPELINEGEN_REPORT_TOKEN"))
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := *t.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	return t.requested(&client, req, nil)
}

// requested sends a request and, when to is given, reads the answer into
// it. A failure exits as curl -f did: 22 for an answer of 400 or more, 6
// for a host it can't resolve, 7 for one it can't reach.
func (t *Task) requested(client *http.Client, req *http.Request, to *bytes.Buffer) int {
	resp, err := client.Do(req)
	if err != nil {
		t.sayf("%s %s: %v", req.Method, req.URL.Redacted(), err)

		if dns, ok := errors.AsType[*net.DNSError](err); ok && dns != nil {
			return 6
		}

		return 7
	}

	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		t.sayf("%s %s: %s", req.Method, req.URL.Redacted(), resp.Status)

		return 22
	}

	if to == nil {
		return 0
	}

	if _, err := to.ReadFrom(resp.Body); err != nil {
		t.sayf("%s %s: %v", req.Method, req.URL.Redacted(), err)

		return 1
	}

	return 0
}

// out prints a line on the task's stdout.
func (t *Task) outf(format string, args ...any) {
	fmt.Fprintf(t.Stdout, format+"\n", args...) //nolint:errcheck // a log line that can't be written can't be reported either
}
