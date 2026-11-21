package diag

import (
	"fmt"
	"io"
	"os"
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

// SetOutput sends warnings to w until the returned function is called, so a test can
// assert on the nudges without redirecting the process's stderr.
func SetOutput(w io.Writer) (restore func()) {
	prev := warnTo
	warnTo = w
	return func() { warnTo = prev }
}
