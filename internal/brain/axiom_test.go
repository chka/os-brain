package brain

import (
	"bytes"
	"io"
	"testing"
)

// Axioms are the machine-wide rules every project's brief carries. They live once, in the
// shared spine, and are read live; every change is confirmed by the person at the terminal
// (Confirm: yes stands in for that person here).

// Axioms are pulled, never propagated: one row, read live by every brief.
func TestAxiomAddIsReadableByEveryProjectImmediately(t *testing.T) {
	s := newTestStore(t)
	if err := s.Axiom(io.Discard, AxiomArgs{Confirm: yes, Sub: "add", Text: "never force push",
		Why: "it destroys someone else's work"}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ax := range s.Axioms() {
		if ax.Text == "never force push" {
			found = true
			if ax.Rationale == "" {
				t.Error("the rationale was dropped")
			}
		}
	}
	if !found {
		t.Error("a stored axiom is not read back")
	}
}

// The built-in set is seeded once. Treating the built-ins as a fallback for an empty table
// would mean deleting the last axiom silently brings them all back.
func TestDeletingAnAxiomDoesNotResurrectTheBuiltInSet(t *testing.T) {
	s := newTestStore(t)
	// Touch the spine so the seed runs: until something creates it, Axioms() serves the
	// built-in fallback, whose rows have no id to delete.
	if err := s.Axiom(io.Discard, AxiomArgs{Confirm: yes, Sub: "add", Text: "seed the spine"}); err != nil {
		t.Fatal(err)
	}
	all := s.Axioms()
	if len(all) == 0 {
		t.Fatal("no axioms seeded")
	}
	for _, ax := range all {
		if err := s.Axiom(io.Discard, AxiomArgs{Confirm: yes, Sub: "rm", ID: ax.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.Axioms(); len(got) != 0 {
		t.Errorf("deleted axioms came back: %d", len(got))
	}
}

// Attribution is the only thing separating an authoritative rule from an anonymous one.
// Rewording an axiom by adding the same text again used to stamp whoever was editing as its
// author. Both halves are asserted, because the guarantee is a contrast between two
// commands: edit keeps the origin, add-again keeps the origin and records an editor.
func TestEditingAnAxiomKeepsItsOriginAndAddingRecordsAnEditor(t *testing.T) {
	s := newTestStore(t)
	var out bytes.Buffer
	if err := s.Axiom(&out, AxiomArgs{Confirm: yes, Sub: "add", Text: "a rule written elsewhere",
		Why: "the original reason"}); err != nil {
		t.Fatal(err)
	}
	g := s.globalConnect(true)
	if g == nil {
		t.Fatal("no writable spine")
	}
	defer s.closeGlobal(g)
	var id int64
	if err := g.QueryRow("SELECT id FROM axioms WHERE text=?", "a rule written elsewhere").
		Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Exec("UPDATE axioms SET origin_project='faraway', origin_session='s1' "+
		"WHERE id=?", id); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if err := s.Axiom(&out, AxiomArgs{Confirm: yes, Sub: "edit", ID: id, Why: "a reworded reason"}); err != nil {
		t.Fatal(err)
	}
	var project, why string
	if err := g.QueryRow("SELECT origin_project, rationale FROM axioms WHERE id=?", id).
		Scan(&project, &why); err != nil {
		t.Fatal(err)
	}
	if project != "faraway" {
		t.Errorf("edit overwrote the origin: got %q, want %q", project, "faraway")
	}
	if why != "a reworded reason" {
		t.Errorf("edit did not change the rationale: %q", why)
	}

	out.Reset()
	if err := s.Axiom(&out, AxiomArgs{Confirm: yes, Sub: "add", Text: "a rule written elsewhere",
		Why: "yet another reason"}); err != nil {
		t.Fatal(err)
	}
	var editor string
	if err := g.QueryRow("SELECT origin_project, edited_project FROM axioms WHERE id=?", id).
		Scan(&project, &editor); err != nil {
		t.Fatal(err)
	}
	if project != "faraway" || editor != "testproj" {
		t.Errorf("add-again: author %q (want faraway), editor %q (want testproj)", project, editor)
	}
}
