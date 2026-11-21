package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/clitest"
)

// `remember "title" "body"` once joined both arguments into the title and stored an empty
// body, without a word, so the detail the agent meant to keep was lost. Words left unquoted
// still make one title, since that is how people type at a shell. Separately quoted
// arguments are refused and store nothing, as is a title given both as --title and as an
// argument. The refusal names the flag to use instead. `task add` had the same join.
func TestSeparatelyQuotedArgumentsAreNotJoinedIntoATitle(t *testing.T) {
	clitest.Brain(t, "e2e")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "e2e-session-1")
	do := func(argv ...string) (string, error) {
		var out bytes.Buffer
		err := Run(argv, &out)
		return out.String(), err
	}
	for _, argv := range [][]string{
		{"remember", "desktop POST fault resolved", "the CR2032 was flat at 1.2 V"},
		{"remember", "--title", "desktop POST fault resolved", "the CR2032 was flat"},
		{"task", "add", "move the root key", "it is on the desktop"},
	} {
		if out, err := do(argv...); err == nil {
			t.Errorf("%q was accepted:\n%s", argv, out)
		} else if !strings.Contains(err.Error(), "--") {
			t.Errorf("%q: the refusal does not name the flag to use: %v", argv, err)
		}
	}
	if out, _ := do("recall", "CR2032", "--json"); strings.Contains(out, "1.2 V") || strings.Contains(out, "desktop POST") {
		t.Fatalf("a refused remember stored something:\n%s", out)
	}
	if out, err := do("remember", "fix", "the", "nginx", "reload"); err != nil {
		t.Fatalf("unquoted words were refused: %v\n%s", err, out)
	}
	if out, err := do("remember", "nginx terminates TLS on 8443", "--body", "on the edge box"); err != nil {
		t.Fatalf("one quoted title with --body was refused: %v\n%s", err, out)
	}
	if out, err := do("task", "add", "rotate", "the", "release", "keys"); err != nil {
		t.Fatalf("task add with unquoted words was refused: %v\n%s", err, out)
	}
}
