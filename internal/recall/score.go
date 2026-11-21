package recall

import (
	"math"

	"github.com/chka/os-brain/internal/store"
)

// TierWeight is the ranking weight of a tier: what a record IS. Load-bearing facts lean up,
// passing state leans down, everything else is neutral. It is one of the stakes signals, so
// StakesBand bounds how far it can move a result.
func TierWeight(tier string) float64 {
	switch tier {
	case "A":
		return 1.30
	case "B":
		return 1.20
	case "D":
		return 0.70
	}
	return 1.00
}

// TierHalfLifeDays is how fast a tier's memories fade; 0 means they never do. Only the soft
// tiers decay: a preference drifts over months, a "right now" is stale in days, and a fact
// or a ruling stays true until something supersedes it.
func TierHalfLifeDays(tier string) float64 {
	switch tier {
	case "C":
		return 60.0
	case "D":
		return 4.0
	}
	return 0.0
}

// RecencyFactor is the decay term: 1.0 for a tier that does not decay or a memory written
// now (or, by clock skew, in the future), halving once per half-life after that.
func RecencyFactor(tier string, ts, now int64) float64 {
	halfLife := TierHalfLifeDays(tier)
	if halfLife <= 0 {
		return 1.0
	}
	days := float64(now-ts) / 86400.0
	if days <= 0 {
		return 1.0
	}
	return math.Pow(0.5, days/halfLife)
}

// UsageFactor is the reinforcement term: a memory that real matches have returned before is
// a little more likely to be the answer again. Logarithmic so the first few uses count most,
// and capped at +50% so a popular memory cannot become unbeatable by popularity alone.
func UsageFactor(useCount int) float64 {
	if useCount <= 0 {
		return 1.0
	}
	return 1.0 + math.Min(0.15*math.Log1p(float64(useCount)), 0.5)
}

// MatchFloor is the least relevance a matched row can have. bm25 is exactly 0.0 when a term
// appears in every row (zero IDF), and a matched row must never score as if it had not
// matched at all.
const MatchFloor = 0.35

// BrowseRelevance is what a row is worth when nothing MATCHED and the caller asked to
// browse anyway. It sits above MatchFloor on purpose: a browse is not a weak match, it is a
// different question, so its rows are not ranked as if they had barely matched.
const BrowseRelevance = 0.5

// PinBoost is what a pin is worth on a browse. A pin says "read this by default", which is
// decisive when there is nothing to rank on and irrelevant when the user asked a specific
// question, so it multiplies the score only when nothing matched and breaks ties otherwise.
// A pinned memory loses nothing by that: it is in every brief unconditionally.
const PinBoost = 2.0

// ScopeWeight prioritises the scope that was asked for without hiding its ancestors.
// Scoping WIDENS the result set (a scope sees its ancestors and global), and with no
// counterweight the widening was all it did. chain is ScopeAncestors(asked), nearest first,
// so distance 0 is the requested scope itself: 1.6, then 0.25 less per step up, never below
// 0.8. A row from outside the chain is neutral.
func ScopeWeight(chain []string, scope string) float64 {
	for distance, c := range chain {
		if c == scope {
			return math.Max(0.8, 1.6-0.25*float64(distance))
		}
	}
	return 1.0
}

// RoundScore is applied before the sort, not after, so that two scores equal to four
// decimal places tie-break by pin, recency and id rather than by float noise, which would
// otherwise order near-ties differently from one platform to the next.
func RoundScore(v float64) float64 { return math.Round(v*10000) / 10000 }

// RelevanceFromBM25 maps FTS5 bm25() (negative; more negative is a better match) onto
// MatchFloor..1, monotonically increasing with match strength. s/(1+s) saturates, so strong
// matches bunch up near 1; see StakesBand for why that matters.
func RelevanceFromBM25(bm float64) float64 {
	strength := math.Max(0.0, -bm)
	return MatchFloor + (1.0-MatchFloor)*(strength/(1.0+strength))
}

// StakesBand bounds how far the stakes signals (tier, importance, usage, confidence) may
// move a result relative to what actually matched the query.
//
// Without a bound the multipliers decided the ranking. Measured over ten real queries on a
// brain of 571 memories: lexical relevance of the returned results spanned 1.12x (0.844 to
// 0.945), because RelevanceFromBM25 saturates, while the multipliers spanned 43.75x. So
// "relevance x ..." was in practice "the multipliers": 54% of result pairs came back out of
// lexical order, some promoted twelve places, and in every worst case the demoted memory was
// the one that literally answered the question, beaten by an unrelated memory that happened
// to be importance 5 and confirmed.
//
// That was never the intent. Confidence exists so hearsay cannot outrank a measured fact by
// being NEWER: a tie-break between comparable matches, not a licence to outrank a better
// match. The rule is: WHAT A RECORD IS BREAKS TIES; IT DOES NOT OVERRIDE WHAT MATCHED.
//
// The band is DERIVED, not picked: it is the widest of the tier weights, so the coarse
// ranking of types passes through untouched and only the compounding of several signals is
// bounded. TierWeight spans 0.70 (D) to 1.30 (A), and 1/0.70 is 1.43, so that is the band.
// Anything tighter would clamp tier D on its own and make the documented tier weights a lie.
const StakesBand = 1.0 / 0.70

// StakesFactor is the bounded tie-break: tier weight x importance/3 x usage x confidence,
// clamped to [1/StakesBand, StakesBand]. Importance is divided by 3 so the middle of the
// 1..5 scale is neutral.
//
// Decay is deliberately NOT in here. Staleness is a claim about whether something is still
// TRUE, not about how much it matters, and a tier-D memory thirty days past a four-day
// half-life should be able to sink out of sight rather than be held within 35% of a fresh
// one.
func StakesFactor(memType string, useCount, importance int, confidence string) float64 {
	f := TierWeight(store.TierOf(memType)) *
		(float64(store.ClampImportance(importance)) / 3.0) *
		UsageFactor(useCount) *
		store.Confidence[store.NormConfidence(confidence)]
	return math.Min(StakesBand, math.Max(1.0/StakesBand, f))
}

// ScoreMem is the composite retrieval score:
//
//	relevance x recencyFactor x stakes(tier, importance, usage, confidence)
//
// where the stakes term is bounded by StakesBand. Recency is unbounded because it is about
// truth over time; the rest is about stakes, and stakes break ties. Recall applies two more
// multipliers on top (PinBoost on a browse, ScopeWeight) and then RoundScore.
func ScoreMem(relevance float64, memType string, ts, now int64, useCount, importance int, confidence string) float64 {
	return relevance * RecencyFactor(store.TierOf(memType), ts, now) *
		StakesFactor(memType, useCount, importance, confidence)
}
