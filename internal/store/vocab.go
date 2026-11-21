package store

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/chka/os-brain/internal/pyfmt"
)

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
	if Contains(chain, "global") {
		return chain
	}
	return append(chain, "global")
}

// Every memory type belongs to a tier, and the tier decides two things about it: how much
// weight it carries in ranking (TierWeight) and whether it fades with age (TierHalfLifeDays).
//
//	A  load-bearing facts: weight 1.30, never decays. It ages only by being superseded.
//	   `incident` is here rather than in D because what happened to a system does not stop
//	   being true: an outage or a breach is worth most to whoever asks about it months later.
//	B  durable rulings and limits: weight 1.20, never decays.
//	C  soft social knowledge: weight 1.00, a 60-day half-life.
//	D  ephemeral "right now" state: weight 0.70, a 4-day half-life.
//	N  reference documents and anything untyped: plain relevance, no weight and no decay.
var (
	TierA    = Set("infra", "access", "gotcha", "incident")
	TierB    = Set("decision", "constraint", "commitment")
	TierC    = Set("preference", "relationship")
	TierD    = Set("state")
	DocTypes = Set("knowledge", "brand", "sop", "process", "standard", "policy", "report", "note")
)

// GuardrailTypes are always injected into the brief, pinned or not, so a hard rule can
// never lose its slot to something more topical.
var GuardrailTypes = []string{"policy", "constraint"}

func Set(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, i := range items {
		m[i] = true
	}
	return m
}

// MemTypes are the memory types proper: the ones that sit in a tier.
func MemTypes() map[string]bool {
	m := map[string]bool{}
	for _, s := range []map[string]bool{TierA, TierB, TierC, TierD} {
		for k := range s {
			m[k] = true
		}
	}
	return m
}

// AllTypes is every type a memory may carry: the tiered ones plus the document types.
func AllTypes() map[string]bool {
	m := MemTypes()
	for k := range DocTypes {
		m[k] = true
	}
	return m
}

// UnknownType is the refusal for a memory type that does not exist. A task is the one a
// caller reaches for most, because `os-brain task` exists and --type looks like the way in;
// tasks are their own table with a schedule and a state, so the refusal says where they go.
func UnknownType(t string) string {
	msg := fmt.Sprintf("unknown type %s. valid: %s", pyfmt.Repr(t), JoinSorted(AllTypes()))
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "task", "todo", "reminder":
		msg += "\n  a task is not a memory: os-brain task add \"<title>\" [--body ...] " +
			"[--when \"tomorrow 9am\"] [--owner user|agent]"
	}
	return msg
}

// TierOf names the tier of a memory type; "N" for documents and anything unknown.
func TierOf(memType string) string {
	switch {
	case TierA[memType]:
		return "A"
	case TierB[memType]:
		return "B"
	case TierC[memType]:
		return "C"
	case TierD[memType]:
		return "D"
	}
	return "N"
}

// Confidence is how solid the claim is. It folds into the score so a casual remark cannot
// outrank a measured fact by being newer. `confirmed` must be earned with a citation; see
// EarnedConfidence.
var Confidence = map[string]float64{"confirmed": 1.15, "reported": 1.00, "assumed": 0.80}

const DefaultConfidence = "reported"

// high/medium/low is the other common spelling of the same idea. Accept it rather than
// silently storing a value nothing understands.
var confidenceAliases = map[string]string{"high": "confirmed", "medium": "reported", "low": "assumed"}

// NormConfidence maps any spelling onto a known confidence, falling back to the default.
func NormConfidence(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if a, ok := confidenceAliases[v]; ok {
		v = a
	}
	if _, ok := Confidence[v]; ok {
		return v
	}
	return DefaultConfidence
}

// KnownConfidence reports whether v is a confidence or one of its aliases, so a caller can
// refuse a typo instead of letting NormConfidence quietly turn it into the default.
func KnownConfidence(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	if _, ok := Confidence[v]; ok {
		return true
	}
	_, ok := confidenceAliases[v]
	return ok
}

// ClampImportance keeps importance on its 1..5 scale; 3 is neutral in the score.
func ClampImportance(v int) int {
	if v < 1 {
		return 1
	}
	if v > 5 {
		return 5
	}
	return v
}

// BindingTypes carry a rule into every brief whether or not it is pinned. A rule stored as
// anything else is a rule that stops being read.
var BindingTypes = map[string]bool{
	"constraint": true, "policy": true, "preference": true, "relationship": true,
}

var ruleWords = regexp.MustCompile(`(?i)\b(never|always|must not|must never|do not|don't|` +
	`under no circumstances|no session may|is forbidden|is not allowed)\b`)

// RuleShaped reports whether a title reads as a standing rule. It reads the TITLE only:
// a body mentioning "never" in passing is ordinary prose, and a heuristic that fires on
// prose is a heuristic people learn to ignore.
func RuleShaped(title string) bool { return ruleWords.MatchString(title) }

// TierTypes is the set of memory types in a tier; nil for tier N, which is defined by
// what it is not.
func TierTypes(tier string) map[string]bool {
	switch tier {
	case "A":
		return TierA
	case "B":
		return TierB
	case "C":
		return TierC
	case "D":
		return TierD
	}
	return nil
}
