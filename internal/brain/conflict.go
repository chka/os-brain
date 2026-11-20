package brain

import (
	"fmt"
	"sort"
)

// Conflict is recall admitting it cannot choose. Two memories of the same type, in the
// same scope, scoring within a hair of each other, neither superseding the other: from
// the outside that is indistinguishable from two answers to one question, which is what
// two sessions reaching opposite conclusions actually produces.
//
// It deliberately does NOT claim to detect contradiction. Nothing lexical can: "Postgres
// for the job queue" and "keep SQLite everywhere, no server dependency" share almost no
// tokens, and a heuristic confident enough to call that a contradiction would be confident
// enough to call a hundred unrelated pairs one too. What it claims is narrower and true:
// these came out level, and if you were expecting one answer you have two.
type Conflict struct {
	With     []int64  `json:"with"`
	Note     string   `json:"note"`
	Sessions []string `json:"sessions,omitempty"`
}

// ConflictEpsilon is how close two scores must be to count as level. The scores are
// rounded to four places before this runs, so a value below that would never fire; 0.02 is
// about 1% of a typical mid-range score and was chosen to catch the exact-tie case
// (measured: two contradictory decisions both scored 0.483) plus genuine near-ties,
// without sweeping in every adjacent result.
const ConflictEpsilon = 0.02

// MarkConflicts annotates results that came out level with a peer. Same scope and same
// type is the whole filter: an infra fact and a decision that score alike are not
// competing answers, and neither are two facts about different parts of the system.
//
// Superseded pairs are exempt by construction - a retraction is the store having already
// chosen, which is the opposite of a tie.
func MarkConflicts(rows []*Memory) {
	for _, group := range liveRowsByScopeAndType(rows) {
		if len(group) < 2 {
			continue
		}
		for i := range group {
			markIfLevel(group, i)
		}
	}
}

func liveRowsByScopeAndType(rows []*Memory) map[string][]*Memory {
	byGroup := map[string][]*Memory{}
	for _, r := range rows {
		if r.SupersededBy != nil {
			continue
		}
		k := r.Scope + "\x00" + r.MemType
		byGroup[k] = append(byGroup[k], r)
	}
	return byGroup
}

// markIfLevel sets group[i].Conflict when any other row of its group scored within
// ConflictEpsilon of it, naming those rows and the sessions involved.
func markIfLevel(group []*Memory, i int) {
	a := group[i]
	var with []int64
	sessions := map[string]bool{ShortSession(a.Session): true}
	for j, b := range group {
		if i == j || abs(a.Score-b.Score) > ConflictEpsilon {
			continue
		}
		with = append(with, b.ID)
		sessions[ShortSession(b.Session)] = true
	}
	if len(with) == 0 {
		return
	}
	sort.Slice(with, func(x, y int) bool { return with[x] < with[y] })
	a.Conflict = &Conflict{With: with, Note: conflictNote(with, sessions)}
	for s := range sessions {
		a.Conflict.Sessions = append(a.Conflict.Sessions, s)
	}
	sort.Strings(a.Conflict.Sessions)
}

// conflictNote says which of the two situations this is, because they need different
// responses. Different sessions means two agents disagreed and nobody has adjudicated;
// one session means the same agent stored both, which is likelier to be two genuine facts
// than a contradiction, so the wording is softer.
func conflictNote(with []int64, sessions map[string]bool) string {
	multi := len(sessions) > 1
	ids := ""
	for i, id := range with {
		if i > 0 {
			ids += ", "
		}
		ids += fmt.Sprintf("#%d", id)
	}
	if multi {
		return fmt.Sprintf("ranks level with %s from a different session, and neither "+
			"supersedes the other - two answers to one question", ids)
	}
	return fmt.Sprintf("ranks level with %s in the same scope; if these are one question, "+
		"one of them is stale", ids)
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
