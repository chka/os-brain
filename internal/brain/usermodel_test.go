package brain

import (
	"database/sql"
	"testing"
)

// The user model holds traits of the person the agent works for (how deep to go, whether they
// want sources), each with the evidence it was inferred from, so a trait can be checked and
// challenged instead of trusted blindly.

// A trait comes back with its value, its evidence and the time it was last updated.
func TestObserveUserModelRoundTrip(t *testing.T) {
	s := newTestStore(t)
	if err := s.Observe("cites_sources", "yes", "asked for the RFC text, not a summary, twice"); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	traits, err := s.UserModel()
	if err != nil {
		t.Fatalf("UserModel: %v", err)
	}
	if len(traits) != 1 {
		t.Fatalf("want 1 trait, got %d (%+v)", len(traits), traits)
	}
	tr := traits[0]
	if tr.Key != "cites_sources" || tr.Value != "yes" {
		t.Fatalf("unexpected trait: %+v", tr)
	}
	if tr.Evidence == "" {
		t.Fatal("evidence was not preserved")
	}
	if tr.UpdatedAt == 0 {
		t.Fatal("updated_at was not stamped")
	}
}

// A trait without evidence is a guess about a person stated as fact, so it is refused and
// nothing is written.
func TestObserveEmptyEvidenceRefused(t *testing.T) {
	s := newTestStore(t)
	if err := s.Observe("cites_sources", "yes", ""); err == nil {
		t.Fatal("Observe with empty evidence should be refused")
	}
	traits, err := s.UserModel()
	if err != nil {
		t.Fatalf("UserModel: %v", err)
	}
	if len(traits) != 0 {
		t.Fatalf("a refused Observe must not have written anything, got %+v", traits)
	}
}

// A trait has one current value: observing the same key again replaces it, rather than
// leaving two contradictory answers for the reader to choose between.
func TestObserveTwiceUpdatesNotDuplicates(t *testing.T) {
	s := newTestStore(t)
	if err := s.Observe("depth", "peer-to-peer", "asked no beginner caveats"); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if err := s.Observe("depth", "expert", "corrected the agent's TLS claim, and was right"); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	traits, err := s.UserModel()
	if err != nil {
		t.Fatalf("UserModel: %v", err)
	}
	if len(traits) != 1 {
		t.Fatalf("want 1 trait after a second Observe on the same key, got %d (%+v)", len(traits), traits)
	}
	if traits[0].Value != "expert" {
		t.Fatalf("want the updated value, got %q", traits[0].Value)
	}
}

// A wrong trait can be removed, and removing one that is not there is an error, so a typo in
// the key is not reported as success.
func TestForgetTrait(t *testing.T) {
	s := newTestStore(t)
	if err := s.Observe("mood", "grumpy", "one bad session, not a pattern"); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if err := s.ForgetTrait("mood"); err != nil {
		t.Fatalf("ForgetTrait: %v", err)
	}
	traits, err := s.UserModel()
	if err != nil {
		t.Fatalf("UserModel: %v", err)
	}
	if len(traits) != 0 {
		t.Fatalf("trait should be gone, got %+v", traits)
	}
	if err := s.ForgetTrait("mood"); err == nil {
		t.Fatal("forgetting a trait that is not there should error, not silently succeed")
	}
}

// The user model is a profile of a person, not of a project: it belongs in the spine, and
// must never land in the per-project store.
func TestUserModelLandsInSpineNotProject(t *testing.T) {
	s := newTestStore(t)
	if err := s.Observe("depth", "expert", "corrected a TLS claim and was right"); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	var name string
	err := s.DB.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='user_traits'").
		Scan(&name)
	if err != sql.ErrNoRows {
		t.Fatalf("user_traits must not exist in the project store; err=%v name=%q", err, name)
	}
}
