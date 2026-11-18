// Package envs is a fixture of go-driver's tests: its tests write down
// the names of the variables they see.
package envs

import (
	"os"
	"sort"
	"strings"
)

// Note appends the variables' names, sorted, as one line to the file
// FIXTURE_ENV_LOG names.
func Note(test string) error {
	var names []string

	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		names = append(names, name)
	}

	sort.Strings(names)

	f, err := os.OpenFile(os.Getenv("FIXTURE_ENV_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}

	_, err = f.WriteString(test + " " + strings.Join(names, ",") + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}

	return err
}
