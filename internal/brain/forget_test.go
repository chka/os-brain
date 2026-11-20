package brain

import (
	"io"
	"strings"
	"testing"
)

// forget deletes for good, so it refuses whatever would hurt, keeps supersession chains
// whole and leaves a tombstone behind.

func TestForgetRefusesGuardrailsAndChainHeads(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Type: "constraint", Title: "never wipe the prod db", Body: "x"})
	if err := s.Forget(io.Discard, ForgetArgs{ID: 1}); err == nil {
		t.Error("a constraint must not be deleted by accident")
	}
	remember(t, s, RememberArgs{Title: "old fact", Body: "x"})
	remember(t, s, RememberArgs{Title: "new fact", Body: "y", Supersedes: 2})
	if err := s.Forget(io.Discard, ForgetArgs{ID: 3}); err == nil {
		t.Error("deleting a chain head would republish what it retracted")
	} else if !strings.Contains(err.Error(), "back into recall") {
		t.Errorf("the refusal must say why, got %v", err)
	}
}

// Deleting the middle of a chain must leave the chain pointing past it, or the oldest fact
// would come back as current.
func TestForgetSplicesTheChainAndLeavesATombstone(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "v1", Body: "a"})
	remember(t, s, RememberArgs{Title: "v2", Body: "b", Supersedes: 1})
	remember(t, s, RememberArgs{Title: "v3", Body: "c", Supersedes: 2})
	// #2 is superseded by #3, and supersedes #1: deleting it must leave #1 pointing at #3.
	if err := s.Forget(io.Discard, ForgetArgs{ID: 2, Reason: "merged"}); err != nil {
		t.Fatal(err)
	}
	m, err := s.Own(1, "read")
	if err != nil {
		t.Fatal(err)
	}
	if m.SupersededBy == nil || *m.SupersededBy != 3 {
		t.Errorf("chain not spliced: %v", m.SupersededBy)
	}
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM tombstones").Scan(&n); err != nil || n != 1 {
		t.Errorf("a deliberate forget must leave a tombstone (%d, %v)", n, err)
	}
}

// A correction may carry the SAME title as the fact it corrects: the retracted row keeps a
// hash derived from its id and the successor takes the title's identity. Keying the
// tombstone on a hash recomputed from the title therefore tombstoned the LIVE successor
// whenever the retracted half was forgotten, and a later restore deleted the wrong
// tombstone.
func TestForgettingARetractedMemoryDoesNotTombstoneItsLiveSuccessor(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "prod db host", Body: "db.internal"})
	remember(t, s, RememberArgs{Title: "prod db host", Body: "db3.internal", Supersedes: 1})

	if err := s.Forget(io.Discard, ForgetArgs{ID: 1, Reason: "wrong from the start"}); err != nil {
		t.Fatal(err)
	}
	live := ContentHash(s.Env.Project, "global", "prod db host")
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM tombstones WHERE hash=?", live).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("the tombstone carries the live successor's identity, so #2 now reads as forgotten")
	}
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM tombstones").Scan(&n); err != nil || n != 1 {
		t.Errorf("the forget must still leave its own tombstone (%d, %v)", n, err)
	}
	m, err := s.Own(2, "read")
	if err != nil {
		t.Fatal(err)
	}
	if m.Hash == nil || *m.Hash != live {
		t.Errorf("#2 no longer holds the title's identity: %v", m.Hash)
	}
}
