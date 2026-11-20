package brain

import (
	"strings"
	"testing"
)

// The ladder records how settled a memory is. A new memory is "stated". Restated from a
// different session it becomes "law": the user said it again on another occasion, so it is a
// rule rather than a passing remark. Restated after someone objected to it, it becomes
// "hardened": it survived a challenge. Objections are kept with their reasons, and only the
// user can reopen a law.

// mkMemory creates a fresh memory and returns its id. Each test gets its own store
// (newTestStore), so the most recently inserted row is unambiguously this one.
func mkMemory(t *testing.T, s *Store, title string) int64 {
	t.Helper()
	remember(t, s, RememberArgs{Type: "knowledge", Title: title, Body: "body of " + title})
	var id int64
	if err := s.DB.QueryRow("SELECT id FROM memories ORDER BY id DESC LIMIT 1").Scan(&id); err != nil {
		t.Fatalf("could not find memory %q: %v", title, err)
	}
	return id
}

// The starting rung: one statement, no hardening and no law yet.
func TestFreshMemoryIsStated(t *testing.T) {
	s := newTestStore(t)
	id := mkMemory(t, s, "fresh memory one")

	ls, err := s.LadderOf(id)
	if err != nil {
		t.Fatal(err)
	}
	if ls.State != "stated" {
		t.Fatalf("state = %q, want stated", ls.State)
	}
	if ls.StatedCount != 1 {
		t.Fatalf("stated_count = %d, want 1", ls.StatedCount)
	}
	if ls.HardenedAt != nil || ls.LawAt != nil {
		t.Fatalf("fresh memory should have no hardened_at/law_at: %+v", ls)
	}
}

// Repeating something within one session is emphasis, not confirmation on another occasion.
func TestRestateSameSessionStaysStated(t *testing.T) {
	s := newTestStore(t)
	id := mkMemory(t, s, "restate same session")

	// Remember() wrote the memory under this process's session. Restating from the same
	// session must not make it law: that is the mistake this rule exists to catch.
	ls, err := s.AdvanceLadder(id, s.Env.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	if ls.State != "stated" {
		t.Fatalf("state = %q, want stated (same session restated)", ls.State)
	}
	if ls.StatedCount != 2 {
		t.Fatalf("stated_count = %d, want 2", ls.StatedCount)
	}
	if ls.LawAt != nil {
		t.Fatalf("law_at set on a same-session restatement: %+v", ls)
	}
}

// A second session saying the same thing makes it law, and the date it became law is the
// first such date: later restatements count but do not rewrite that history.
func TestRestateDifferentSessionBecomesLawAndStays(t *testing.T) {
	s := newTestStore(t)
	id := mkMemory(t, s, "restate different session")

	ls, err := s.AdvanceLadder(id, "session-two")
	if err != nil {
		t.Fatal(err)
	}
	if ls.State != "law" {
		t.Fatalf("state = %q, want law", ls.State)
	}
	if ls.LawAt == nil {
		t.Fatal("law_at not set")
	}
	firstLawAt := *ls.LawAt

	// A further restatement, even from yet another session, must not move law_at: the
	// first time law is reached is the only time it can be reached.
	ls2, err := s.AdvanceLadder(id, "session-three")
	if err != nil {
		t.Fatal(err)
	}
	if ls2.LawAt == nil || *ls2.LawAt != firstLawAt {
		t.Fatalf("law_at moved: was %d now %+v", firstLawAt, ls2.LawAt)
	}
	if ls2.StatedCount != 3 {
		t.Fatalf("stated_count = %d, want 3", ls2.StatedCount)
	}
}

// An objection alone changes nothing, but a restatement after an objection hardens the memory,
// and the objection is kept with its reason and date so the challenge can be read later.
func TestObjectionThenRestatementHardens(t *testing.T) {
	s := newTestStore(t)
	id := mkMemory(t, s, "objection then restate")

	objLS, err := s.Object(id, "this contradicts what we measured last week", "session-obj")
	if err != nil {
		t.Fatal(err)
	}
	if objLS.State != "stated" {
		t.Fatalf("state after objection alone = %q, want still stated", objLS.State)
	}

	// Restating from the SAME session as creation, so this test isolates hardening from
	// law: only the objection should move the state.
	ls, err := s.AdvanceLadder(id, s.Env.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	if ls.State != "hardened" {
		t.Fatalf("state = %q, want hardened", ls.State)
	}
	if ls.HardenedAt == nil {
		t.Fatal("hardened_at not set")
	}

	objs, err := s.Objections(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 {
		t.Fatalf("len(objections) = %d, want 1", len(objs))
	}
	if objs[0].Why != "this contradicts what we measured last week" {
		t.Fatalf("objection reasoning lost: %+v", objs[0])
	}
	if objs[0].RaisedAt == 0 {
		t.Fatal("objection has no date")
	}
}

// An agent cannot argue a law away. Its objection is recorded, but the state stays law and the
// answer says only the user can reopen it.
func TestObjectionAgainstLawSaysOnlyUserCanReopen(t *testing.T) {
	s := newTestStore(t)
	id := mkMemory(t, s, "law memory objected")

	if _, err := s.AdvanceLadder(id, "session-two"); err != nil {
		t.Fatal(err)
	}
	ls, err := s.LadderOf(id)
	if err != nil {
		t.Fatal(err)
	}
	if ls.State != "law" {
		t.Fatalf("setup failed: state = %q, want law", ls.State)
	}

	objLS, err := s.Object(id, "I still think this is wrong", "session-three")
	if err != nil {
		t.Fatal(err)
	}
	if objLS.State != "law" {
		t.Fatalf("objecting must not change law state: got %q", objLS.State)
	}
	if !strings.Contains(strings.ToLower(objLS.Why), "user") {
		t.Fatalf("Why does not say only the user can reopen it: %q", objLS.Why)
	}

	objs, err := s.Objections(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 {
		t.Fatalf("objection against a law memory was not recorded: %d rows", len(objs))
	}
}

// The latest objection is the one a reader needs first.
func TestObjectionsNewestFirst(t *testing.T) {
	s := newTestStore(t)
	id := mkMemory(t, s, "objections ordering")

	if _, err := s.Object(id, "first objection", "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Object(id, "second objection", "s2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Object(id, "third objection", "s3"); err != nil {
		t.Fatal(err)
	}

	objs, err := s.Objections(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 3 {
		t.Fatalf("len(objections) = %d, want 3", len(objs))
	}
	want := []string{"third objection", "second objection", "first objection"}
	for i, w := range want {
		if objs[i].Why != w {
			t.Fatalf("objections[%d] = %q, want %q (newest first)", i, objs[i].Why, w)
		}
	}
}

// A session may only move memories of its own project; ids are not secret, and another
// project's rules are not this session's to promote.
func TestAdvanceLadderRefusesAnotherProjectsMemory(t *testing.T) {
	s := newTestStore(t)
	id := mkMemory(t, s, "belongs to testproj")

	s.Env.Project = "someone-elses-project"
	if _, err := s.AdvanceLadder(id, ""); err == nil {
		t.Fatal("expected AdvanceLadder to refuse a memory owned by another project")
	}
}

// An objection without a reason cannot be weighed later, so a blank one is refused.
func TestObjectRequiresAReason(t *testing.T) {
	s := newTestStore(t)
	id := mkMemory(t, s, "needs a reason")

	if _, err := s.Object(id, "   ", ""); err == nil {
		t.Fatal("expected Object to refuse an empty reason")
	}
}
