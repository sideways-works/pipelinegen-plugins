package driver

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	KeepDescendants()
	os.Exit(m.Run())
}
