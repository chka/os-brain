package brain

import (
	"math"
	"strings"
	"testing"
)

// The ranking rules in score.go. Where the documentation states a number, the tests assert
// the number rather than an ordering: a test that only checks "A came before B" still
// passes when the formula is wrong in a way that happens to keep the order, which is most
// ways it can be wrong.

// scoreOf returns the score recall gave the memory whose title contains title.
func scoreOf(t *testing.T, s *Store, query, title string) float64 {
	t.Helper()
	res := recall(t, s, RecallArgs{Query: query, N: 50, Browse: true, NoTouch: true})
	for _, r := range res["results"].([]interface{}) {
		m := r.(map[string]interface{})
		if strings.Contains(m["title"].(string), title) {
			return m["score"].(float64)
		}
	}
	t.Fatalf("%q not in the results for %q", title, query)
	return 0
}

// --- lexical relevance -------------------------------------------------------------

// bm25 is exactly 0.0 when a term appears in every row, and a matched row must never score
// as if it had not matched at all.
func TestRelevanceFloorKeepsAZeroIDFMatchAboveANonMatch(t *testing.T) {
	if RelevanceFromBM25(0.0) != MatchFloor {
		t.Errorf("zero bm25 should map to the floor, got %v", RelevanceFromBM25(0.0))
	}
	if RelevanceFromBM25(-5) <= RelevanceFromBM25(-1) {
		t.Error("a stronger match must score higher")
	}
	if RelevanceFromBM25(-1e9) > 1.0 {
		t.Error("relevance must stay bounded by 1")
	}
}

// The same floor, end to end through recall: three memories that all hold the term.
func TestATermInEveryMemoryStillScoresAsAMatch(t *testing.T) {
	s := newTestStore(t)
	for _, tl := range []string{"alpha ubiquitous", "bravo ubiquitous", "charlie ubiquitous"} {
		remember(t, s, RememberArgs{Title: tl, Body: "ubiquitous appears in all of them",
			Type: "note"})
	}
	res := recall(t, s, RecallArgs{Query: "ubiquitous", N: 10, NoTouch: true})
	rows := res["results"].([]interface{})
	if len(rows) != 3 {
		t.Fatalf("a zero-IDF term matched %d of 3 memories", len(rows))
	}
	for _, r := range rows {
		m := r.(map[string]interface{})
		if sc := m["score"].(float64); sc < MatchFloor {
			t.Errorf("%q scored %.3f, below the %.2f floor: a match scoring as a non-match",
				m["title"], sc, MatchFloor)
		}
	}
}

// --- tier weights and decay ----------------------------------------------------------

func TestTierDecayAndWeights(t *testing.T) {
	now := int64(1_700_000_000)
	// A and B never time-decay: load-bearing facts stay authoritative forever.
	if RecencyFactor("A", now-365*86400, now) != 1.0 {
		t.Error("tier A must not decay")
	}
	// D halves every 4 days, C every 60.
	if got := RecencyFactor("D", now-4*86400, now); math.Abs(got-0.5) > 1e-9 {
		t.Errorf("tier D half-life: got %v", got)
	}
	if got := RecencyFactor("C", now-60*86400, now); math.Abs(got-0.5) > 1e-9 {
		t.Errorf("tier C half-life: got %v", got)
	}
}

// Documented tier weights: A 1.30, B 1.20, neutral 1.00, D 0.70. Four memories that differ
// only by type, so the ratio of their scores is the ratio of their weights.
func TestTierWeightsAreTheDocumentedRatios(t *testing.T) {
	s := newTestStore(t)
	for _, c := range []struct{ typ, title string }{
		{"infra", "widget alpha"},    // A
		{"decision", "widget bravo"}, // B
		{"note", "widget charlie"},   // neutral
		{"state", "widget delta"},    // D
	} {
		remember(t, s, RememberArgs{Title: c.title, Body: "same body for all four",
			Type: c.typ, Scope: "global"})
	}
	a := scoreOf(t, s, "widget", "alpha")
	b := scoreOf(t, s, "widget", "bravo")
	n := scoreOf(t, s, "widget", "charlie")
	d := scoreOf(t, s, "widget", "delta")
	for _, c := range []struct {
		name      string
		got, want float64
	}{
		{"A/neutral", a / n, 1.30},
		{"B/neutral", b / n, 1.20},
		{"D/neutral", d / n, 0.70},
	} {
		if math.Abs(c.got-c.want) > 0.02 {
			t.Errorf("%s ratio is %.3f, documented as %.2f", c.name, c.got, c.want)
		}
	}
}

