package driver

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func cutRecording(dir string) {
	profiles, err := filepath.Glob(filepath.Join(dir, "*.coverprofile"))
	if err != nil {
		return
	}

	for _, p := range profiles {
		_ = cutProfile(p) //nolint:errcheck // a profile left whole is still the slice's recording
	}
}

// cutProfile keeps the mode line, every block that ran, and the first block
// of each file none of whose blocks ran: a full profile of a large module is
// megabytes of zeros. Every file keeps a block, so a reader still knows
// which files the coverage holds.
func cutProfile(p string) error {
	data, err := os.ReadFile(p) //nolint:gosec // the slice's own recording
	if err != nil {
		return fmt.Errorf("profile: %w", err)
	}

	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "mode:") {
		return nil
	}

	ran := map[string]bool{}

	type block struct {
		file string
		ran  bool
	}

	blocks := make([]block, 0, len(lines)-1)

	for _, line := range lines[1:] {
		file, rest, ok := strings.CutLast(line, ":")
		f := strings.Fields(rest)

		if !ok || len(f) != 3 {
			return nil
		}

		b := block{file: file, ran: f[2] != "0"}
		ran[file] = ran[file] || b.ran
		blocks = append(blocks, b)
	}

	var (
		out  bytes.Buffer
		kept = map[string]bool{}
	)

	out.WriteString(lines[0] + "\n")

	for i, b := range blocks {
		if b.ran || (!ran[b.file] && !kept[b.file]) {
			out.WriteString(lines[i+1] + "\n")

			kept[b.file] = true
		}
	}

	if out.Len() == len(data) {
		return nil
	}

	tmp := p + ".cut"
	if err := os.WriteFile(tmp, out.Bytes(), 0o600); err != nil {
		return fmt.Errorf("profile: %w", err)
	}

	if err := os.Rename(tmp, p); err != nil {
		return fmt.Errorf("profile: %w", err)
	}

	return nil
}
