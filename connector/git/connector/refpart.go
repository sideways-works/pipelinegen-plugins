package connector

import (
	"fmt"
	"strings"
)

// RefPart encodes byte for byte as pipelinegen names its records: a change id is part
// of every record's ref, so another encoding would lose a change's earlier runs.
func RefPart(part string) string {
	var b strings.Builder

	for j := range len(part) {
		c := part[j]

		dot := c == '.' && (j == 0 || j == len(part)-1 || part[j-1] == '.' || strings.HasPrefix(part[j:], ".lock") && j+5 == len(part))
		if refByte(c) && !dot {
			b.WriteByte(c)

			continue
		}

		fmt.Fprintf(&b, "%%%02X", c)
	}

	return b.String()
}

func refByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.IndexByte("-_.+,", b) >= 0
}
