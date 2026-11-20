package brain

import "testing"

// 'confirmed' outranks 'reported', so it has to be earned by a citation a stranger could
// check: the user's own word, a file and line, a commit, a URL, a command that was run.
// "user" anywhere in a source, any --ref at all and any x.md in a body each used to earn it.
func TestConfirmedTakesACitationAStrangerCouldCheck(t *testing.T) {
	for _, c := range []struct {
		source string
		refs   []string
		body   string
	}{
		{"anonymous forum user, unverified", nil, ""},
		{"session", []string{"x"}, ""},
		{"session", nil, "see a.md"},
		{"session", nil, "it works on TCP/IP and/or UDP"},
	} {
		if HasCitation(c.source, c.refs, c.body) {
			t.Errorf("earned by nothing checkable: %+v", c)
		}
	}
	for _, c := range []struct {
		source string
		refs   []string
		body   string
	}{
		{"stated by the user", nil, ""},
		{"the user, in chat 2026-09-01", nil, ""},
		{"session", []string{"scripts/deploy.sh:41"}, ""},
		{"session", []string{"0eb27d2"}, ""},
		{"session", []string{"#1234"}, ""},
		{"README.md", nil, ""},
		{"session", nil, "per internal/brain/store.go"},
		{"session", nil, "cmd_write.go:412 does it"},
		{"session", nil, "see https://example.com/doc"},
		{"session", nil, "ran `go test ./...`"},
	} {
		if !HasCitation(c.source, c.refs, c.body) {
			t.Errorf("a real citation was refused: %+v", c)
		}
	}
}
