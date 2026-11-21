package store

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/chka/os-brain/internal/textguard"
)

// Confidence is self-declared, and that is a hole nothing else in the brain covers. verify
// and review --stale-days handle a fact going stale; nothing handled a fact being WRONG at
// the moment it was written. An agent that misreports what it did (and they do, routinely,
// including inventing conversations that never happened) writes that report into the store
// at `confirmed`, dated, and it then outranks hearsay forever by 1.15x.
//
// The answer is not a better self-assessment. It is to stop taking the top tier on trust:
// `confirmed` requires a citation somebody else could check. What CANNOT earn it is exactly
// the class that goes wrong: "I ran the tests", "we agreed X", the default source of
// "session". Those still store, at `reported`, which is what they always were.
//
// This generalises what source_commit already does: record something a later session can
// re-derive, rather than something only the writer can vouch for.
var (
	// a path someone can open: rooted (/etc/nginx/nginx.conf, ./x, ~/.ssh/config), two
	// directories deep (internal/store/store.go), or a directory and a file with an
	// extension (docs/mcp.md). One slash alone is "and/or" and "TCP/IP", which cite nothing.
	citePathRE = regexp.MustCompile(`(?:^|[\s(\[])(?:[~.]?/[\w.-]+(?:/[\w.-]+)*|[\w.-]+/[\w.-]+/[\w./-]+|[\w.-]+/[\w-]+\.[A-Za-z]\w*)`)
	// a file named by extension alone: store.go, mcp.md
	citeFileRE = regexp.MustCompile(`[\w-]+\.(?:go|py|js|ts|md|json|ya?ml|sql|sh|toml|conf|txt|rs|java|c|h)\b`)
	// an issue or pull request: #25903, owner/repo#12
	citeIssueRE = regexp.MustCompile(`^[\w./-]*#\d+$`)
	// the same with the line it points at: cmd_write.go:412
	citeFileLineRE = regexp.MustCompile(`[\w-]+\.(?:go|py|js|ts|md|json|ya?ml|sql|sh|toml|conf|txt|rs|java|c|h):\d+`)
	// a git object: seven or more hex characters, not a decimal number
	citeSHARE = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
	citeURLRE = regexp.MustCompile(`\b(?:https?|ssh|git)://\S+`)
	// a command someone can re-run, either backticked or named with its output
	citeCmdRE = regexp.MustCompile("`[^`]+`|\\b(?:git|go|npm|psql|sqlite3|curl|ssh|systemctl|docker|make|pytest|grep)\\s+\\S+")
)

// citeExempt are sources whose authority is a person rather than an artifact. A standing
// preference IS the user saying so: there is nothing to cite but them, and demanding a file
// path for "he wants short commit messages" would make the one command designed to be
// frictionless the one that argues back. These are roles, never a person's name: a name
// here would be a magic word in a binary shipped to everybody else.
var citeExempt = []string{"stated by the user", "the user", "user", "the owner", "the developer"}

// personSource is a source that IS one of those roles: the phrase alone, or the phrase
// leading ("stated by the user, 2026-09-01"). Not a substring match, which let "anonymous
// forum user, unverified" earn the top tier by containing the word.
func personSource(src string) bool {
	low := strings.ToLower(strings.TrimSpace(src))
	for _, e := range citeExempt {
		if low == e {
			return true
		}
		if strings.HasPrefix(low, e) {
			next := []rune(low[len(e):])[0]
			if !unicode.IsLetter(next) && !unicode.IsDigit(next) {
				return true
			}
		}
	}
	return false
}

// HasCitation reports whether anything here could be checked by someone who was not present
// when it was written.
//
// A flag that DECLARES a citation (--source, --ref) earns it with anything a stranger could
// look up: a path, a file, a URL, a command, a commit. A body earns it only by quoting one
// specific enough to find: a path, a URL, a command, a commit, or a file AT a line. A bare
// filename in prose is a mention ("see a.md"), not a citation, and --ref must hold one of
// those things, not merely be present.
func HasCitation(source string, refs []string, body string) bool {
	src := strings.TrimSpace(source)
	if src != "" && src != "session" {
		if personSource(src) || looksCheckable(src) || citeFileRE.MatchString(src) {
			return true
		}
	}
	for _, r := range refs {
		r = strings.TrimSpace(r)
		if looksCheckable(r) || citeFileRE.MatchString(r) || citeIssueRE.MatchString(r) ||
			citeSHARE.FindString(r) == r && r != "" {
			return true
		}
	}
	return looksCheckable(body) || citeFileLineRE.MatchString(body)
}

func looksCheckable(text string) bool {
	if text == "" {
		return false
	}
	if citeURLRE.MatchString(text) || citePathRE.MatchString(text) || citeCmdRE.MatchString(text) {
		return true
	}
	// A bare sha only counts alongside a word suggesting it is one: "commit 0eb27d2".
	if citeSHARE.MatchString(text) {
		low := strings.ToLower(text)
		for _, w := range []string{"commit", "sha", "rev", "head", "tag", "release"} {
			if strings.Contains(low, w) {
				return true
			}
		}
	}
	return false
}

// AppendRefs is what --ref stores: one "ref: <x>" line per citation under the body.
func AppendRefs(body string, refs []string) string {
	var lines []string
	for _, r := range refs {
		if r != "" {
			lines = append(lines, "ref: "+r)
		}
	}
	if len(lines) == 0 {
		return body
	}
	if body != "" {
		body += "\n\n"
	}
	return body + strings.Join(lines, "\n")
}

// EarnedConfidence refuses an unearned `confirmed`. It used to store the memory as
// `reported` and warn, on the reasoning that refusing loses the text at the only moment the
// caller holds it. In practice the caller holds it for the rest of the turn, and the warning
// went to stderr, which an MCP tool result and a hook do not carry: a session's first writes
// all went in as `reported` while it believed them confirmed. A refusal names the flag, and
// the same command with it goes through.
func EarnedConfidence(confidence, source string, refs []string, body string) (string, error) {
	if NormConfidence(confidence) != "confirmed" {
		return NormConfidence(confidence), nil
	}
	if HasCitation(source, refs, body) {
		return "confirmed", nil
	}
	return "", textguard.Judged("refusing 'confirmed': nothing here can be checked by anyone who was " +
		"not in this session.\n  'confirmed' outranks everything else forever, so it takes " +
		"evidence, not a self-assessment.\n  Add --source \"<file, command or URL>\", " +
		"--ref \"<path:line>\", or --source \"stated by the user\" (the phrase itself, not a " +
		"source that contains the word); or store it as " +
		"--confidence reported.")
}
