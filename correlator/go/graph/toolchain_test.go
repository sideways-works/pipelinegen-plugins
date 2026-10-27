package graph

import "testing"

func TestRelease(t *testing.T) {
	for v, want := range map[string]string{
		"go1.27.1":                "go1.27.1",
		"go1.27.4-X:nodwarf5":     "go1.27.4",
		"go1.27.1 X:boringcrypto": "go1.27.1",
	} {
		if got := release(v); got != want {
			t.Errorf("release(%q) = %q, want %q", v, got, want)
		}
	}
}
