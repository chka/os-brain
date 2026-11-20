package brain

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/chka/os-brain/internal/termtext"
)

// The output format was set by the first, Python, implementation of this tool, and
// os-brain prints the same bytes. Go's defaults differ from Python's in three small ways
// that show up in almost every command; each was found by diffing the two over the same
// command sequence.

// Repr quotes a string the way Python's %r does: single quotes, switching to double only
// when the value itself contains a single quote. Go's %q always uses double quotes, so
// every "no such thing %q" message would differ.
//
// Control characters are escaped as Python escapes them, because a Repr'd value is usually
// the caller's own input echoed into an error, and that is a terminal escape's way in.
func Repr(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case termtext.IsControl(r) && r < 0x100:
			fmt.Fprintf(&b, `\x%02x`, r)
		case termtext.IsControl(r):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	esc := b.String()
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		return `"` + esc + `"`
	}
	return "'" + strings.ReplaceAll(esc, "'", `\'`) + "'"
}

// PyBool renders True/False, as Python does. Go's %v gives lowercase.
func PyBool(b bool) string {
	if b {
		return "True"
	}
	return "False"
}

// PyG formats a float the way Python's format spec 'g' does: six significant digits,
// trailing zeros stripped. Go's FormatFloat with precision -1 gives the shortest
// representation that round-trips, which turns 2.1-1.8 into 0.30000000000000004 in every
// measure delta.
func PyG(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', 6, 64)
}
