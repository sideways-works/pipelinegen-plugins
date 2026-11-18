package pgid

import (
	"strings"
	"testing"
)

func TestIDs(t *testing.T) {
	for got, want := range map[string]string{
		Of(Table, "public", "notes"):                     "pg:table:public.notes",
		Of(Relation, "app", "Notes"):                     "pg:relation:app.Notes",
		Func("public", "f", []string{"integer", "text"}): "pg:function:public.f(integer, text)",
		Func("public", "touch", nil):                     "pg:function:public.touch()",
		Routine("public", "f"):                           "pg:routine:public.f",
		Ext("citext"):                                    "pg:extension:citext",
	} {
		if got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	}
}

func TestParse(t *testing.T) {
	kind, rest, ok := Parse("pg:function:public.f(integer)")
	if !ok || kind != Function || rest != "public.f(integer)" {
		t.Fatalf("got %q %q %v", kind, rest, ok)
	}

	if _, _, ok := Parse("go:pkg#func:F"); ok {
		t.Fatal("parsed an id of another namespace")
	}
}

func TestLabel(t *testing.T) {
	for id, want := range map[string]string{
		"pg:table:public.bookings":            "the table bookings",
		"pg:view:app.active_users":            "the view app.active_users",
		"pg:function:public.f(integer, text)": "the function f(integer, text)",
		"pg:data:public.bookings":             "the rows of table bookings",
		"pg:relation:public.bookings":         "the table or view bookings",
		"pg:routine:public.f":                 "every overload of f",
		"pg:extension:citext":                 "the extension citext",
	} {
		if got := Label(id); got != want {
			t.Errorf("Label(%s) = %q, want %q", id, got, want)
		}
	}

	if got := Label("pg:table:public." + strings.Repeat("x", 200)); len([]rune(got)) != MaxLabel {
		t.Errorf("a long label is %d long", len([]rune(got)))
	}
}
