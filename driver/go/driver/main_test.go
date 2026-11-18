package driver

import (
	"fmt"
	"os"
	"testing"
)

// TestMain is the driver's exec-test verb when test2json runs the test binary
// as the driver.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == ExecVerb {
		fmt.Fprintln(os.Stderr, ExecTest(os.Args[2:]))

		os.Exit(1)
	}

	os.Exit(m.Run())
}
