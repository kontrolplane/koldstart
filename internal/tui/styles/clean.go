package styles

import (
	"fmt"
	"strings"
	"unicode"
)

// Clean makes text from aws, the state or an error safe to draw: control characters (C0, DEL,
// C1) are shown escaped, so an escape sequence in a name cannot reach the terminal.
func Clean(s string) string { return clean(s, false) }

// CleanBlock is Clean for multi-line text, keeping newlines; tabs become 4 spaces, as lipgloss
// expands tabs differently from how it measures them.
func CleanBlock(s string) string { return clean(s, true) }

func clean(s string, block bool) string {
	if strings.IndexFunc(s, unicode.IsControl) < 0 {
		return s
	}
	if block {
		s = strings.ReplaceAll(s, "\r\n", "\n")
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' && block:
			b.WriteRune(r)
		case r == '\t' && block:
			b.WriteString("    ")
		case r == '\t':
			b.WriteByte(' ')
		case unicode.IsControl(r):
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
