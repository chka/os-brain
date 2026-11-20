package brain

// Tests for safety copies: the automatic whole-store copies that sit beside the shared store,
// and `safety restore`, which puts one back. A backup is only a backup if it can be restored,
// so most of these empty a store and bring it back for real.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedTitles must be genuinely different from each other: `remember` refuses a second memory
// about the same subject in one scope, so a fixture of "fact 1", "fact 2" would be refused by
// the near-duplicate guard rather than stored.
var seedTitles = []string{
	"nginx terminates TLS on port 8443 in front of the app",
	"postgres runs on the same box, loopback only, no published port",
	"the deploy user is called ship and owns /srv",
	"redis needs requirepass set or the botnet finds it",
	"certificates renew from cron at 03:20 every Monday",
	"the staging box has half the memory of production",
	"backups land on a second provider, not the same one",
	"the queue drains slowly whenever the embedder is down",
	"log rotation keeps fourteen days and then compresses",
	"the health endpoint answers ok even when the disk is full",
	"DNS is delegated, so a record change takes an hour to matter",
	"the firewall drops everything except 22, 80 and 443",
	"docker writes its own iptables and bypasses the firewall",
	"secrets come from the vault at boot and never touch the image",
}

// seedFor stores the first n seed titles as global notes.
func seedFor(t *testing.T, s *Store, n int) {
	t.Helper()
	if n > len(seedTitles) {
		t.Fatalf("the fixture has %d distinct titles and the test wants %d", len(seedTitles), n)
	}
	for i := 0; i < n; i++ {
		if err := s.Remember(io.Discard, RememberArgs{
			Scope: "global", Type: "note", Title: seedTitles[i],
			Body: "a body long enough to be a real memory rather than a stub",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// liveCount counts every row in the memories table.
func liveCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM memories").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The drill that makes a backup a backup: empty the store and put it back. A restore path
// that has only ever been dry-run is a restore path nobody has tested, and the day it is
// first used is the worst possible day to find out it does not work.
func TestASafetyCopyCanActuallyPutTheBrainBack(t *testing.T) {
	s := newTestStore(t)
	seedFor(t, s, 6)
	before := liveCount(t, s)

	copy1, err := s.MakeSafetyCopy()
	if err != nil {
		t.Fatal(err)
	}
	if !copy1.OK {
		t.Fatalf("the copy did not verify: %s", copy1.Problem)
	}
	if copy1.Memories != before {
		t.Fatalf("copy holds %d memories, the store had %d", copy1.Memories, before)
	}

	if _, err := s.DB.Exec("DELETE FROM memories"); err != nil {
		t.Fatal(err)
	}
	if liveCount(t, s) != 0 {
		t.Fatal("the store was not emptied, so the test proves nothing")
	}

	var out strings.Builder
	if err := s.SafetyRestore(&out, copy1.Path, false, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "dry run") {
		t.Errorf("restore without --apply did not say it was a dry run: %s", out.String())
	}
	if liveCount(t, s) != 0 {
		t.Fatal("a dry run wrote to the store")
	}

	out.Reset()
	if err := s.SafetyRestore(&out, copy1.Path, true, false); err != nil {
		t.Fatal(err)
	}
	// The restore closes the store handle because it replaces the file underneath it.
	// Reopen the way the next session would.
	s2, err := Open(s.Env)
	if err != nil {
		t.Fatalf("the store does not open after a restore: %v", err)
	}
	defer s2.Close()
	if got := liveCount(t, s2); got != before {
		t.Errorf("restored %d memories, expected %d", got, before)
	}
	if !strings.Contains(out.String(), "copied first") {
		t.Error("the restore did not copy the CURRENT store before replacing it")
	}
}

// A restore must never be the moment data is lost, so what it replaces is copied first. That
// copy is the only route back if the file being restored turns out to be the wrong one.
func TestRestoreCopiesWhatItIsAboutToReplace(t *testing.T) {
	s := newTestStore(t)
	seedFor(t, s, 3)
	early, err := s.MakeSafetyCopy()
	if err != nil {
		t.Fatal(err)
	}
	seedFor(t, s, 4) // the store moves on past the copy
	current := liveCount(t, s)

	if err := s.SafetyRestore(io.Discard, early.Path, true, false); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(s.Env)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	copies, err := s2.listCopies()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, c := range copies {
		if c.Memories == current {
			found = true
		}
	}
	if !found {
		t.Errorf("nothing holds the %d memories that were live before the restore; "+
			"the pre-restore copy is the only way back from restoring the wrong file", current)
	}
}

// A copy that does not verify must not be allowed near the live store. Failing open here
// would make the one command whose job is recovery a way to install damage.
func TestRestoreRefusesACopyThatDoesNotVerify(t *testing.T) {
	s := newTestStore(t)
	seedFor(t, s, 2)
	junk := filepath.Join(t.TempDir(), "not-a-database.db")
	if err := os.WriteFile(junk, []byte("this is not a sqlite file"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := s.SafetyRestore(io.Discard, junk, true, false)
	if err == nil {
		t.Fatal("restore accepted a file that is not a database")
	}
	if !strings.Contains(err.Error(), "does not verify") {
		t.Errorf("the refusal does not say why: %v", err)
	}
	if liveCount(t, s) != 2 {
		t.Error("the refused restore still touched the store")
	}
}

// Rotation is the part of this feature most able to cause the loss it exists to prevent: a
// store is emptied by accident, ordinary sessions follow, and a purely time-based rotation
// faithfully replaces every good copy with a copy of the damage. The copy holding the most
// memories is therefore never rotated out.
func TestRotationNeverDiscardsTheCopyHoldingTheMost(t *testing.T) {
	s := newTestStore(t)
	seedFor(t, s, 9)
	rich, err := s.MakeSafetyCopy()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.DB.Exec("DELETE FROM memories"); err != nil {
		t.Fatal(err)
	}
	// Enough empty copies to push the rich one out of a purely time-based window.
	copies := []SafetyCopy{}
	for i := 0; i < SafetyKeep+3; i++ {
		copies = append(copies, SafetyCopy{
			Path: rich.Path + fmt.Sprint(i), Memories: 0, OK: true, Taken: rich.Taken + int64(i) + 1,
		})
	}
	// Newest first, exactly as listCopies returns them, with the rich copy oldest.
	copies = append(copies, SafetyCopy{
		Path: rich.Path, Memories: rich.Memories, OK: true, Taken: rich.Taken,
	})
	for i := range copies {
		if copies[i].Path == rich.Path {
			continue
		}
		if err := os.WriteFile(copies[i].Path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	s.rotate(copies)
	if _, err := os.Stat(rich.Path); err != nil {
		t.Fatalf("rotation deleted the copy holding the most memories: %v", err)
	}
}

// The store getting smaller than its best copy is reported and never acted on: `forget --all`
// is a command people run on purpose, so shrinking is not proof of an accident.
func TestTheBriefSaysWhenTheStoreIsSmallerThanItsBestCopy(t *testing.T) {
	s := newTestStore(t)
	seedFor(t, s, 10)
	if _, err := s.MakeSafetyCopy(); err != nil {
		t.Fatal(err)
	}
	if _, best := s.SafetyShrink(); best != nil {
		t.Fatal("a store equal to its copy was reported as shrunken")
	}

	if _, err := s.DB.Exec("DELETE FROM memories WHERE id > 2"); err != nil {
		t.Fatal(err)
	}
	live, best := s.SafetyShrink()
	if best == nil {
		t.Fatal("the store lost most of its memories and nothing said so")
	}
	if live >= best.Memories {
		t.Errorf("live %d is not below the best copy %d", live, best.Memories)
	}
	if liveCount(t, s) != 2 {
		t.Error("SafetyShrink changed the store; it is a report, not an action")
	}
}

// --auto is what a hook runs, so it must be silent when there is nothing to do. A line on
// every session start gets ignored, and takes the line that mattered with it.
func TestAutoIsSilentWhenNothingIsDue(t *testing.T) {
	s := newTestStore(t)
	seedFor(t, s, 2)

	var first strings.Builder
	if err := s.SafetyAuto(&first); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.String(), "safety copy") {
		t.Errorf("the first run made no copy: %q", first.String())
	}

	var second strings.Builder
	if err := s.SafetyAuto(&second); err != nil {
		t.Fatal(err)
	}
	if second.String() != "" {
		t.Errorf("a second run with nothing to do printed %q", second.String())
	}
}

// Copies land outside every project, because a copy in the working tree is one `git add -A`
// away from being published, and copies of a brain holding private notes have been found
// sitting untracked in a working tree. Each copy holds every memory in cleartext, so it is
// owner-only too.
func TestCopiesAreNeverWrittenInsideTheProject(t *testing.T) {
	s := newTestStore(t)
	seedFor(t, s, 2)
	c, err := s.MakeSafetyCopy()
	if err != nil {
		t.Fatal(err)
	}
	// The invariant is that copies live beside the SHARED store rather than in the brain
	// directory, which in normal use is outside every project. It cannot be stated as
	// "outside the project root" here, because the hermetic test harness puts its config dir
	// inside the same temp root it uses as the project.
	if rel, err := filepath.Rel(s.Env.Here, c.Path); err == nil && !strings.HasPrefix(rel, "..") {
		t.Errorf("a safety copy was written inside the brain directory at %s, where a "+
			"`git add -A` would find it", c.Path)
	}
	if want := filepath.Dir(s.Env.GlobalDB); !strings.HasPrefix(c.Path, want) {
		t.Errorf("copies land in %s, not beside the shared store at %s", c.Path, want)
	}
	info, err := os.Stat(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("a safety copy is mode %o, and it holds every memory in cleartext",
			info.Mode().Perm())
	}
}
