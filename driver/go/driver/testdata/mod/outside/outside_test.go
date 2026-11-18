package outside

import (
	"os"
	"testing"
)

// TestOutside passes while a file outside the module says pass: state Go's
// test cache doesn't key on, as git history is.
func TestOutside(t *testing.T) {
	data, err := os.ReadFile("../../outside.txt")
	if err != nil || string(data) != "pass\n" {
		t.Fatalf("outside.txt: %q %v", data, err)
	}
}