// Recency through recall: C halves every 60 days, A never decays. Checked at exactly one
// half-life rather than as "it went down".
func TestDecayHalvesAtTheDocumentedHalfLife(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "sprocket fresh preference", Body: "b", Type: "preference"})
	remember(t, s, RememberArgs{Title: "sprocket aged preference", Body: "b", Type: "preference"})
	remember(t, s, RememberArgs{Title: "sprocket aged infra", Body: "b", Type: "infra"})
	// 60 days back for the C-tier one, and the same for an A-tier one that must not move.
	sixtyDays := Now() - 60*86400
	for _, title := range []string{"sprocket aged preference", "sprocket aged infra"} {
		if _, err := s.DB.Exec("UPDATE memories SET updated_at=? WHERE title=?",
			sixtyDays, title); err != nil {
			t.Fatal(err)
		}
	}
	fresh := scoreOf(t, s, "sprocket", "fresh preference")
	aged := scoreOf(t, s, "sprocket", "aged preference")
	if got := aged / fresh; math.Abs(got-0.5) > 0.03 {
		t.Errorf("a C-tier memory at exactly one 60-day half-life scores %.3f of a fresh "+
			"one; the documented half-life says 0.5", got)
	}
	remember(t, s, RememberArgs{Title: "sprocket fresh infra", Body: "b", Type: "infra"})
	if got := scoreOf(t, s, "sprocket", "aged infra") / scoreOf(t, s, "sprocket", "fresh infra"); got < 0.98 {
		t.Errorf("a tier A memory lost %.1f%% over 60 days; tier A never decays",
			(1-got)*100)
	}
}

// An incident does not decay. When `incident` sat in tier D, a pinned, importance-5
// security incident stored 18 days earlier could not be recalled by a query naming its own
// subject: 4.5 half-lives is a 23x penalty, and the dullest tier-A fact about the same host
// outranked it. An incident is valuable precisely because whoever asks months later can
// still find it.
func TestAnIncidentDoesNotDecay(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "cogwheel fresh incident", Body: "b", Type: "incident"})
	remember(t, s, RememberArgs{Title: "cogwheel aged incident", Body: "b", Type: "incident"})
	remember(t, s, RememberArgs{Title: "cogwheel aged state", Body: "b", Type: "state"})
	remember(t, s, RememberArgs{Title: "cogwheel fresh state", Body: "b", Type: "state"})
	eighteenDays := Now() - 18*86400
	for _, title := range []string{"cogwheel aged incident", "cogwheel aged state"} {
		if _, err := s.DB.Exec("UPDATE memories SET updated_at=? WHERE title=?",
			eighteenDays, title); err != nil {
			t.Fatal(err)
		}
	}
	if got := scoreOf(t, s, "cogwheel", "aged incident") /
		scoreOf(t, s, "cogwheel", "fresh incident"); got < 0.98 {
		t.Errorf("an incident lost %.1f%% over 18 days; an incident never decays", (1-got)*100)
	}
	// `state` keeps the 4-day half-life, so the two are not merely the same tier by accident.
	if got := scoreOf(t, s, "cogwheel", "aged state") /
		scoreOf(t, s, "cogwheel", "fresh state"); got > 0.1 {
		t.Errorf("a 18-day-old state memory scores %.3f of a fresh one; tier D halves "+
			"every 4 days, so it should be far below that", got)
	}
}

// --- usage and confidence ------------------------------------------------------------

func TestUsageFactorIsCapped(t *testing.T) {
	if UsageFactor(0) != 1.0 {
		t.Error("unused memories get no lift")
	}
	if UsageFactor(1_000_000) > 1.5 {
		t.Error("usage lift must cap at +50%")
	}
}

// A casual remark must not outrank a measured fact by being newer.
func TestConfidenceDiscountsHearsay(t *testing.T) {
	now := Now()
	confirmed := ScoreMem(1, "decision", now, now, 0, 3, "confirmed")
	assumed := ScoreMem(1, "decision", now, now, 0, 3, "assumed")
	if !(confirmed > assumed) {
		t.Error("confirmed must outrank assumed")
	}
	if NormConfidence("high") != "confirmed" || NormConfidence("nonsense") != DefaultConfidence {
		t.Error("confidence aliases")
	}
}

// Documented confidence multipliers: confirmed 1.15, reported 1.00, assumed 0.80.
func TestConfidenceMultipliersAreTheDocumentedRatios(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "gadget confirmed one", Body: "b", Type: "note",
		Confidence: "confirmed", Source: "internal/brain/score.go"})
	remember(t, s, RememberArgs{Title: "gadget reported two", Body: "b", Type: "note"})
	remember(t, s, RememberArgs{Title: "gadget assumed three", Body: "b", Type: "note",
		Confidence: "assumed"})
	rep := scoreOf(t, s, "gadget", "reported")
	if got := scoreOf(t, s, "gadget", "confirmed") / rep; math.Abs(got-1.15) > 0.02 {
		t.Errorf("confirmed/reported is %.3f, documented as 1.15", got)
	}
	if got := scoreOf(t, s, "gadget", "assumed") / rep; math.Abs(got-0.80) > 0.02 {
		t.Errorf("assumed/reported is %.3f, documented as 0.80", got)
	}
}

