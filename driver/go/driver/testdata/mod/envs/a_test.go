package envs

import "testing"

func TestA(t *testing.T) {
	if err := Note("TestA"); err != nil {
		t.Fatal(err)
	}
}
