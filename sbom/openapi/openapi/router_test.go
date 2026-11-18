package openapi

import (
	"os"
	"path/filepath"
	"testing"
)

const routes = `openapi: 3.0.3
servers:
  - url: https://notes.example/api
paths:
  /notes:
    get: {operationId: listNotes, responses: {"200": {description: ok}}}
    post: {operationId: createNote, responses: {"201": {description: ok}}}
  /notes/{id}:
    get: {operationId: getNote, responses: {"200": {description: ok}}}
  /notes/drafts:
    get: {operationId: listDrafts, responses: {"200": {description: ok}}}
  /notes/{id}.txt:
    get: {operationId: getNoteText, responses: {"200": {description: ok}}}
`

func TestRouter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "api.yaml"), []byte(routes), 0o600); err != nil {
		t.Fatal(err)
	}

	m, err := Build(dir, []Document{{Path: "api.yaml"}}, Options{})
	if err != nil {
		t.Fatal(err)
	}

	r := m.NewRouter(nil)

	for _, c := range []struct{ method, url, want string }{
		{"GET", "/api/notes", "api:listNotes"},
		{"post", "/api/notes", "api:createNote"},
		{"GET", "/api/notes/n1", "api:getNote"},
		{"GET", "/api/notes/drafts", "api:listDrafts"},
		{"GET", "/api/notes/n1.txt", "api:getNoteText"},
		{"GET", "https://notes.example/api/notes/n1?full=1", "api:getNote"},
		{"DELETE", "/api/notes/n1", ""},
		{"GET", "/notes/n1", ""},
		{"GET", "/apinotes", ""},
		{"GET", "/static/app.js", ""},
	} {
		got, _ := r.Match(c.method, c.url)
		if got != c.want {
			t.Errorf("%s %s = %q, want %q", c.method, c.url, got, c.want)
		}
	}

	if got, _ := m.NewRouter(map[string]string{"api.yaml": "/v2"}).Match("GET", "/v2/notes"); got != "api:listNotes" {
		t.Errorf("with a base: %q", got)
	}
}
