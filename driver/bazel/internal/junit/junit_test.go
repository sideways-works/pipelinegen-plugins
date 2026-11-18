package junit

import (
	"strings"
	"testing"
)

func TestAFailingGoTest(t *testing.T) {
	cases, err := ReadFile("testdata/failing.xml")
	if err != nil {
		t.Fatal(err)
	}

	if len(cases) != 1 || cases[0].Name != "TestBad" || !cases[0].Failed || !strings.Contains(cases[0].Message, "the answer is not 42") {
		t.Errorf("%+v", cases)
	}
}

// rules_go writes an empty suite list under coverage.
func TestAnEmptyFile(t *testing.T) {
	cases, err := Read([]byte("<testsuites></testsuites>"))
	if err != nil || len(cases) != 0 {
		t.Errorf("%v %v", cases, err)
	}
}
