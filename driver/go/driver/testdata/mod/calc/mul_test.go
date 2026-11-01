package calc

import "testing"

func TestMul(t *testing.T) {
	if Mul(2, 3) != 7 {
		t.Fatal("2*3 is not 7 here: this test fails on purpose")
	}
}
