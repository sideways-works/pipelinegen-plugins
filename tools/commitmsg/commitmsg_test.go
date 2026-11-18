package commitmsg

import (
	"errors"
	"strings"
	"testing"
)

func TestGoodMessagesPass(t *testing.T) {
	for _, msg := range []string{
		"fix(driver/go): a bundle's tests see go test's environment",
		"docs: the README names each plugin's targets\n",
		"feat(templates): a sbom template that passes the kit\n\n",
		"chore: x",
		"build: pin bazel 9.2.0\n# Please enter the commit message for your changes.\n#\n",
		"test: " + strings.Repeat("x", MaxSubject-len("test: ")),
	} {
		if err := Check(msg); err != nil {
			t.Errorf("%q: %v", msg, err)
		}
	}
}

func TestBadMessagesFail(t *testing.T) {
	for _, msg := range []string{
		"",
		"# only a comment\n",
		"test: " + strings.Repeat("x", MaxSubject-len("test: ")+1),
		"Fix the build",
		"fixed: the build",
		" fix: space before",
		"fix: a subject\n\nand a body",
		"fix: a subject\n\nCo-Authored-By: someone",
	} {
		if err := Check(msg); !errors.Is(err, ErrMessage) {
			t.Errorf("%q: got %v, want a refusal", msg, err)
		}
	}
}
