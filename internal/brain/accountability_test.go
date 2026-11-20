package brain

// Tests for session accountability and for the rules on what may claim authority.
//
// A session that learned things and stored none used to be indistinguishable from one that
// learned nothing. The stop hook now reports what a session left behind, and the next brief
// tells the next session. The same file covers the two other places where a session could
// claim more than it can show: marking a memory `confirmed`, and changing the axioms that
// bind every project.

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// --- What a session leaves behind ---

// A long session that stored nothing is reported by the stop hook, then told to the next
// session (the one that can still act on it), and told only once.
func TestASilentSessionIsVisibleToTheNextOne(t *testing.T) {
	s := newTestStore(t)
	t.Setenv("OS_BRAIN_SESSION_ID", "session-one")

	// A session opens, runs long enough to have learned something, and writes nothing.
	if err := s.Brief(io.Discard, BriefArgs{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("UPDATE session_log SET started_at=started_at-3600 " +
		"WHERE session='session-one'"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := s.SessionEnd(&out, SessionEndArgs{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "stored 0 memories") {
		t.Errorf("the stop hook said nothing about an empty session:\n%s", out.String())
	}

	// The next session is told, because that is the one that can still act on it.
	t.Setenv("OS_BRAIN_SESSION_ID", "session-two")
	var brief bytes.Buffer
	if err := s.Brief(&brief, BriefArgs{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(brief.String(), "LEFT NOTHING BEHIND") {
		t.Errorf("the brief hid the previous session's silence:\n%s", brief.String())
	}

	// And only once. The same warning every morning for a week is wallpaper.
	var again bytes.Buffer
	if err := s.Brief(&again, BriefArgs{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(again.String(), "LEFT NOTHING BEHIND") {
		t.Errorf("the same silent session was reported twice:\n%s", again.String())
	}
}

// Remembering and logging answer different questions, so the stop hook accounts for them
// separately: a session that stored and logged hears nothing, one that stored and never
// logged is told which half is missing, and a session that did nothing at all is not news.
// Storing a memory once bought total silence, so a project could store steadily, log
// nothing for its whole life and never be told.
func TestSessionsThatWroteOrDidNothingAreNotFlagged(t *testing.T) {
	s := newTestStore(t)
	t.Setenv("OS_BRAIN_SESSION_ID", "worked-and-stored")
	if err := s.Brief(io.Discard, BriefArgs{}); err != nil {
		t.Fatal(err)
	}
	remember(t, s, RememberArgs{Title: "something worth keeping", Body: "x"})
	if err := s.Log(io.Discard, LogArgs{Summary: "did the work", Kind: "note", Scope: DefaultScope}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("UPDATE session_log SET started_at=started_at-7200"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := s.SessionEnd(&out, SessionEndArgs{}); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("a session that stored a memory AND logged was told off:\n%s", out.String())
	}

	t.Setenv("OS_BRAIN_SESSION_ID", "stored-but-never-logged")
	if err := s.Brief(io.Discard, BriefArgs{}); err != nil {
		t.Fatal(err)
	}
	remember(t, s, RememberArgs{Title: "another thing worth keeping", Body: "y"})
	if _, err := s.DB.Exec("UPDATE session_log SET started_at=started_at-7200 " +
		"WHERE session='stored-but-never-logged'"); err != nil {
		t.Fatal(err)
	}
	var half bytes.Buffer
	if err := s.SessionEnd(&half, SessionEndArgs{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(half.String(), "logged nothing") {
		t.Errorf("a session that stored and never logged was not told:\n%s", half.String())
	}

	t.Setenv("OS_BRAIN_SESSION_ID", "did-nothing")
	if err := s.Brief(io.Discard, BriefArgs{}); err != nil {
		t.Fatal(err)
	}
	var quick bytes.Buffer
	if err := s.SessionEnd(&quick, SessionEndArgs{}); err != nil {
		t.Fatal(err)
	}
	if quick.Len() != 0 {
		t.Errorf("a two-second session was treated as a failure:\n%s", quick.String())
	}
}

// --- Confirmed needs evidence ---

// confirmed is the only field in a memory that cannot be re-derived later, so it is the
// only one that must not be taken on the writer's word. It needs something somebody else
// could check: a file, a command, a citation in --source, or the user saying so. A bare
// self-report is refused, and the refusal names --source as the way through.
func TestConfirmedNeedsSomethingSomebodyElseCouldCheck(t *testing.T) {
	s := newTestStore(t)
	cases := []struct {
		name string
		args RememberArgs
		want string
	}{
		{"a file", RememberArgs{Title: "the fts index is written in code not triggers",
			Body: "see internal/brain/schema.go", Confidence: "confirmed"}, "confirmed"},
		{"a command", RememberArgs{Title: "the suite passes on this tree",
			Body: "ran `go test ./...`", Confidence: "confirmed"}, "confirmed"},
		{"a citation in the source flag", RememberArgs{Title: "the spine holds five tables",
			Body: "counted them", Confidence: "confirmed", Source: "internal/brain/global.go"},
			"confirmed"},
		{"the user saying so", RememberArgs{Title: "he wants short commit messages",
			Body: "", Confidence: "confirmed", Source: "stated by the user"}, "confirmed"},
	}
	bare := RememberArgs{Title: "the migration ran cleanly", Type: "note", Scope: "global",
		Body: "I ran it and it worked", Confidence: "confirmed", Source: "session"}
	if err := s.Remember(io.Discard, bare); err == nil || !strings.Contains(err.Error(), "--source") {
		t.Errorf("a bare self-report was not refused with the way through: %v", err)
	}
	for _, c := range cases {
		c.args.Type = "note"
		c.args.Scope = "global"
		remember(t, s, c.args)
		var got string
		if err := s.DB.QueryRow("SELECT confidence FROM memories WHERE title=?",
			c.args.Title).Scan(&got); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: stored as %q, wanted %q", c.name, got, c.want)
		}
	}
}

// update and verify would be two ways round the rule if it lived only in remember.
func TestConfirmedCannotBeClaimedLaterWithoutACitation(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "a claim with nothing behind it", Body: "trust me"})
	conf := "confirmed"
	if err := s.Update(io.Discard, UpdateArgs{ID: 1, Confidence: &conf}); err == nil {
		t.Error("update claimed confirmed without evidence")
	}
	if err := s.Verify(io.Discard, VerifyArgs{ID: 1, Confidence: "confirmed"}); err == nil {
		t.Error("verify claimed confirmed without evidence")
	}
	var got string
	if err := s.DB.QueryRow("SELECT confidence FROM memories WHERE id=1").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got == "confirmed" {
		t.Errorf("a refused claim still landed as %q", got)
	}
	// With a citation it goes through, because the point is evidence, not refusal.
	if err := s.Verify(io.Discard, VerifyArgs{ID: 1, Confidence: "confirmed",
		Source: "checked against internal/brain/store.go"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow("SELECT confidence FROM memories WHERE id=1").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "confirmed" {
		t.Errorf("a cited verify was still downgraded to %q", got)
	}
}

// --- Axioms are changed by a person ---

// Axioms bind every project, so changing one is a person's act at a terminal (Confirm). Who
// wrote each is recorded, removing a core one says what it was, and the next brief is told.
func TestAxiomsCarryTheirOriginAndTheCoreSetResistsRemoval(t *testing.T) {
	s := newTestStore(t)
	t.Setenv("OS_BRAIN_SESSION_ID", "an-agent-somewhere-else")
	if err := s.Axiom(io.Discard, AxiomArgs{Confirm: yes, Sub: "add", Text: "never deploy on a Friday",
		Why: "nobody is around on Saturday"}); err != nil {
		t.Fatal(err)
	}
	var seeded, custom Axiom
	for _, a := range s.Axioms() {
		if a.Seeded {
			seeded = a
		} else {
			custom = a
		}
	}
	if seeded.ID == 0 || custom.ID == 0 {
		t.Fatal("expected both a seeded and a custom axiom")
	}
	if custom.Origin != "testproj" || custom.Session != "an-agent-somewhere-else" {
		t.Errorf("a written axiom lost its provenance: %+v", custom)
	}
	// A session cannot remove one at all without a person: that is the person gate.
	if err := s.Axiom(io.Discard, AxiomArgs{Sub: "rm", ID: seeded.ID}); err == nil {
		t.Error("removing a core axiom without a person was not refused")
	}
	// The person's yes is the whole confirmation: a command issued is a command meant, so
	// no extra flag is asked for on top of it, and the prompt they answer says the axiom is
	// core.
	var asked string
	confirm := func(summary string) (bool, error) { asked = summary; return true, nil }
	var said bytes.Buffer
	if err := s.Axiom(&said, AxiomArgs{Confirm: confirm, Sub: "rm", ID: seeded.ID}); err != nil {
		t.Errorf("removing a core axiom was refused: %v", err)
	}
	if !strings.Contains(asked, "core set") {
		t.Errorf("the person was not told the axiom is core: %q", asked)
	}
	if !strings.Contains(said.String(), "core set") {
		t.Errorf("removing a core axiom did not say what it was: %q", said.String())
	}
	// A custom one goes without ceremony either.
	if err := s.Axiom(io.Discard, AxiomArgs{Confirm: yes, Sub: "rm", ID: custom.ID}); err != nil {
		t.Errorf("removing a custom axiom was refused: %v", err)
	}
}

// The rules every project obeys must never change silently: the next brief announces a
// new axiom, once.
func TestTheBriefSaysWhenTheAxiomsChanged(t *testing.T) {
	s := newTestStore(t)
	// The spine has to exist before there is anything to fingerprint: until something
	// creates it, Axioms() serves the built-in set, which cannot change without the
	// binary changing.
	if err := s.Axiom(io.Discard, AxiomArgs{Confirm: yes, Sub: "add", Text: "seed the spine"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Brief(io.Discard, BriefArgs{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Axiom(io.Discard, AxiomArgs{Confirm: yes, Sub: "add",
		Text: "pull before you work, another session may have been here"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := s.Brief(&out, BriefArgs{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "AXIOMS CHANGED") ||
		!strings.Contains(out.String(), "pull before you work") {
		t.Errorf("a new axiom arrived silently:\n%s", out.String())
	}
	var again bytes.Buffer
	if err := s.Brief(&again, BriefArgs{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(again.String(), "AXIOMS CHANGED") {
		t.Errorf("the same change was announced twice:\n%s", again.String())
	}
}
