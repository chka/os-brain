package brain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// How text becomes a search: accent folding, title normalisation and the FTS query built
// from what the user typed.

// SQLite's unicode61 tokenizer does not fold Greek tonos at any remove_diacritics setting,
// which is the whole reason the search index is written in Go rather than by a trigger.
func TestStripAccentsFoldsGreekTonos(t *testing.T) {
	if got := StripAccents("φώσφορος"); got != "φωσφορος" {
		t.Errorf("greek: got %q", got)
	}
	if got := StripAccents("Ωραία Ελλάδα"); got != "Ωραια Ελλαδα" {
		t.Errorf("greek caps: got %q", got)
	}
	if got := StripAccents("café naïve"); got != "cafe naive" {
		t.Errorf("latin: got %q", got)
	}
}

// Folding has to work in both directions through the real index: a memory written with
// accents is found by a query without them, in any case, and the reverse.
func TestGreekFoldsBothWays(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "φώσφορος στόχος", Body: "από τα λαμπερά",
		Type: "knowledge", Scope: "global"})
	for _, q := range []string{"φώσφορος", "φωσφορος", "ΦΩΣΦΟΡΟΣ", "Φωσφόρος"} {
		got := titles(recall(t, s, RecallArgs{Query: q, N: 5, Strict: true, NoTouch: true}))
		if len(got) != 1 {
			t.Errorf("%q found %d, want 1: folding is not symmetric", q, len(got))
		}
	}
}

// Two titles that differ only in case, punctuation or accents name the same fact, so they
// must normalise to the same key.
func TestNormTitleIgnoresCasePunctuationAndAccents(t *testing.T) {
	a := NormTitle("Ο Φώσφορος, ανεβαίνει!")
	b := NormTitle("ο φωσφορος ανεβαινει")
	if a != b {
		t.Errorf("%q != %q", a, b)
	}
}

func TestFTSQueryKeepsPunctuationAsASplit(t *testing.T) {
	// Stripping punctuation instead turned "brain.md" into "brainmd*", which matched
	// nothing while the index held "brain" and "md" as separate tokens.
	if got := FTSQuery("brain.md", "OR"); got != `"brain"* OR "md"*` {
		t.Errorf("got %q", got)
	}
	// A one-character token is a real query (units, element symbols, single-letter names),
	// so it is matched exactly rather than as a prefix over the whole alphabet.
	if got := FTSQuery("Req/S", "AND"); got != `"Req"* AND "S"` {
		t.Errorf("got %q", got)
	}
	// An [A-Za-z] character class would shred Greek into nothing.
	if got := FTSQuery("φώσφορος", "OR"); got != `"φωσφορος"*` {
		t.Errorf("greek: got %q", got)
	}
}

// An uppercase AND, OR, NOT or NEAR used to reach FTS5 as an operator, and the resulting
// syntax error came back as a silent miss. They are ordinary words in a query.
func TestOperatorWordsInAQueryAreWords(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "widgets are assembled in the north building"})
	for _, q := range []string{"NOT widgets", "widgets AND", "widgets OR", "NEAR widgets"} {
		var out bytes.Buffer
		if err := s.Recall(&out, RecallArgs{Query: q, JSON: true}); err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		var res struct{ Matched bool }
		json.Unmarshal(out.Bytes(), &res)
		if !res.Matched {
			t.Errorf("%q missed a memory that holds the word", q)
		}
	}
}

// A pasted log used to cost one index scan per word on every prompt. The number of
// distinct terms is capped, and repeats in any case count once.
func TestAQueryHasABoundedNumberOfDistinctTerms(t *testing.T) {
	var words []string
	for i := 0; i < 500; i++ {
		words = append(words, fmt.Sprintf("w%d", i))
	}
	if n := len(QueryTerms(strings.Join(words, " "))); n != MaxQueryTerms {
		t.Errorf("%d terms", n)
	}
	if got := QueryTerms("Deploy deploy DEPLOY now"); len(got) != 2 {
		t.Errorf("repeats counted: %v", got)
	}
}
