package textguard

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/chka/os-brain/internal/pyfmt"
)

// --- secrets ---------------------------------------------------------------------
//
// The brain refuses to distil a secret into plaintext SQLite. The store inherits the
// sensitivity of its source: if it came from a vault, keep the pointer, not the value.

// secretPatterns are the credential formats recognised by their shape alone.
var secretPatterns = []struct {
	re   *regexp.Regexp
	what string
}{
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`), "a private key"},
	{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), "an AWS access key id"},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`), "an API secret key"},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`), "a GitHub token"},
	{regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}`), "a Slack token"},
	{regexp.MustCompile(`(?i)\b(password|passwd|secret|api[_-]?key|token)\s*[:=]\s*\S{8,}`), "a credential"},
	// A JWT is three base64url segments and always starts with the encoded `{"` of its
	// header. Bearer tokens were going in untouched.
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), "a JWT"},
	// ns-brain's team server issues tokens with this prefix precisely so a scanner can
	// catch them, and someone who uses both editions may well paste one.
	{regexp.MustCompile(`\bhive_[A-Za-z0-9_-]{20,}`), "a hive token"},
	// `sk-` above needs the hyphen, so Stripe's underscore keys went straight in.
	{regexp.MustCompile(`\b[sr]k_(?:live|test)_[A-Za-z0-9]{16,}`), "a Stripe key"},
	// An LLM platform's API key, carrying a fixed prefix for the same reason.
	{regexp.MustCompile(`\bko_[A-Za-z0-9_-]{32,}`), "a platform API key"},
}

// urlCredential is a password in a URL's userinfo, postgres://admin:S3cret@db. A
// placeholder there ($PASS, <password>, ${DB_PASS}, ****) is a pointer, which is what a
// brain should hold, so it is not a hit.
var urlCredential = regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9+.-]*://[^/\s:@]+:([^/\s@]+)@`)

// bearerToken is the value after Bearer. "bearer tokens expire hourly" is prose, so the
// value has to look like one: long, and holding a digit or a symbol.
var bearerToken = regexp.MustCompile(`(?i)\bbearer\s+([A-Za-z0-9._~+/=-]{20,})`)

// placeholderSecret is a value that stands for a secret rather than being one: $VAR,
// <password>, %s, or nothing but asterisks, x's and dots.
func placeholderSecret(v string) bool {
	return strings.HasPrefix(v, "$") || strings.HasPrefix(v, "<") || strings.HasPrefix(v, "%") ||
		strings.Trim(v, "*xX.") == ""
}

// prosePassword catches "the password is hunter2longenough", which the `key: value` rule
// above never saw. Requiring only length would fire on ordinary sentences about
// credentials ("the token is refreshed hourly"), and a guard that trips on correct prose
// gets --allow-secret pasted in reflexively, which is worse than no guard. So the value
// has to look like one: no spaces, at least eight characters, and containing a digit or a
// symbol, which English words do not.
var prosePassword = regexp.MustCompile(
	`(?i)\b(?:password|passwd|passphrase|secret|api[_-]?key|token)\s+(?:is|was|=)\s+["'` + "`" + `]?([^\s"'` + "`" + `]{8,})`)

// looksLikeSecretValue is true for a value holding a digit or anything other than an ASCII
// letter.
func looksLikeSecretValue(v string) bool {
	digit, symbol := false, false
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z'):
			symbol = true
		}
	}
	return digit || symbol
}

// ScanSecrets names each kind of credential the text appears to hold, in a fixed order;
// nil when it holds none. GuardText refuses on the first.
func ScanSecrets(text string) []string {
	var hits []string
	for _, m := range prosePassword.FindAllStringSubmatch(text, -1) {
		if looksLikeSecretValue(m[1]) {
			hits = append(hits, "a credential")
			break
		}
	}
	for _, p := range secretPatterns {
		if p.re.MatchString(text) {
			hits = append(hits, p.what)
		}
	}
	for _, m := range urlCredential.FindAllStringSubmatch(text, -1) {
		if !placeholderSecret(m[1]) {
			hits = append(hits, "a credential in a URL")
			break
		}
	}
	for _, m := range bearerToken.FindAllStringSubmatch(text, -1) {
		if looksLikeSecretValue(m[1]) {
			hits = append(hits, "a bearer token")
			break
		}
	}
	return hits
}

