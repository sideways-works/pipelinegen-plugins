package openapi

import (
	"strings"
	"unicode"
)

// separators are the characters oapi-codegen's default name normaliser
// treats as word breaks.
const separators = "-#@!$&=.+:;_~ (){}[]"

// GoName is the name oapi-codegen's default normaliser gives an operation's
// methods and types.
func GoName(operationID string) string {
	var b strings.Builder

	capNext := true

	for _, r := range strings.Trim(operationID, " ") {
		switch {
		case unicode.IsUpper(r), unicode.IsDigit(r):
			b.WriteRune(r)
		case unicode.IsLower(r):
			if capNext {
				b.WriteRune(unicode.ToUpper(r))
			} else {
				b.WriteRune(r)
			}
		}

		capNext = strings.ContainsRune(separators, r)
	}

	name := b.String()

	switch {
	case name == "":
		return "Empty"
	case unicode.IsDigit(rune(name[0])):
		return "N" + name
	default:
		return name
	}
}
