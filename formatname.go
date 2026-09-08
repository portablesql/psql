package psql

import (
	"strings"
	"unicode"
)

// FormatTableName is a variable that holds the table name formatter used by
// [LegacyNamer]. It defaults to Camel_Snake_Case ("UserProfile" → "User_Profile")
// but can be overridden. This is kept for backwards compatibility - new code
// should configure a [Namer] on the Backend instead.
var FormatTableName = formatCamelSnakeCase

// format to Camel_Snake_Case
func formatCamelSnakeCase(name string) string {
	b := &strings.Builder{}

	for n, c := range name {
		if n == 0 {
			b.WriteRune(unicode.ToUpper(c))
			continue
		}
		if !unicode.IsLetter(c) {
			if unicode.IsNumber(c) {
				b.WriteRune(c)
			}
			continue
		}
		if unicode.IsUpper(c) {
			b.WriteByte('_')
		}
		b.WriteRune(c)
	}

	return b.String()
}
