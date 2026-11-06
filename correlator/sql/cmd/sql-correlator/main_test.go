package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func call(t *testing.T, name, req string) (int, string, string) {
	t.Helper()

	var out, errs bytes.Buffer

	code := run([]string{name}, strings.NewReader(req), &out, &errs)

	return code, out.String(), errs.String()
}

func TestDescribe(t *testing.T) {
	code, out, _ := call(t, "describe", "")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}

	var d struct {
		Kind         string   `json:"kind"`
		Capabilities []string `json:"capabilities"`
	}

	if err := json.Unmarshal([]byte(out), &d); err != nil || d.Kind != "correlator" {
		t.Fatalf("%s %v", out, err)
	}
}

func TestCollectAndObserve(t *testing.T) {
	log, err := filepath.Abs("../../testdata/server.json")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	cfg := `{"log":"` + log + `","setup_databases":["*_template"],"databases":["notes_test_*"],"quiet":"10ms"}`

	if code, _, errs := call(t, "collect", `{"protocol":1,"config":`+cfg+`,"action":"setup-end","dir":"`+dir+`"}`); code != 0 {
		t.Fatalf("collect: exit %d: %s", code, errs)
	}

	setup := filepath.Join(t.TempDir(), "setup")

	code, out, errs := call(t, "observe", `{"protocol":1,"config":`+cfg+`,"slice":"all","dirs":["`+setup+`","`+dir+`"]}`)
	if code != 0 {
		t.Fatalf("observe: exit %d: %s", code, errs)
	}

	for _, id := range []string{"pg:relation:public.notes", "pg:data:public.tags", "pg:routine:public.note_score"} {
		if !strings.Contains(out, `"`+id+`"`) {
			t.Errorf("%s missing from %s", id, out)
		}
	}
}

func TestCannotTellExitsThree(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "statements.jsonl"), []byte(`{"text":"what ( is this","count":1}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if code, _, _ := call(t, "observe", `{"protocol":1,"dirs":["`+filepath.Join(t.TempDir(), "setup")+`","`+dir+`"]}`); code != plugin.ExitCantTell {
		t.Fatalf("exit %d", code)
	}
}

func TestBadRequests(t *testing.T) {
	if code, _, _ := call(t, "observe", "not json"); code != 1 {
		t.Errorf("bad json: exit %d", code)
	}

	if code, _, _ := call(t, "collect", `{"protocol":1,"config":{"logs":"x"}}`); code != 1 {
		t.Errorf("unknown config key: exit %d", code)
	}

	if code, _, _ := call(t, "collect", `{"protocol":1,"config":{"log":"x"},"action":"later"}`); code != 1 {
		t.Errorf("unknown action: exit %d", code)
	}

	if code, _, _ := call(t, "frobnicate", `{}`); code != 1 {
		t.Errorf("unknown call: exit %d", code)
	}
}

func TestACutLogEndsInCantTell(t *testing.T) {
	logDir, dir := t.TempDir(), t.TempDir()
	log := filepath.Join(logDir, "postgresql.json")

	if err := os.WriteFile(log, []byte(`al_count": 3}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := `{"log":"` + logDir + `","quiet":"10ms"}`

	if code, _, errs := call(t, "collect", `{"protocol":1,"config":`+cfg+`,"action":"setup-end","dir":"`+dir+`"}`); code != 0 {
		t.Fatalf("collect: exit %d: %s", code, errs)
	}

	code, _, errs := call(t, "observe", `{"protocol":1,"config":`+cfg+`,"slice":"s","dirs":["`+filepath.Join(t.TempDir(), "setup")+`","`+dir+`"]}`)
	if code != plugin.ExitCantTell || !strings.Contains(errs, "postgresql.json: line 1 (byte 0): not a PostgreSQL JSON log line (log_destination must be jsonlog)") {
		t.Errorf("observe: exit %d: %s", code, errs)
	}
}
