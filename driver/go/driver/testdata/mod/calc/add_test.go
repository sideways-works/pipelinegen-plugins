package calc

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatal("add")
	}
}

func TestAddSub(t *testing.T) {
	t.Run("zero", func(t *testing.T) {
		if Add(0, 0) != 0 {
			t.Fatal("zero")
		}
	})
}
