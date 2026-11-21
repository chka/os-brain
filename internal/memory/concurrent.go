package memory

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/chka/os-brain/internal/store"
)

// ConcurrentWindow is how recent (in seconds) another session's write has to be to count as
// "right now". Two sessions in one project, one storing "we will do A" and the other "B" less
// than two minutes later: the second must be told, and told to ask which stands. Later than
// that, or the same session, is an ordinary write and says nothing.
const ConcurrentWindow = 120

// Concurrent is the WHO and WHEN half: a different, known session, inside the window. An
// empty session on either side is "unknown", never "different": rows written before
// sessions were tracked must not alert everybody for ever.
func Concurrent(otherSession, mine string, otherAt, now int64) bool {
	if otherSession == "" || mine == "" || store.SameSession(otherSession, mine) {
		return false
	}
	age := now - otherAt
	return age >= 0 && age <= ConcurrentWindow
}

// decisionLike are the memory types that state a choice, so two of them in one scope inside
// the window are taken to be about the same thing.
var decisionLike = map[string]bool{"decision": true, "constraint": true, "policy": true,
	"preference": true}

// ConcurrentSubject is the WHAT half, and it is deliberately looser than the duplicate
// guard. "We go direct to Anthropic" and "OpenRouter stays the only route" share almost no
// words, so inside the window two decisions in one scope are the same subject by being
// decisions; anything else needs a quarter of its title in common, half the duplicate bar.
// It can afford to be loose because it only ever speaks inside two minutes, and because it
// alerts rather than refuses: nothing here can tell A from not-A, so it asks a person.
func ConcurrentSubject(myTitle, myType, otherTitle, otherType string) bool {
	if decisionLike[myType] && decisionLike[otherType] {
		return true
	}
	return TitleOverlap(myTitle, otherTitle) >= 0.25
}

// ConcurrentAlert is the one wording, for the refusal and the success alike.
func ConcurrentAlert(id int64, otherType, otherTitle string, ago int64) string {
	return fmt.Sprintf("ALERT: another session wrote #%d [%s] %ds ago, in this scope, while "+
		"you were working:\n    %s\n  If that and what you are storing cannot both stand, "+
		"STOP and ask the user which one does, through the question mechanism. Then: "+
		"--supersedes %d if yours stands, or drop yours if theirs does. If they are "+
		"unrelated, carry on.", id, otherType, ago, otherTitle, id)
}

// supersedeClaimRE matches "supersedes #12" and "supersedes preference #12": the optional
// word is the type the text names. Group 1 is that word, group 2 the id.
var supersedeClaimRE = regexp.MustCompile(
	`(?i)\b(?:supersedes|superseding|replaces|replacing|retracts|retracting)\s+` +
		`(?:([a-z]+)\s+)?#(\d+)`)

// SupersedeClaims is every memory id a text SAYS it supersedes, in the active voice.
// "superseded by #9" is the opposite direction and is not a claim.
//
// It exists because the relation is only real when --supersedes records it. A decision was
// once stored whose body read "Supersedes preference #375": no flag, and no #375 in that
// store either, so the rule it killed stayed live, stayed in every brief, and the next
// session obeyed it. The text was right and the store never heard it.
func SupersedeClaims(texts ...string) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, m := range supersedeClaimRE.FindAllStringSubmatch(strings.Join(texts, "\n"), -1) {
		if strings.EqualFold(m[1], "by") {
			continue
		}
		id, err := strconv.ParseInt(m[2], 10, 64)
		if err != nil || id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// UnrecordedClaimNote is doctor's line for a memory ALREADY in the store whose text claims a
// supersession the store does not hold. It reports and repairs nothing: whether #N should be
// retracted is a decision about that project's facts, and the row may simply be telling
// history. The write guard stops new ones; this finds the ones that arrived before it.
func UnrecordedClaimNote(row, claimed int64, state ClaimState) string {
	if state == ClaimRetracted {
		return ""
	}
	if state == ClaimMissing {
		return fmt.Sprintf("#%d says it supersedes #%d, and there is no #%d in this project: "+
			"whatever it meant to retract may still be live under another id (os-brain get %d)",
			row, claimed, claimed, row)
	}
	return fmt.Sprintf("#%d says it supersedes #%d, and #%d is still live: both are being "+
		"recalled (os-brain get %d; if #%d really is retracted, store the correction again "+
		"with --supersedes %d)", row, claimed, claimed, row, claimed, claimed)
}

// ClaimState is what the store found when it looked a claimed id up.
type ClaimState int

const (
	ClaimMissing   ClaimState = iota // no such memory where the caller can write
	ClaimLive                        // it exists and nothing has superseded it
	ClaimRetracted                   // already superseded: the sentence is history, not a claim
)

// SupersedeClaimRefusal is the refusal for one claimed id, or "" when it may pass. flagged
// is what --supersedes was given.
func SupersedeClaimRefusal(claimed, flagged int64, state ClaimState) string {
	if claimed == flagged || state == ClaimRetracted {
		return ""
	}
	if state == ClaimMissing {
		return fmt.Sprintf("the text says it supersedes #%d, and there is no #%d in this "+
			"project. Ids are per store: one read in another project, on another machine or "+
			"before a move is a different memory here. Find the one you mean (os-brain "+
			"recall), then:\n  it IS being retracted     --supersedes <its id>, and name "+
			"that id in the text\n  the sentence is history   reword it, or --claim-ok", claimed, claimed)
	}
	return fmt.Sprintf("the text says it supersedes #%d, but --supersedes was not passed, so "+
		"#%d would stay live beside it: still recalled, still in every brief if it is a "+
		"preference or a rule. Saying it in prose retracts nothing.\n"+
		"  it IS being retracted     --supersedes %d\n"+
		"  the sentence is history   reword it, or --claim-ok", claimed, claimed, claimed)
}
