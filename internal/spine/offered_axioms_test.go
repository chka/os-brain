package spine

import (
	"testing"

	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/timefmt"
)

// Axioms are the few rules shown in every project's brief, kept in the spine (the per-user
// database every project shares). The binary ships a list of defaults and offers them to the
// spine. These tests pin how that list evolves across releases without trampling the owner:
// a new default arrives once, a removed one stays removed, a retired one leaves, and the
// owner's own axioms are never touched.

// A default added in a later release reaches a spine that already exists, on the first open
// by that release, and comes first in the order the binary lists it. Before this a spine
// seeded once and never read the list again, so a new default reached new installs only.
func TestADefaultAddedLaterReachesAnExistingSpineOnce(t *testing.T) {
	s := newTestStore(t)
	g := s.GlobalConnect(true)
	if g == nil {
		t.Fatal("no spine")
	}
	s.CloseGlobal(g)
	saved := store.DefaultAxioms
	t.Cleanup(func() { store.DefaultAxioms = saved })
	added := [2]string{"A rule we ship next year.", "Because a later release can add one."}
	store.DefaultAxioms = append([][2]string{added}, saved...)

	got := s.Axioms()
	if len(got) != len(store.DefaultAxioms) || got[0].Text != added[0] {
		t.Fatalf("got %d axioms with %q first, want %d with the new default first",
			len(got), got[0].Text, len(store.DefaultAxioms))
	}
	// A second open offers nothing again.
	if again := s.Axioms(); len(again) != len(got) {
		t.Errorf("a second open changed the count from %d to %d", len(got), len(again))
	}
}

// A default the owner removed stays removed when the spine is opened again, including by a
// release that ships more defaults than the one that seeded it.
func TestARemovedDefaultIsNotOfferedAgain(t *testing.T) {
	s := newTestStore(t)
	g := s.GlobalConnect(true)
	if g == nil {
		t.Fatal("no spine")
	}
	if _, err := g.Exec("DELETE FROM axioms WHERE text=?", store.DefaultAxioms[0][0]); err != nil {
		t.Fatal(err)
	}
	s.CloseGlobal(g)
	for _, a := range s.Axioms() {
		if a.Text == store.DefaultAxioms[0][0] {
			t.Fatal("a default the owner removed came back")
		}
	}
}

// A spine seeded by a release that shipped more defaults ends up with today's set when a
// newer binary opens it: a withdrawn default leaves, a reworded one takes the new text once.
// A copy the owner wrote, even one word for word the same, is theirs and stays.
func TestARetiredDefaultLeavesAnExistingSpine(t *testing.T) {
	s := newTestStore(t)
	g := s.GlobalConnect(true)
	if g == nil {
		t.Fatal("no spine")
	}
	if _, err := g.Exec("DELETE FROM axioms"); err != nil {
		t.Fatal(err)
	}
	ts := timefmt.Now()
	target := map[string]bool{}
	for _, r := range store.RetiredDefaults {
		target[r[1]] = true
	}
	for _, keep := range store.DefaultAxioms {
		if target[keep[0]] {
			continue
		}
		if _, err := g.Exec("INSERT INTO axioms(text,rationale,origin_project,seeded,created_at,updated_at) VALUES(?,?,'acme',1,?,?)",
			keep[0], keep[1], ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range store.RetiredDefaults {
		if _, err := g.Exec("INSERT INTO axioms(text,rationale,origin_project,seeded,created_at,updated_at) VALUES(?,'old reason','acme',1,?,?)",
			r[0], ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	ownerCopy := "Know what else exists, in my words."
	if _, err := g.Exec("INSERT INTO axioms(text,rationale,origin_project,seeded,created_at,updated_at) VALUES(?,'','',0,?,?)",
		ownerCopy, ts, ts); err != nil {
		t.Fatal(err)
	}
	if err := store.RetireDefaultAxioms(g); err != nil {
		t.Fatal(err)
	}
	if err := store.OfferDefaultAxioms(g); err != nil {
		t.Fatal(err)
	}
	s.CloseGlobal(g)
	got := s.Axioms()
	if len(got) != len(store.DefaultAxioms)+1 {
		t.Fatalf("got %d axioms, want the %d defaults and the owner's own", len(got), len(store.DefaultAxioms))
	}
	for i, d := range store.DefaultAxioms {
		if got[i].Text != d[0] || got[i].Rationale != d[1] {
			t.Errorf("axiom %d is %q (%q), want %q (%q)", i+1, got[i].Text, got[i].Rationale, d[0], d[1])
		}
	}
	if got[len(got)-1].Text != ownerCopy {
		t.Errorf("the owner's own axiom is gone or moved: last is %q", got[len(got)-1].Text)
	}
}

// Every retired default names a current default or nothing, so a reword never points at a
// text no release ships.
func TestRetiredDefaultsPointAtCurrentDefaults(t *testing.T) {
	for _, r := range store.RetiredDefaults {
		if store.DefaultAxiomIndex(r[0]) >= 0 {
			t.Errorf("%q is both retired and shipped", r[0])
		}
		if r[1] != "" && store.DefaultAxiomIndex(r[1]) < 0 {
			t.Errorf("%q is reworded to a text no default carries", r[0])
		}
	}
}
