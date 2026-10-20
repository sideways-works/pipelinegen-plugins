package openapi

import (
	"encoding/json"
	"regexp"
	"testing"
	"unicode/utf8"
)

func TestLabels(t *testing.T) {
	m := buildSplit(t, splitTree(t))

	got := map[string]string{}
	for _, u := range m.Units {
		got[u.ID] = u.Label

		if u.Label == "" || utf8.RuneCountInString(u.Label) > MaxLabel {
			t.Errorf("%s: label %q", u.ID, u.Label)
		}
	}

	for id, want := range map[string]string{
		"api:getNote": "GET /notes/{id} (getNote)",
		"api:shared/common.yaml#/components/schemas/Problem":       "schema Problem in shared/common.yaml",
		"api:shared/common.yaml#/components/requestBodies/NewNote": "request body NewNote in shared/common.yaml",
		"api:api.yaml#/paths": "the routes of api.yaml",
		"api:api.yaml":        "the document api.yaml",
		"api:docs:getNote":    "the text of GET /notes/{id} (getNote)",
	} {
		if got[id] != want {
			t.Errorf("%s: label %q, want %q", id, got[id], want)
		}
	}

	defer func(f func(Unit, map[string]Operation) string) { labeler = f }(labeler)

	labeler = func(Unit, map[string]Operation) string { return "x" }
	other := buildSplit(t, splitTree(t))

	strip := func(m *Model) string {
		b, err := json.Marshal(map[string]any{"units": m.Units, "links": m.Links})
		if err != nil {
			t.Fatal(err)
		}

		return regexp.MustCompile(`"label":"[^"]*",?`).ReplaceAllString(string(b), "")
	}

	if strip(m) != strip(other) {
		t.Error("an answer changes with its labels")
	}
}