// Judged formats a refusal: the guard looked at the caller's input and turned it down, so
// sending it again unchanged will not help. It is an ordinary error; the name marks each
// guard's refusal at its call site.
func Judged(format string, args ...interface{}) error {
	return fmt.Errorf(format, args...)
}

// GuardText is on every path that puts caller-supplied text into the DB; allow is the
// command's --allow-secret flag. The refusal is not only about the store being plaintext:
// a distilled memory is a denser target than the transcript it came from, and it is read
// into an agent's context by design.
func GuardText(allow bool, fields ...string) error {
	if allow {
		return nil
	}
	hits := ScanSecrets(strings.Join(fields, "\n"))
	if len(hits) == 0 {
		return nil
	}
	return Judged("refusing to store what looks like %s. A brain is distilled and gets "+
		"read into context: store a pointer (vault item, file path), not the value. "+
		"Override with --allow-secret only if this is a false positive", hits[0])
}

// GuardTextNoOverride is the same check for the commands that have no --allow-secret
// flag. It exists because the message is a promise: three commands once called GuardText
// without registering the flag it names, so a genuine false positive on any of them was
// permanently unstoreable - the error told you to pass a flag the command would then
// reject. Either the flag exists or the message does not mention it.
func GuardTextNoOverride(fields ...string) error {
	hits := ScanSecrets(strings.Join(fields, "\n"))
	if len(hits) == 0 {
		return nil
	}
	return fmt.Errorf("refusing to store what looks like %s. A brain is distilled and gets "+
		"read into context: store a pointer (a vault item, a file path, an env var name), "+
		"not the value. This command has no override", hits[0])
}

// --- names, scopes and sizes -----------------------------------------------------

var safeToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._:-]{0,39}$`)

// CheckToken refuses a name-like value (a type, a key, a label) that is not short plain
// text, so it can be printed, put in a path or matched without escaping.
func CheckToken(value, what string) error {
	if !safeToken.MatchString(value) {
		return Judged("invalid %s %s: letters, digits, space and . _ : - only, "+
			"40 characters max", what, pyfmt.Repr(value))
	}
	return nil
}

// Clip truncates a body for display: newlines survive (every caller re-indents them) and
// the tail says how much was cut and how to read the rest.
func Clip(s string, n int) string { return ClipWith(s, n, "os-brain get <id>") }

// ClipWith is Clip naming the command that reads the rest. A note clipped with Clip told
// the reader to run `get`, which looks up memories and answers "no memory #N".
func ClipWith(s string, n int, more string) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimRight(string(r[:n]), " \n\t") +
		fmt.Sprintf(" … (+%d chars, %s)", len(r)-n, more)
}

// A memory is one distilled fact. Nothing used to cap either field, so a 24 MB paste was
// stored in six seconds and every recall that matched it printed all of it, through the
// prompt hook into the agent's context.
const (
	MaxTitleRunes = 300
	MaxBodyBytes  = 64 << 10
)

// CheckMemorySize refuses a title or body over the caps above.
func CheckMemorySize(title, body string) error {
	if n := len([]rune(title)); n > MaxTitleRunes {
		return Judged("the title is %d characters; a title names one fact in at most %d. "+
			"Put the detail in the body", n, MaxTitleRunes)
	}
	if len(body) > MaxBodyBytes {
		return Judged("the body is %d bytes; a memory holds at most %d. A memory is one "+
			"distilled fact, not a paste: store the pointer (a file path, a URL) and the "+
			"conclusion", len(body), MaxBodyBytes)
	}
	return nil
}
