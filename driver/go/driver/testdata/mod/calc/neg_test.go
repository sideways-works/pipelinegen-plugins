package calc

//ci:alone

import "testing"

func TestNeg(t *testing.T) {
	if Neg(1) != -1 {
		t.Fatal("neg")
	}
}
