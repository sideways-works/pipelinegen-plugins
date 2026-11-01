package envs

import "testing"

func TestB(t *testing.T) {
	if err := Note("TestB"); err != nil {
		t.Fatal(err)
	}
}
