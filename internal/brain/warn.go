package brain

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// warnTo is where Warn writes; tests set it to assert on the nudges, which are user-facing
// text with their own failure mode: a warning that fires on correct usage trains people to
// ignore the ones that matter. nil means "whatever os.Stderr is right now", because
// capturing it once at package init breaks every test that redirects os.Stderr afterwards.
var warnTo io.Writer

// Warn prints a one-line "[brain]" notice to stderr.
func Warn(format string, a ...interface{}) {
	w := warnTo
	if w == nil {
		w = os.Stderr
	}
	fmt.Fprintf(w, "[brain] "+format+"\n", a...)
}

func trim(s string) string  { return strings.TrimSpace(s) }
func lower(s string) string { return strings.ToLower(s) }

// splitOnSemicolon splits a DDL block into statements, stripping `--` line comments first.
// Splitting naively is wrong: a schema comment once contained a semicolon, which cut a
// CREATE TABLE in half. The shared schema then failed to build, the shared store would not
// open, and the brief silently fell back to the built-in axioms with no error anywhere,
// the class of failure this codebase treats as worse than a crash.
func splitOnSemicolon(s string) []string {
	var clean strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		clean.WriteString(line)
		clean.WriteString("\n")
	}
	return strings.Split(clean.String(), ";")
}
