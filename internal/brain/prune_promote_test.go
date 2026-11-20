package brain

import (
	"bytes"
	"database/sql"
	"io"
	"strings"
	"testing"
)

// prune and promote are bulk and structural edits. Neither may undo a correction or
// overwrite an existing fact.

// prune once deleted the live head of a supersession chain because it was old and low-tier,
// which brought the fact it retracted back to life. A memory that retracts another is kept,
// and prune says what it kept.
func TestPruneKeepsTheMemoryThatRetractsAnother(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Type: "decision", Scope: "ops", Title: "we route prod traffic through the old gateway box"})
	old := idOf(t, s, "we route prod traffic through the old gateway box")
	remember(t, s, RememberArgs{Type: "state", Scope: "ops", Title: "prod traffic goes through the new gateway now",
		Supersedes: old})
	head := idOf(t, s, "prod traffic goes through the new gateway now")
	s.DB.Exec("UPDATE memories SET updated_at=1 WHERE id=?", head)
	var out bytes.Buffer
	if err := s.Prune(&out, PruneArgs{Tier: "D", OlderThan: 30, Scope: "ops", Apply: true}); err != nil {
		t.Fatal(err)
	}
	var sup sql.NullInt64
	if err := s.DB.QueryRow("SELECT superseded_by FROM memories WHERE id=?", old).Scan(&sup); err != nil || !sup.Valid {
		t.Errorf("the retracted decision is live again (%v):\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "kept 1") {
		t.Errorf("prune did not say what it kept:\n%s", out.String())
	}
}

// Promoting a memory to a wider scope where the same title already exists would put two
// versions of one fact side by side.
func TestPromoteRefusesToCollideWithAnExistingTitle(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "nginx on 8443", Type: "infra", Scope: "channel:a"})
	remember(t, s, RememberArgs{Title: "nginx on 8443", Type: "infra", Scope: "global"})
	if err := s.Promote(io.Discard, PromoteArgs{ID: 1, To: "global"}); err == nil {
		t.Error("promoted a memory on top of an existing title")
	}
}
