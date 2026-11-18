package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func serve(t *testing.T, call, in string) (string, int) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	code := plugin.Serve([]string{call}, strings.NewReader(in), &stdout, &stderr, handlers())

	return stdout.String(), code
}

func TestDescribe(t *testing.T) {
	out, code := serve(t, plugin.CallDescribe, `{"protocol": 1}`)

	var d plugin.Description
	if err := json.Unmarshal([]byte(out), &d); code != 0 || err != nil || d.Kind != plugin.KindConnector || d.Version != Version {
		t.Fatalf("%d %v %+v", code, err, d)
	}
}

func TestIdentify(t *testing.T) {
	out, code := serve(t, plugin.CallIdentify, `{"protocol": 1, "args": {"remote": "git@github.com:team/app.git"}}`)

	var r plugin.Round
	if err := json.Unmarshal([]byte(out), &r); code != 0 || err != nil || len(r.Requests) != 0 || !strings.Contains(string(r.Done), `"repository":"team/app"`) {
		t.Fatalf("%d %v %s", code, err, out)
	}
}

func TestABodyOutsideTheWorkIsRefused(t *testing.T) {
	in := `{"protocol": 1, "work": "` + t.TempDir() + `", "config": {"remote": "https://github.com/team/app.git"},
		"args": {"commit": "abc"}, "responses": [{"id": "commit", "status": 200, "body_file": "../../etc/passwd"}]}`

	if _, code := serve(t, plugin.CallSubject, in); code == 0 {
		t.Fatal("answered")
	}
}
