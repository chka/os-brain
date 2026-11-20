package brain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// --- identity --------------------------------------------------------------------

// StripAccents folds diacritics the way the FTS index and title identity both need.
// unicode61 does not fold Greek tonos at any remove_diacritics setting, which is why
// the index is written in code instead of by a trigger (see ftsDDL).
func StripAccents(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// NormTitle is memory identity: case, punctuation, spacing and diacritics all ignored,
// so "φώσφορος" and "φωσφορος" are the same fact.
func NormTitle(title string) string {
	lowered := StripAccents(strings.ToLower(title))
	var b strings.Builder
	for _, r := range lowered {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// ContentHash keys identity on project + scope + normalised title. The project has to be
// in the key: two projects may hold the same title for different facts, and in one store
// that would otherwise collide.
func ContentHash(project, scope, title string) string {
	sum := sha256.Sum256([]byte(project + "\x00" + scope + "\x00" + NormTitle(title)))
	return hex.EncodeToString(sum[:])
}

// --- query terms -----------------------------------------------------------------

// Stopwords carry no topic and cannot be filtered by frequency. Document frequency
// inverts on a small corpus: in 28 memories of terse technical prose "what", "this" and
// "does" are rarer than "project", so the topical term was dropped for being popular.
// Class has to do this job, not rarity. Greek is here because the brain is used in it.
var stopwords = wordSet(`
a about all also am an and any are as at be been being both but by can could did do
does doing done each for from further had has have having he her here hers him his how
i if in into is it its just me more most my no nor not now of off on once only or other
our ours out over own same she should so some such than that the their theirs them then
there these they this those through to too under until up us very was we were what when
where which while who whom why will with would you your yours
αλλα αν απο αυτα αυτο αυτος για δεν ειναι ενα ενας εχει η ηταν θα και κατα με μια μου να
οι ολα οπως οσο οταν οτι ποιο ποια ποιος που πως σας σε στη στην στο στον στους τα την
της το τον του των τι τις ως`)

func wordSet(words string) map[string]bool {
	set := map[string]bool{}
	for _, w := range strings.Fields(words) {
		set[w] = true
	}
	return set
}

// IsStopword accepts the trailing '*' FTSQuery adds for prefix matching.
func IsStopword(term string) bool {
	return stopwords[strings.ToLower(strings.TrimRight(term, "*"))]
}

// CoverageFloor is how many of a query's n terms a row must hold to be a candidate at all.
// Project recall and the spine search both call it, because "which rows may answer" is
// meaning and not mechanism.
//
// One or two terms need all of them. Beyond that, three in five rounded up: 2 of 3, 3 of 4,
// 3 of 5, 6 of 10. Requiring every term made a query WORSE with each word added to it, and
// requiring any one makes a sentence match the whole brain. When no row reaches the floor
// the caller falls back to any-term, which is what a natural-language prompt always got.
func CoverageFloor(n int) int {
	if n <= 2 {
		return n
	}
	return (n*3 + 4) / 5
}

// CoverageWeight scales relevance by the share of the query a row answers, so a row holding
// every term outranks one holding most of them when the engine's own ranks are close. And
// they are close: relevance saturates on an x/(1+x) curve (see RelevanceFromBM25).
func CoverageWeight(matched, n int) float64 {
	if n <= 0 || matched >= n {
		return 1.0
	}
	if matched <= 0 {
		return 1.0 / float64(n)
	}
	return float64(matched) / float64(n)
}

// FTSQuery turns free text into a lenient FTS5 query, tokenised the way the index is.
//
// Punctuation SPLITS a word, it does not vanish from inside it. Stripping it instead
// turned "brain.md" into "brainmd*" and matched nothing, while the index holds "brain"
// and "md" as separate tokens.
func FTSQuery(text, op string) string {
	return strings.Join(FTSTerms(text), " "+op+" ")
}

// FTSTerms is FTSQuery's terms one by one, for the callers that ask the index about each.
// A one-character token is a real query in some domains (K, P, Ca, Hb in a clinical brain),
// so it is matched exactly rather than as a prefix over the whole alphabet.
func FTSTerms(text string) []string {
	var terms []string
	for _, t := range QueryTerms(text) {
		terms = append(terms, FTSTerm(t, len([]rune(t)) > 1))
	}
	return terms
}

// FTSTerm quotes a term so FTS5 reads it as a word. Bare, an uppercase AND, OR, NOT or
// NEAR in somebody's sentence is an operator: "NOT widgets" was a syntax error, and the
// error was swallowed as a miss, so any prompt with a capitalised OR recalled nothing.
// QueryTerms never yields a double quote, so there is nothing to escape.
func FTSTerm(t string, prefix bool) string {
	if prefix {
		return `"` + t + `"*`
	}
	return `"` + t + `"`
}

// MaxQueryTerms bounds what one query costs. Coverage asks the index once per term, and the
// prompt hook runs a recall on every message: a pasted log of a thousand words was a
// thousand index scans per prompt.
const MaxQueryTerms = 32

// QueryTerms splits a user's sentence into the terms a search engine should look for. It
// is the one tokeniser: every query syntax (FTS5 here, and the one ns-brain's team server
// builds) is made from these terms, because two tokenisers drift, and when they did, short
// tokens such as `db`, `s3` and `ci` silently found nothing on one side.
func QueryTerms(text string) []string {
	var terms []string
	for _, word := range strings.Fields(strings.ReplaceAll(StripAccents(text), `"`, " ")) {
		terms = append(terms, alphanumericRuns(word)...)
	}
	return firstDistinctTerms(terms, MaxQueryTerms)
}

// alphanumericRuns splits a word on everything that is not a letter or a digit,
// Unicode-aware: an [A-Za-z] class would shred Greek into nothing, which is how an early
// version silently disabled search for a project that works in another alphabet.
func alphanumericRuns(word string) []string {
	var runs []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			runs = append(runs, cur.String())
			cur.Reset()
		}
	}
	for _, r := range word {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return runs
}

// firstDistinctTerms drops repeats, case-insensitively, and keeps at most limit terms. The
// index is case-insensitive, so "Deploy deploy" is one term, and counting it twice raised
// the coverage floor for a word said twice.
func firstDistinctTerms(terms []string, limit int) []string {
	seen := map[string]bool{}
	out := terms[:0]
	for _, t := range terms {
		k := strings.ToLower(t)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, t)
		if len(out) == limit {
			break
		}
	}
	return out
}

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
			"40 characters max", what, Repr(value))
	}
	return nil
}

// ScopeAncestors: scopes nest on ':' and information flows inward only. A recall in
// channel:deploy:prod also sees channel:deploy, channel and global. The reverse never
// holds. Nearest first, so index 0 is the scope that was asked for.
func ScopeAncestors(scope string) []string {
	if scope == "" || scope == "global" {
		return []string{"global"}
	}
	parts := strings.Split(scope, ":")
	chain := make([]string, len(parts))
	for i := range parts {
		chain[len(parts)-1-i] = strings.Join(parts[:i+1], ":")
	}
	if contains(chain, "global") {
		return chain
	}
	return append(chain, "global")
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
