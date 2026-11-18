// Package kid is a fixture of go-driver's tests: its test runs its own
// binary as a child with a cut-down environment, whose coverage must
// reach the test's recording.
package kid

// Answer is what only the child runs.
func Answer() int { return 42 }
