package main

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestDescribeSaysTheVersion(t *testing.T) {
	defer func(v string) { Version = v }(Version)

	Version = "v1.2.3"

	if got := describe().Version; got != "v1.2.3" {
		t.Errorf("describe says version %q, want v1.2.3", got)
	}
}

// The release build sets the tag into a package-level `var Version = "dev"`
// line only, so the declaration keeps exactly that form.
func TestTheReleaseBuildFindsTheVersion(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(strings.Split(string(src), "\n"), `var Version = "dev"`) {
		t.Error(`main.go has no line var Version = "dev" for the release build to set`)
	}
}
