package memory

// Tests for `forget --all` and `forget --everything`, the commands that empty a project.
// They are the widest deletes in the tool, so every guard here is about never doing the wide
// thing by accident: a dry run by default, two flags to apply, a dump first, and nothing
// outside this project touched.

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// wipeFixture is a store with three memories (one pinned and one a decision, so the
// load-bearing kinds are present) and one event.
func wipeFixture(t *testing.T) *Store {
	t.Helper()
	s := newTestStore(t)
	for _, m := range []RememberArgs{
		{Title: "ordinary fact", Body: "a", Type: "knowledge"},
		{Title: "load bearing host", Body: "b", Type: "infra", Pin: true},
		{Title: "a decision that was made", Body: "c", Type: "decision"},
	} {
		m.Scope = "global"
		m.Source = "stated by the user"
		if err := s.Remember(&bytes.Buffer{}, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Log(&bytes.Buffer{}, LogArgs{Scope: "global", Kind: "note",
		Summary: "something happened"}); err != nil {
		t.Fatal(err)
	}
	return s
}

// countIn counts this project's rows in one table of the project database.
func countIn(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE project=?",
		s.Env.Project).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Without --apply a wipe only reports what it would delete, and says that it is a dry run.
func TestForgetAllReportsAndChangesNothingWithoutApply(t *testing.T) {
	s := wipeFixture(t)
	var out bytes.Buffer
	if err := s.Forget(&out, ForgetArgs{All: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "DRY RUN") {
		t.Errorf("a wipe must announce itself as a dry run:\n%s", out.String())
	}
	if got := countIn(t, s, "memories"); got != 3 {
		t.Errorf("the dry run deleted %d memories", 3-got)
	}
}

// The widest possible delete is never one flag away, and the refusal names the second flag.
func TestForgetAllRefusesApplyWithoutIMeanIt(t *testing.T) {
	s := wipeFixture(t)
	err := s.Forget(&bytes.Buffer{}, ForgetArgs{All: true, Apply: true})
	if err == nil {
		t.Fatal("--apply alone wiped the brain")
	}
	if !strings.Contains(err.Error(), "--i-mean-it") {
		t.Errorf("the refusal must name the way through: %v", err)
	}
	if got := countIn(t, s, "memories"); got != 3 {
		t.Errorf("%d memories survived a refused wipe", got)
	}
}

// prune refuses tiers A and B and skips pinned rows, which is right for decay and wrong for
// "empty this project": the load-bearing memories are exactly the ones a wipe is asked to
// remove. So --all takes every memory, leaves a tombstone for each, clears the search index,
// keeps the activity log, and first writes a dump that only the owner can read.
func TestForgetAllTakesEverythingIncludingPinnedAndDumpsFirst(t *testing.T) {
	s := wipeFixture(t)
	var out bytes.Buffer
	if err := s.Forget(&out, ForgetArgs{All: true, Apply: true, IMeanIt: true,
		Reason: "starting over"}); err != nil {
		t.Fatal(err)
	}
	if got := countIn(t, s, "memories"); got != 0 {
		t.Errorf("%d memories left after a wipe", got)
	}
	if got := countIn(t, s, "events"); got != 1 {
		t.Errorf("--all took the activity log with it: %d events left", got)
	}
	if got := countIn(t, s, "tombstones"); got != 3 {
		t.Errorf("expected a tombstone per memory, got %d", got)
	}
	var fts int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM mem_fts").Scan(&fts); err != nil {
		t.Fatal(err)
	}
	if fts != 0 {
		t.Errorf("%d rows left in the search index, matching memories that are gone", fts)
	}
	var dump string
	for _, l := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(l, "dump: ") {
			dump = strings.TrimPrefix(l, "dump: ")
		}
	}
	if dump == "" {
		t.Fatal("the wipe did not say where the dump went")
	}
	body, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("no dump was written: %v", err)
	}
	if !strings.Contains(string(body), "load bearing host") {
		t.Error("the dump is missing the pinned memory it was taken to protect")
	}
	if fi, err := os.Stat(dump); err == nil && fi.Mode().Perm() != 0o600 {
		t.Errorf("dump is mode %o; every memory is in it in cleartext", fi.Mode().Perm())
	}
}

// --everything goes further than --all: the activity log, the tombstones and the archived
// versions go too.
func TestForgetEverythingClearsTheRestOfTheProjectToo(t *testing.T) {
	s := wipeFixture(t)
	if err := s.Forget(&bytes.Buffer{}, ForgetArgs{Everything: true, Apply: true,
		IMeanIt: true}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"memories", "events", "tombstones", "mem_history"} {
		if got := countIn(t, s, table); got != 0 {
			t.Errorf("%s still holds %d rows after --everything", table, got)
		}
	}
}

// A wipe deletes rows rather than the file because on the shared store the file belongs to
// every project. The neighbour's memory, and its row in the shared search index, survive.
func TestAWipeLeavesOtherProjectsAlone(t *testing.T) {
	s := wipeFixture(t)
	other := clone(s)
	other.Env.Project = "neighbour"
	if err := other.Remember(&bytes.Buffer{}, RememberArgs{Scope: "global",
		Type: "knowledge", Title: "not yours to delete", Body: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Forget(&bytes.Buffer{}, ForgetArgs{Everything: true, Apply: true,
		IMeanIt: true}); err != nil {
		t.Fatal(err)
	}
	if got := countIn(t, &other, "memories"); got != 1 {
		t.Errorf("the neighbouring project has %d memories, expected 1", got)
	}
	var fts int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM mem_fts").Scan(&fts); err != nil {
		t.Fatal(err)
	}
	if fts != 1 {
		t.Errorf("the shared search index holds %d rows, expected the neighbour's 1", fts)
	}
}

// "forget --all 42" reads as "forget 42", and doing the widest thing on that is the failure
// this command is guarded against. The CLI refuses the combination (the forget command in
// package main). This test records the store's side: under --all it ignores the id entirely
// rather than half-honouring it.
func TestForgetAllIgnoresAnIdRatherThanHalfHonouringIt(t *testing.T) {
	s := wipeFixture(t)
	if err := s.Forget(&bytes.Buffer{}, ForgetArgs{All: true, ID: 1, Apply: true,
		IMeanIt: true}); err != nil {
		t.Fatal(err)
	}
	if got := countIn(t, s, "memories"); got != 0 {
		t.Errorf("%d left", got)
	}
}
