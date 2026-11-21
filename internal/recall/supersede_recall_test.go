package recall

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/store"
)

// A broken chain must not hang the walk or lose the row. No command deletes a successor
// this way, but a damaged file might.
func TestABrokenSupersessionChainStillAnswers(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "the old wording here", Body: "x", Type: "note"})
	remember(t, s, RememberArgs{Title: "the new wording here", Body: "y", Type: "note",
		Supersedes: 1})
	if _, err := s.DB.Exec("DELETE FROM memories WHERE id=2"); err != nil {
		t.Fatal(err)
	}
	got := titles(recall(t, s, RecallArgs{Query: "old wording", N: 5, Browse: true, NoTouch: true}))
	if len(got) != 1 || !strings.Contains(got[0], "old wording") {
		t.Errorf("a dangling successor should leave the original answerable, got %v", got)
	}
}

// Superseded rows stay in the search; the walk resolves each one to its successor and the
// exclusion happens after the walk. An earlier version filtered superseded rows out before
// the walk, so the forwarding loop could never run and an outdated wording found nothing,
// or worse, the outdated fact.
func TestOutdatedWordingAnswersWithTheCurrentFact(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "the deploy host is alpha", Body: "alpha runs it"})
	remember(t, s, RememberArgs{Title: "the deploy host is beta",
		Body: "beta runs it now", Supersedes: 1})

	// The word only the RETRACTED memory ever used.
	got := titles(recall(t, s, RecallArgs{Query: "alpha"}))
	if len(got) != 1 || !strings.Contains(got[0], "beta") {
		t.Errorf("a query in the outdated wording should answer with the successor, got %v", got)
	}
	// And it answers once, not twice: the outdated row and its successor must not both
	// come back, which would be the error competing with its own retraction.
	got = titles(recall(t, s, RecallArgs{Query: "deploy host"}))
	if len(got) != 1 {
		t.Errorf("expected one answer for a query matching both versions, got %v", got)
	}
	// --all still shows the outdated version, because that is what it is for.
	got = titles(recall(t, s, RecallArgs{Query: "alpha", All: true}))
	if len(got) == 0 {
		t.Errorf("--all should still show the outdated version, got %v", got)
	}
	// The successor is still found on its own wording.
	got = titles(recall(t, s, RecallArgs{Query: "beta"}))
	if len(got) != 1 || !strings.Contains(got[0], "beta") {
		t.Errorf("the successor should be found on its own wording, got %v", got)
	}
}

// When recall reaches a successor through a retracted row, it says so. Without that, a note
// another session left and then retracted was invisible: every search for its words
// answered with the successor and no sign of the original, which only somebody who already
// knew its id could read.
func TestRecallNamesTheRetractedMemoryItMatched(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	for _, a := range []RememberArgs{
		{Title: "the nightly backup runs from cron at two", Body: "crontab on the old box"},
		{Title: "the nightly backup moved to the scheduler", Body: "the earlier note was wrong", Supersedes: 1},
	} {
		a.Scope, a.Type = store.DefaultScope, "gotcha"
		if err := s.Remember(&bytes.Buffer{}, a); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := s.Recall(&out, RecallArgs{Query: "crontab", N: 8, NoTouch: true}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "#2") || !strings.Contains(text, "matched the RETRACTED #1") ||
		!strings.Contains(text, "os-brain get 1") {
		t.Fatalf("the successor must lead and name the retracted row it was reached by:\n%s", text)
	}
	if strings.Contains(text, "crontab on the old box") {
		t.Errorf("the retracted BODY must not answer as though it stood:\n%s", text)
	}
	out.Reset()
	if err := s.Recall(&out, RecallArgs{Query: "crontab", N: 8, NoTouch: true, JSON: true}); err != nil {
		t.Fatal(err)
	}
	var env struct {
		Results []struct {
			ID  int64          `json:"id"`
			Via []store.ViaRef `json:"matched_via"`
		} `json:"results"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Results) != 1 || env.Results[0].ID != 2 || len(env.Results[0].Via) != 1 ||
		env.Results[0].Via[0].ID != 1 {
		t.Fatalf("matched_via is the machine-readable half: %s", out.String())
	}
	out.Reset()
	if err := s.Recall(&out, RecallArgs{Query: "scheduler", N: 8, NoTouch: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "RETRACTED") {
		t.Errorf("a row matched on its own text was reached via nothing:\n%s", out.String())
	}
}

// The same guarantee three deep: a query in the OLDEST wording answers with the NEWEST fact.
func TestSupersessionForwardsThroughAChain(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "the queue runs on rabbit", Body: "rabbitmq on 5672",
		Type: "decision"})
	remember(t, s, RememberArgs{Title: "the queue runs on redis", Body: "redis streams",
		Type: "decision", Supersedes: 1})
	remember(t, s, RememberArgs{Title: "the queue runs on postgres", Body: "skip locked",
		Type: "decision", Supersedes: 2})

	for _, q := range []string{"rabbitmq", "5672", "redis streams"} {
		got := titles(recall(t, s, RecallArgs{Query: q, N: 10, Strict: true, NoTouch: true}))
		if len(got) != 1 {
			t.Errorf("%q returned %d answers, want exactly the current one: %v", q, len(got), got)
			continue
		}
		if !strings.Contains(got[0], "postgres") {
			t.Errorf("%q answered %q; the current fact is the postgres one", q, got[0])
		}
	}
	all := titles(recall(t, s, RecallArgs{Query: "queue runs", N: 10, All: true, NoTouch: true}))
	if len(all) < 3 {
		t.Errorf("--all should show every version, got %v", all)
	}
}
