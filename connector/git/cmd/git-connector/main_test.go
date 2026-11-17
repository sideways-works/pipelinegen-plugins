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

func TestChanges(t *testing.T) {
	in := `{"protocol": 1, "config": {"remote": "file:///srv/r.git", "branches": ["review/*"],
		"heads": {"review/a": "1111111111111111111111111111111111111111", "main": "2222222222222222222222222222222222222222"}},
		"args": {"base": "main"}}`

	out, code := serve(t, plugin.CallChanges, in)

	var r plugin.Round
	if err := json.Unmarshal([]byte(out), &r); code != 0 || err != nil || len(r.Requests) != 0 || !strings.Contains(string(r.Done), `"id":"branch-review%2Fa"`) {
		t.Fatalf("%d %v %s", code, err, out)
	}
}

func TestAStatusIsRefused(t *testing.T) {
	if _, code := serve(t, plugin.CallStatus, `{"protocol": 1, "args": {}}`); code == 0 {
		t.Fatal("answered")
	}
}
