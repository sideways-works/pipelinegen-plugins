package text

import "testing"

func TestUpper(t *testing.T) {
	if Upper("a") != "A" {
		t.Fatal("upper")
	}
}
