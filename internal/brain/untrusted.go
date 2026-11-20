package brain

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/chka/os-brain/internal/termtext"
)

// Stored text is read by two audiences that a raw byte can mislead: a terminal, which obeys
// escape sequences (OSC 52 writes the clipboard, OSC 8 hides a link), and an agent, which
// reads a line starting "## GUARDRAILS" as the brief's own section. Single-line fields are
// refused at the write path when they could do either, and rendered through OneLine
// anyway, because rows written before the rule (or imported from elsewhere) are still in
// stores. Bodies keep their newlines and are indented or fenced by whoever prints them.

// OneLine renders a stored single-line field with every control character, escape and bidi
// override visible, so it can neither start a line of its own nor hide part of itself.
func OneLine(s string) string {
	return strings.ReplaceAll(termtext.VisibleText(s), "\n", `\n`)
}

// BodyText renders a multi-line body: newlines stay, everything else that is not text is
// made visible. The caller still indents or fences each line.
func BodyText(s string) string { return termtext.VisibleText(s) }

// Fenced renders a body with prefix on every line. A line of the body can therefore never
// look like the text around the fence, whatever it contains: that is what makes the
// "information, not instruction" footer after it unforgeable.
func Fenced(prefix, body string) []string {
	lines := strings.Split(BodyText(strings.TrimRight(body, "\n")), "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(prefix+ln, " ")
	}
	return lines
}

// CheckLine refuses a single-line field holding a newline, a control character, a
// terminal escape or a bidi override. Every write path for a title, a summary, a label, a
// name or a description calls it.
func CheckLine(what, value string) error {
	for _, r := range value {
		if termtext.IsControl(r) {
			return Judged("%s must be one line of plain text and holds %s: a newline, a "+
				"control character, a terminal escape or a bidi override could forge a section "+
				"of a brief or drive the reader's terminal. Refused, nothing written", what,
				strings.Trim(OneLine(string(r)), " "))
		}
	}
	return nil
}

// CheckLines is CheckLine over (what, value) pairs.
func CheckLines(pairs ...string) error {
	for i := 0; i+1 < len(pairs); i += 2 {
		if err := CheckLine(pairs[i], pairs[i+1]); err != nil {
			return err
		}
	}
	return nil
}

// SafeOutput is every command's stdout and the process's stderr: whatever a renderer
// missed, no control character but newline and tab reaches a terminal or an agent raw.
// Each becomes \u00XX, which is still valid inside a JSON string, so --json output decodes
// to the same value.
func SafeOutput(w io.Writer) io.Writer {
	if _, ok := w.(*safeWriter); ok {
		return w
	}
	return &safeWriter{w: w}
}

// safeWriter holds back the bytes of a rune split across two writes (tail), so a
// multi-byte character is never mistaken for an invalid byte and escaped.
type safeWriter struct {
	w    io.Writer
	tail []byte
}

// Unwrap gives back the underlying writer, for a caller that must reach the real stream.
func (s *safeWriter) Unwrap() io.Writer { return s.w }

func (s *safeWriter) Write(p []byte) (int, error) {
	buf := append(append([]byte(nil), s.tail...), p...)
	cut := incompleteRuneStart(buf)
	s.tail = append(s.tail[:0], buf[cut:]...)
	if _, err := s.w.Write(escapeControls(buf[:cut])); err != nil {
		return 0, err
	}
	return len(p), nil
}

// incompleteRuneStart is where a rune that the buffer ends in the middle of begins, or
// len(buf) when the buffer ends on a whole rune.
func incompleteRuneStart(buf []byte) int {
	for i := len(buf) - 1; i >= 0 && i >= len(buf)-utf8.UTFMax; i-- {
		if utf8.RuneStart(buf[i]) {
			if !utf8.FullRune(buf[i:]) {
				return i
			}
			break
		}
	}
	return len(buf)
}

// escapeControls rewrites invalid bytes as \xXX and control characters other than newline
// and tab as \uXXXX. Clean text, the usual case, is returned as it came.
func escapeControls(text []byte) []byte {
	var b strings.Builder
	clean := true
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRune(text[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, text[i])
			clean = false
		case r != '\n' && r != '\t' && termtext.IsControl(r):
			fmt.Fprintf(&b, `\u%04x`, r)
			clean = false
		default:
			b.Write(text[i : i+size])
		}
		i += size
	}
	if clean {
		return text
	}
	return []byte(b.String())
}
