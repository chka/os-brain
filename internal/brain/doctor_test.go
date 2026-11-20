package brain

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

// doctor is the backstop for damage no command should cause: a stale search index,
// supersession cycles, identity hashes that are not the row's own. It must see each fault,
// and --fix must repair only this project's rows, after a backup.

// The search index is written in code, not by a trigger, so a write path that forgets it
// leaves memories that exist but can never be recalled.
func TestEveryWriteKeepsTheIndexInStep(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "indexed fact", Body: "findable"})
	body := "rewritten body about lighthouses"
	if err := s.Update(io.Discard, UpdateArgs{ID: 1, Body: &body}); err != nil {
		t.Fatal(err)
	}
	if got := titles(recall(t, s, RecallArgs{Query: "lighthouses"})); len(got) != 1 {
		t.Errorf("an updated body must be searchable, got %v", got)
	}
	var buf bytes.Buffer
	if err := s.Doctor(&buf, DoctorArgs{JSON: true}); err != nil {
		t.Fatal(err)
	}
	var d map[string]interface{}
	json.Unmarshal(buf.Bytes(), &d)
	// Health is an empty problems list, not a boolean: that is the envelope both editions
	// print and consumers parse.
	if probs, _ := d["problems"].([]interface{}); len(probs) != 0 {
		t.Errorf("doctor found problems: %s", buf.String())
	}
}

// A raw SQL insert is exactly what leaves the index stale. doctor must notice and --fix
// must make the row findable.
func TestDoctorFixRepairsAStaleIndex(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "findable", Type: "note"})
	if _, err := s.DB.Exec("INSERT INTO memories(project,scope,mem_type,title,body,hash," +
		"created_at,updated_at) VALUES('testproj','global','note','invisible','','rawhash',1,1)"); err != nil {
		t.Fatal(err)
	}
	res := mustJSON(t, func(w io.Writer) error { return s.Doctor(w, DoctorArgs{JSON: true}) })
	if probs, _ := res["problems"].([]interface{}); len(probs) == 0 {
		t.Fatal("doctor did not notice the stale index")
	}
	if err := s.Doctor(io.Discard, DoctorArgs{Fix: true}); err != nil {
		t.Fatal(err)
	}
	res = mustJSON(t, func(w io.Writer) error { return s.Doctor(w, DoctorArgs{JSON: true}) })
	if probs, _ := res["problems"].([]interface{}); len(probs) != 0 {
		t.Errorf("--fix left problems: %v", probs)
	}
	if got := titles(recall(t, s, RecallArgs{Query: "invisible"})); len(got) != 1 {
		t.Errorf("the repaired row is still unfindable: %v", got)
	}
}

// doctor's two index faults must not cancel out. A memory missing from the index plus an
// orphan index row give equal counts, and a check that compared only the two counts
// reported clean while a memory nobody could find sat in the store.
func TestDoctorSeesAMissingIndexRowEvenWithAnOrphanPresent(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "the invisible one", Body: "x", Type: "note"})
	remember(t, s, RememberArgs{Title: "the visible one", Body: "y", Type: "note"})
	if _, err := s.DB.Exec("DELETE FROM mem_fts WHERE rowid=1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO mem_fts(rowid, title, body) VALUES(9999, 'ghost', '')"); err != nil {
		t.Fatal(err)
	}
	var nmem, nfts int
	_ = s.DB.QueryRow("SELECT COUNT(*) FROM memories").Scan(&nmem)
	_ = s.DB.QueryRow("SELECT COUNT(*) FROM mem_fts").Scan(&nfts)
	if nmem != nfts {
		t.Fatalf("the fixture did not reproduce the cancelling case: %d memories, %d index rows",
			nmem, nfts)
	}
	var buf bytes.Buffer
	if err := s.Doctor(&buf, DoctorArgs{}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "not in the search index") {
		t.Errorf("doctor did not report a memory missing from the index:\n%s", out)
	}
	if !strings.Contains(out, "point at deleted memories") {
		t.Errorf("doctor did not report the orphan row:\n%s", out)
	}
}

// --fix used to null every link on every path into a supersession cycle, in every project,
// which made every retracted fact on the way into the cycle live again. It now breaks one
// edge per cycle, only in this project, and takes a backup first.
func TestDoctorFixBreaksOneEdgePerCycleInThisProjectAfterABackup(t *testing.T) {
	s := newTestStore(t)
	for _, title := range []string{"one fact here", "two fact here", "three fact here", "four fact here"} {
		remember(t, s, RememberArgs{Title: title})
	}
	a, b, c := idOf(t, s, "one fact here"), idOf(t, s, "two fact here"), idOf(t, s, "three fact here")
	// a -> b -> c -> b: a leads into the cycle b <-> c.
	s.DB.Exec("UPDATE memories SET superseded_by=? WHERE id=?", b, a)
	s.DB.Exec("UPDATE memories SET superseded_by=? WHERE id=?", c, b)
	s.DB.Exec("UPDATE memories SET superseded_by=? WHERE id=?", b, c)
	// Another project's cycle in the same file, which --fix must leave alone.
	for i, title := range []string{"theirs one", "theirs two"} {
		s.DB.Exec("INSERT INTO memories(project,scope,mem_type,title,body,hash,created_at,updated_at) "+
			"VALUES('other','global','note',?,'',?,1,1)", title, fmt.Sprintf("h%d", i))
	}
	x, y := idOf(t, s, "theirs one"), idOf(t, s, "theirs two")
	s.DB.Exec("UPDATE memories SET superseded_by=? WHERE id=?", y, x)
	s.DB.Exec("UPDATE memories SET superseded_by=? WHERE id=?", x, y)

	var out bytes.Buffer
	if err := s.Doctor(&out, DoctorArgs{Fix: true}); err != nil {
		t.Fatal(err)
	}
	link := func(id int64) bool {
		var sup sql.NullInt64
		s.DB.QueryRow("SELECT superseded_by FROM memories WHERE id=?", id).Scan(&sup)
		return sup.Valid
	}
	if !link(a) {
		t.Error("the chain into the cycle was cut: a retracted fact is live again")
	}
	if link(b) == link(c) {
		t.Errorf("expected exactly one of #%d, #%d unlinked (b=%v c=%v)", b, c, link(b), link(c))
	}
	if !link(x) || !link(y) {
		t.Error("--fix rewrote another project's rows")
	}
	if !strings.Contains(out.String(), "backed up") {
		t.Errorf("no backup was taken first:\n%s", out.String())
	}
}

// The identity hash is what duplicate checks compare, so a hash computed under another
// project name (left behind by a relabel, for example) makes the row a stranger to its own
// title. doctor reports it and --fix restores the row's own identity.
func TestDoctorFindsAnIdentityHashThatIsNotTheRowsOwn(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "a fact somebody relabelled once"})
	id := idOf(t, s, "a fact somebody relabelled once")
	s.DB.Exec("UPDATE memories SET hash=? WHERE id=?", ContentHash("oldname", "global", "a fact somebody relabelled once"), id)
	var out bytes.Buffer
	if err := s.Doctor(&out, DoctorArgs{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "identity hash") {
		t.Fatalf("doctor did not see the stale hash:\n%s", out.String())
	}
	if err := s.Doctor(io.Discard, DoctorArgs{Fix: true}); err != nil {
		t.Fatal(err)
	}
	var h string
	s.DB.QueryRow("SELECT hash FROM memories WHERE id=?", id).Scan(&h)
	if h != ContentHash("testproj", "global", "a fact somebody relabelled once") {
		t.Error("--fix did not restore the row's own identity")
	}
}
