package npm

import (
	"strings"
	"unicode/utf8"
)

// MaxLabel is the longest label: one line of pipelinegen's why.
const MaxLabel = 80

// labelOf is a variable so a test can show that nothing else in an answer
// depends on it.
var labelOf = func(u Unit) string {
	versions := strings.Join(u.Attributes.Versions, ", ")

	if name, ok := strings.CutPrefix(u.ID, Namespace+"toolchain:"); ok {
		return clip("the toolchain " + name + " " + versions)
	}

	name := strings.TrimPrefix(u.ID, Namespace)
	if versions == "" {
		return clip("the package " + name)
	}

	return clip("the package " + name + "@" + versions)
}

func clip(s string) string {
	if utf8.RuneCountInString(s) <= MaxLabel {
		return s
	}

	r := []rune(s)
	half := (MaxLabel - 1) / 2

	return string(r[:half]) + "…" + string(r[len(r)-(MaxLabel-1-half):])
}
