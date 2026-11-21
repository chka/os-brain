package lexicon

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// --- query terms -----------------------------------------------------------------

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

// ContentWords is the set of normalised words of four letters or more.
func ContentWords(text string) map[string]bool {
	out := map[string]bool{}
	for _, t := range strings.Fields(NormTitle(text)) {
		if len([]rune(t)) >= 4 {
			out[t] = true
		}
	}
	return out
}

// Jaccard is the overlap of two word sets: shared words over all words, 0 when either is empty.
func Jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
