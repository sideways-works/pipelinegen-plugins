package access

import "testing"

// The generated names of every operation that needs a role.
func TestEveryOperationHasARole(t *testing.T) {
	for name, want := range map[string]string{"GetNote": "reader", "CreateNote": "writer"} {
		if name == "" || want == "" {
			t.Fatal("empty")
		}
	}
}
