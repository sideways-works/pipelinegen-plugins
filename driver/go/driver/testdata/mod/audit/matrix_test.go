package audit

import (
	"os"
	"testing"
)

// TestMatrix proves nothing without its database, so it skips.
func TestMatrix(t *testing.T) {
	if os.Getenv("AUDIT_DB") == "" {
		t.Skip("no database: AUDIT_DB is not set")
	}
}
