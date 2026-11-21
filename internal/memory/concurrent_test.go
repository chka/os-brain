package memory

import (
	"bytes"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/store"
)

// Tests for concurrent.go: supersede claims written in prose, and the alert a session gets
// when another session wrote a conflicting memory moments earlier.

// A body that says "supersedes #N" claims a relation the store only knows about if
// --supersedes recorded it. Only the active voice is a claim: "superseded by #N" points the
// other way, and a bare mention of an id is not a claim at all.
func TestSupersedeClaimsReadsTheActiveVoiceOnly(t *testing.T) {
	for text, want := range map[string][]int64{
		"Supersedes preference #375 (remind every session)": {375},
		"SUPERSEDES #736 (which described only half)":       {736},
		"this replaces #12 and retracts #13":                {12, 13},
		"Superseded by #736. See also #732":                 nil,
		"relates to #283 and #287":                          nil,
		"supersedes #5, and again supersedes #5":            {5},
	} {
		got := SupersedeClaims(text)
		if len(got) != len(want) {
			t.Errorf("%q: got %v, want %v", text, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%q: got %v, want %v", text, got, want)
			}
		}
	}
}

// A decision whose body read "Supersedes preference #375" was once stored with no
// --supersedes flag, and no #375 existed in that store either. The rule it meant to kill
// stayed live, stayed in every brief, and the next session obeyed it. remember now refuses
// a prose claim the store was not told about, and names the way through: --supersedes when
// the claim is a correction, --claim-ok when the sentence is history.
func TestRememberRefusesASupersedeClaimTheStoreWasNotTold(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	try := func(a RememberArgs) (string, error) {
		var out bytes.Buffer
		a.JSON = false
		a.Scope = store.DefaultScope
		err := s.Remember(&out, a)
		return out.String(), err
	}
	if _, err := try(RememberArgs{Type: "preference", Title: "remind me about the audit every session",
		Body: "until decided or killed"}); err != nil {
		t.Fatal(err)
	}
	kill := RememberArgs{Type: "decision", Title: "the audit is killed",
		Body: "Supersedes preference #1. Stop raising it."}
	if _, err := try(kill); err == nil || !strings.Contains(err.Error(), "--supersedes 1") {
		t.Fatalf("a prose claim on a live row must be refused with the way out: %v", err)
	}
	missing := kill
	missing.Body = "Supersedes preference #375. Stop raising it."
	if _, err := try(missing); err == nil || !strings.Contains(err.Error(), "no #375") {
		t.Fatalf("a claim on an id this project does not hold must say so: %v", err)
	}
	missing.Supersedes = 375
	if _, err := try(missing); err == nil || !strings.Contains(err.Error(), "no #375") {
		t.Fatalf("--supersedes on a missing id stored the correction and corrected "+
			"nothing; it must refuse: %v", err)
	}
	history := kill
	history.Title, history.ClaimOK = "how the audit decision went", true
	if _, err := try(history); err != nil {
		t.Fatalf("--claim-ok is the way through for a sentence that is history: %v", err)
	}
	kill.Supersedes = 1
	if _, err := try(kill); err != nil {
		t.Fatalf("the flag matching the text is the ordinary correction: %v", err)
	}
	var live int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM memories WHERE id=1 AND superseded_by IS NULL").
		Scan(&live); err != nil || live != 0 {
		t.Fatalf("#1 must be retracted now (live=%d, %v)", live, err)
	}
	// Once #1 is retracted, a later text mentioning that it was superseded is history.
	if _, err := try(RememberArgs{Type: "knowledge", Title: "why nobody raises the audit any more",
		Body: "the kill decision supersedes #1"}); err != nil {
		t.Fatalf("a claim about an already-retracted row is not a claim: %v", err)
	}
}

// Two sessions in one project, one storing "we will do A" and the other "B" less than two
// minutes later: the second must be told, and told to ask the user which stands. Nothing
// else alerts: not the same session, not a write outside the window, not an unrelated fact.
func TestASecondSessionIsAlertedInsideTheWindowAndOnlyThen(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	write := func(session, title, memType string) string {
		t.Helper()
		t.Setenv("OS_BRAIN_SESSION_ID", session)
		var out bytes.Buffer
		if err := s.Remember(&out, RememberArgs{Type: memType, Title: title, Body: "because", Scope: store.DefaultScope}); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if out := write("sessionAAA", "billing goes direct to the card processor", "decision"); strings.Contains(out, "ALERT") {
		t.Fatalf("the first write has nobody to conflict with:\n%s", out)
	}
	// Two decisions share almost no words and still alert: inside the window, two decisions
	// in one scope are treated as the same subject.
	out := write("sessionBBB", "the payment gateway stays the only route", "decision")
	if !strings.Contains(out, "ALERT") || !strings.Contains(out, "billing goes direct") ||
		!strings.Contains(out, "ask the user") {
		t.Fatalf("two decisions from two sessions inside the window must alert the second:\n%s", out)
	}
	// A further write alerts at most once, and the alert never prints the other session's id.
	if out := write("sessionBBB", "the fallback order is processor then gateway", "decision"); strings.Contains(out, "sessionAAA") ||
		strings.Count(out, "ALERT") > 1 {
		t.Fatalf("unexpected:\n%s", out)
	}
	// The same session never alerts itself.
	if _, err := s.DB.Exec("DELETE FROM memories WHERE session='sessionAAA'"); err != nil {
		t.Fatal(err)
	}
	if out := write("sessionBBB", "metrics use the shared secret header", "decision"); strings.Contains(out, "ALERT") {
		t.Fatalf("a session must not be alerted about its own writes:\n%s", out)
	}
	// Outside the window it is an ordinary write.
	if _, err := s.DB.Exec("UPDATE memories SET created_at=created_at-300, updated_at=updated_at-300"); err != nil {
		t.Fatal(err)
	}
	if out := write("sessionCCC", "deploys wait for the metrics check", "decision"); strings.Contains(out, "ALERT") {
		t.Fatalf("five minutes later is not concurrent:\n%s", out)
	}
	// An unrelated fact inside the window is not a conflict either.
	if out := write("sessionDDD", "the office plants are watered on fridays", "knowledge"); strings.Contains(out, "ALERT") {
		t.Fatalf("an unrelated fact must not alert:\n%s", out)
	}
}