// --- the stakes band -------------------------------------------------------------------
//
// Tier, importance, usage and confidence together are the "stakes" of a memory. Unbounded,
// they once spanned 43.75x on a real store against a lexical relevance spread of 1.12x, so
// ranking was in practice the multipliers alone, and the demoted memory was often the one
// that answered the question. Stakes now break ties within a bounded band.

// What is asserted is the bound itself: with the SAME stakes on both sides, the better
// match must win. What is deliberately NOT asserted is that a better match always wins
// regardless of stakes: it does not while RelevanceFromBM25 saturates (s/(1+s) turns a 3.7x
// difference in bm25 into 1.21x of relevance), and no bounded tie-break can be made
// subordinate to that without becoming no tie-break at all.
func TestABetterMatchWinsWhenTheStakesAreEqual(t *testing.T) {
	s := newTestStore(t)
	// Filler first. With a two-document corpus bm25 has no IDF to work with, both results
	// land on MatchFloor and stakes decide a genuine tie, which would make this test pass
	// for the wrong reason.
	for _, ttl := range seedTitles[:10] {
		remember(t, s, RememberArgs{Title: ttl, Body: "unrelated body about other things",
			Type: "note", Scope: "global"})
	}
	remember(t, s, RememberArgs{
		Title: "the metrics server runs as a subcommand of one binary",
		Body:  "metrics server. metrics server. metrics server.",
		Type:  "note", Scope: "global", Importance: 3, Confidence: "reported"})
	remember(t, s, RememberArgs{
		Title: "security audit of the tenant boundary across the estate",
		Body: "a very long body about auditing tenants and organisations and roles and " +
			"grants and boundaries and reviews and findings, mentioning the metrics server " +
			"exactly once, and otherwise concerned with entirely different matters",
		Type: "note", Scope: "global", Importance: 3, Confidence: "reported"})

	res := recall(t, s, RecallArgs{Query: "metrics server", N: 10, NoTouch: true})
	rows, _ := res["results"].([]interface{})
	if len(rows) < 2 {
		t.Fatalf("expected both memories back, got %d", len(rows))
	}
	top := rows[0].(map[string]interface{})
	if !strings.Contains(top["title"].(string), "subcommand of one binary") {
		t.Errorf("with equal stakes the weaker match still won: %q", top["title"])
	}
}

// Every signal at its maximum against every signal at its minimum can now differ by at most
// StakesBand squared, which is 2.04x.
func TestStakesCannotCompoundBeyondTheBand(t *testing.T) {
	best := StakesFactor("infra", 1000, 5, "confirmed")
	worst := StakesFactor("state", 0, 1, "assumed")
	if best > StakesBand+1e-9 || worst < 1.0/StakesBand-1e-9 {
		t.Errorf("stakes escaped the band: %.3f and %.3f outside [%.3f, %.3f]",
			best, worst, 1.0/StakesBand, StakesBand)
	}
	if ratio := best / worst; ratio > 2.05 {
		t.Errorf("the widest stakes gap between two results is %.2fx; bounded means "+
			"bounded, and 43.75x is what this exists to prevent", ratio)
	}
}

// The band must not swallow the tier weights themselves: they are the documented coarse
// ranking of types, and clamping tier D on its own would publish a ratio the code does not
// honour. That is why StakesBand is derived from 1/0.70 rather than picked, and this test
// keeps the derivation true.
func TestTheStakesBandAdmitsEveryTierWeightUnclamped(t *testing.T) {
	for _, tier := range []string{"A", "B", "C", "D", "N"} {
		w := TierWeight(tier)
		if w > StakesBand || w < 1.0/StakesBand {
			t.Errorf("tier %s weighs %.2f, outside the stakes band [%.3f, %.3f]: the "+
				"documented tier ratio cannot be honoured", tier, w, 1.0/StakesBand, StakesBand)
		}
	}
}

// Decay is deliberately NOT bounded. Staleness is a claim about whether something is still
// true, not about how much it matters, and a tier-D memory long past its four-day half-life
// must be able to sink out of sight rather than be held within a tie-break of a fresh one.
func TestDecayIsNotBoundedByTheStakesBand(t *testing.T) {
	now := Now()
	fresh := ScoreMem(1.0, "state", now, now, 0, 3, "reported")
	stale := ScoreMem(1.0, "state", now-40*86400, now, 0, 3, "reported")
	if stale >= fresh/10 {
		t.Errorf("a state memory 40 days past a 4-day half-life scored %.4f against a "+
			"fresh %.4f: decay has been flattened into the tie-break band", stale, fresh)
	}
}
