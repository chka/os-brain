// Package termtext makes untrusted text safe to show. Stored memories, repository text and
// input from an MCP client reach terminals, OS notifications and an agent's context.
// Terminal escapes can repaint or hide what a person is asked to approve, and bidi overrides
// can make a string read differently from what it is, so every such string goes through
// one of these.
package termtext

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxLabel caps a label or person shown anywhere.
const MaxLabel = 80

// isBidiControl is a rune that reorders the text around it: ALM, LRM, RLM, the embeddings and
// overrides (U+202A-U+202E) and the isolates (U+2066-U+2069).
func isBidiControl(r rune) bool {
	switch {
	case r == 0x061C, r == 0x200E, r == 0x200F:
		return true
	case r >= 0x202A && r <= 0x202E:
		return true
	case r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// IsControl reports a rune no rendered text should carry raw: C0 and C1 controls, DEL, bidi
// controls, the Unicode line and paragraph separators and the byte-order mark.
func IsControl(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || isBidiControl(r) ||
		r == 0x2028 || r == 0x2029 || r == 0xFEFF
}

// skipEscape returns the length of the escape sequence starting at s[0]==ESC (or a C1 CSI/OSC
// introducer), so the whole sequence goes, not just the byte that starts it.
func skipEscape(s string) int {
	if len(s) < 2 {
		return len(s)
	}
	switch s[1] {
	case '[': // CSI: parameters and intermediates, then one final byte 0x40-0x7e
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
		return len(s)
	case ']', 'P', 'X', '^', '_': // OSC/DCS/SOS/PM/APC: up to BEL or ST
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
		return len(s)
	}
	return 2
}

// CleanText strips terminal escapes, C0/C1 controls and bidi overrides, keeping newlines and
// tabs only when keepNewlines is set, and caps the result at max runes (0: no cap).
func CleanText(s string, keepNewlines bool, max int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "?")
	}
	var b strings.Builder
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i += skipEscape(s[i:])
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if (r == '\n' || r == '\t') && keepNewlines {
			b.WriteRune(r)
			n++
		} else if IsControl(r) {
			if r == '\n' || r == '\t' || r == '\r' {
				b.WriteByte(' ')
				n++
			}
		} else {
			b.WriteRune(r)
			n++
		}
		if max > 0 && n >= max && i < len(s) {
			b.WriteString("...")
			break
		}
	}
	return b.String()
}

// Label is CleanText for a one-line string shown as a name: a label, a person, a brain name.
func Label(s string) string { return CleanText(s, false, MaxLabel) }

// VisibleText renders every control character, escape and bidi override as a visible
// escape (\x1b, \r, ‮) so a person approving the text sees exactly what it holds.
// Newlines stay newlines: they cannot hide anything once \r and the escapes are visible.
func VisibleText(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "?")
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			fmt.Fprintf(&b, `\x%02x`, r)
		case isBidiControl(r) || r == 0x2028 || r == 0x2029 || r == 0xFEFF:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
