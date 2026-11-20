package brain

// Tests for the freshness verdict: how far the code has moved since a claim about it was
// stored.
//
// A memory of a volatile type (a gotcha, say "the handler drops the error at store.go:212")
// records the git commit it was true against. When it is read back, the brain counts the
// commits since then and says "tree unchanged since stored" or "N commits since stored,
// re-check". The unchanged verdict is what lets a session skip re-deriving the claim from
// source, so it must never be given when the brain cannot be sure.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newGitTestStore is newTestStore with a real git repository at the project root, because
// the whole point of the freshness verdict is what it says about an actual tree. A fake
// commit string would test the rendering and none of the resolution. It returns the store
// and the repository directory, which already has one commit.
func newGitTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "brain")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "brain.json"),
		[]byte(`{"project":"testproj"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("OS_BRAIN_GLOBAL_DB", filepath.Join(dir, "config", "brain", "brain.db"))
	t.Setenv("OS_BRAIN_DB", filepath.Join(home, "brain.db"))
	t.Setenv("OS_BRAIN_PROJECT", "testproj")

	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.email", "test@example.com")
	git(t, dir, "config", "user.name", "Test")
	commit(t, dir, "one")

	s, err := Open(NewEnv(home))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}

// git runs a git command in dir with the user's and the system's git config ignored, so a
// signing hook or a global setting on the test machine cannot change the result. It returns
// the trimmed output and fails the test if git fails.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v (%s)", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes the file name.txt and commits everything in dir with the message name.
func commit(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".txt"), []byte(name), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", name)
}

// freshnessOf returns memory id's freshness verdict as a map, or nil when it gets none.
func freshnessOf(t *testing.T, s *Store, id int64) map[string]interface{} {
	t.Helper()
	m, err := s.Readable(id, true)
	if err != nil {
		t.Fatal(err)
	}
	s.StampFreshness([]*Memory{m})
	if m.Fresh == nil {
		return nil
	}
	return map[string]interface{}{"unchanged": m.Fresh.Unchanged, "unknown": m.Fresh.Unknown,
		"commits": m.Fresh.Commits, "note": m.Fresh.Note}
}

// lastID returns the id of the most recently stored memory.
func lastID(t *testing.T, s *Store) int64 {
	t.Helper()
	var id int64
	if err := s.DB.QueryRow("SELECT MAX(id) FROM memories").Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// --- Stamping ---

// A volatile memory is stamped with HEAD at the moment it is stored.
func TestAVolatileMemoryRecordsTheCommitItWasTrueAgainst(t *testing.T) {
	s, dir := newGitTestStore(t)
	remember(t, s, RememberArgs{Type: "gotcha", Title: "handler drops the error",
		Body: "at store.go:212"})
	var stored string
	if err := s.DB.QueryRow("SELECT source_commit FROM memories WHERE id=?",
		lastID(t, s)).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if head := git(t, dir, "rev-parse", "HEAD"); stored != head {
		t.Errorf("stored %q, HEAD is %q", stored, head)
	}
}

// Only claims a commit can make false get a stamp and a verdict. A settled decision or a
// constraint is not made stale by a commit, and telling a session it is invites
// re-litigating a call the user already made.
func TestOnlyClaimsACommitCanFalsifyGetAVerdict(t *testing.T) {
	s, dir := newGitTestStore(t)
	remember(t, s, RememberArgs{Type: "decision", Title: "OSS ships before Cloud"})
	decision := lastID(t, s)
	remember(t, s, RememberArgs{Type: "constraint", Title: "no enterprise edition yet"})
	constraint := lastID(t, s)
	commit(t, dir, "two")

	for id, what := range map[int64]string{decision: "decision", constraint: "constraint"} {
		var stored string
		if err := s.DB.QueryRow("SELECT source_commit FROM memories WHERE id=?", id).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored != "" {
			t.Errorf("%s was stamped with a commit", what)
		}
		if f := freshnessOf(t, s, id); f != nil {
			t.Errorf("%s got a freshness verdict: %v", what, f)
		}
	}
}

// Without restamping, `os-brain verify` would leave a memory reading as N commits out of
// date immediately after someone checked it against HEAD.
func TestVerifyRestampsSoAConfirmedClaimReadsAsCurrent(t *testing.T) {
	s, dir := newGitTestStore(t)
	remember(t, s, RememberArgs{Type: "gotcha", Title: "handler drops the error"})
	id := lastID(t, s)
	commit(t, dir, "two")
	if f := freshnessOf(t, s, id); f["commits"] != 1 {
		t.Fatalf("expected 1 commit behind, got %v", f)
	}
	var buf bytes.Buffer
	if err := s.Verify(&buf, VerifyArgs{ID: id}); err != nil {
		t.Fatal(err)
	}
	if f := freshnessOf(t, s, id); f["unchanged"] != true {
		t.Errorf("after verify the verdict is still %v", f)
	}
}

// Rewriting the body re-asserts the claim against the current tree, so it restamps.
// Changing metadata such as importance asserts nothing about the code, so it does not.
func TestRewritingABodyRestampsButMetadataEditsDoNot(t *testing.T) {
	s, dir := newGitTestStore(t)
	remember(t, s, RememberArgs{Type: "gotcha", Title: "handler drops the error", Body: "old"})
	id := lastID(t, s)
	commit(t, dir, "two")

	imp := 5
	var buf bytes.Buffer
	if err := s.Update(&buf, UpdateArgs{ID: id, Importance: &imp}); err != nil {
		t.Fatal(err)
	}
	if f := freshnessOf(t, s, id); f["commits"] != 1 {
		t.Errorf("bumping importance re-asserted the claim: %v", f)
	}

	body := "new, checked against the current tree"
	if err := s.Update(&buf, UpdateArgs{ID: id, Body: &body}); err != nil {
		t.Fatal(err)
	}
	if f := freshnessOf(t, s, id); f["unchanged"] != true {
		t.Errorf("rewriting the body did not re-date the claim: %v", f)
	}
}

// --- The verdict ---

// An unmoved tree licenses skipping verification. This is the valuable direction: it is
// what replaces re-deriving the claim from source by hand.
func TestAnUnmovedTreeLicensesSkippingVerification(t *testing.T) {
	s, _ := newGitTestStore(t)
	remember(t, s, RememberArgs{Type: "gotcha", Title: "handler drops the error"})
	f := freshnessOf(t, s, lastID(t, s))
	if f == nil {
		t.Fatal("no verdict for a gotcha in a git tree")
	}
	if f["unchanged"] != true {
		t.Errorf("tree has not moved but the verdict is %v", f)
	}
	if !strings.Contains(f["note"].(string), "unchanged") {
		t.Errorf("note reads %q", f["note"])
	}
}

// Commits since the stamp are counted, and the note tells the session to re-check.
func TestCommitsSinceStoredAreCounted(t *testing.T) {
	s, dir := newGitTestStore(t)
	remember(t, s, RememberArgs{Type: "gotcha", Title: "handler drops the error"})
	commit(t, dir, "two")
	commit(t, dir, "three")
	f := freshnessOf(t, s, lastID(t, s))
	if f["unchanged"] == true || f["commits"] != 2 {
		t.Errorf("after two commits the verdict is %v", f)
	}
	if !strings.Contains(f["note"].(string), "re-check") {
		t.Errorf("note does not tell the session what to do: %q", f["note"])
	}
}

// A stamped commit that is not in this tree (rebased away, garbage-collected, or from a
// branch that no longer exists) gives an "unknown" verdict. Saying "unchanged" here is the
// one thing the verdict must never get wrong, because it is the answer that stops a session
// checking.
func TestACommitNotInThisTreeIsNeverReportedAsUnchanged(t *testing.T) {
	s, _ := newGitTestStore(t)
	remember(t, s, RememberArgs{Type: "gotcha", Title: "handler drops the error"})
	id := lastID(t, s)
	if _, err := s.DB.Exec("UPDATE memories SET source_commit=? WHERE id=?",
		"0123456789abcdef0123456789abcdef01234567", id); err != nil {
		t.Fatal(err)
	}
	f := freshnessOf(t, s, id)
	if f == nil || f["unchanged"] == true || f["unknown"] != true {
		t.Errorf("verdict for an unreachable commit: %v", f)
	}
	if !strings.Contains(f["note"].(string), "re-check") {
		t.Errorf("note does not tell the session to check: %q", f["note"])
	}
}

// One store can hold many projects. Another project's claims were recorded against a tree
// that is not this one, so counting commits here would be meaningless.
func TestAnotherProjectsMemoryIsNotJudgedAgainstThisTree(t *testing.T) {
	s, _ := newGitTestStore(t)
	remember(t, s, RememberArgs{Type: "gotcha", Title: "handler drops the error"})
	id := lastID(t, s)
	if _, err := s.DB.Exec("UPDATE memories SET project='elsewhere' WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	other := *s
	other.Env.Project = "elsewhere"
	if err := other.RegisterProject(); err != nil {
		t.Fatal(err)
	}
	if f := freshnessOf(t, s, id); f != nil {
		t.Errorf("judged another project's memory: %v", f)
	}
}

// The brain has to work in a directory that is not a repository at all: no stamp, no
// verdict, no error.
func TestABrainOutsideAGitTreeSaysNothing(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Type: "gotcha", Title: "handler drops the error"})
	id := lastID(t, s)
	var stored string
	if err := s.DB.QueryRow("SELECT source_commit FROM memories WHERE id=?", id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "" {
		t.Errorf("stamped %q outside a work tree", stored)
	}
	if f := freshnessOf(t, s, id); f != nil {
		t.Errorf("verdict without git: %v", f)
	}
}

// --- Where the verdict is shown ---

// recall shows the verdict to a person and carries it in the JSON envelope for tools.
func TestRecallRendersTheVerdictAndCarriesItInJSON(t *testing.T) {
	s, dir := newGitTestStore(t)
	remember(t, s, RememberArgs{Type: "gotcha", Title: "handler drops the error",
		Body: "at store.go:212"})
	commit(t, dir, "two")

	var human bytes.Buffer
	if err := s.Recall(&human, RecallArgs{Query: "handler"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(human.String(), "1 commit since stored") {
		t.Errorf("recall did not render the verdict:\n%s", human.String())
	}

	res := recall(t, s, RecallArgs{Query: "handler"})
	row := res["results"].([]interface{})[0].(map[string]interface{})
	f, ok := row["freshness"].(map[string]interface{})
	if !ok {
		t.Fatalf("no freshness in the JSON envelope: %v", row)
	}
	if f["commits"].(float64) != 1 || f["unchanged"] != false {
		t.Errorf("json verdict: %v", f)
	}
}

// get shows the verdict on its own "tree" line.
func TestGetRendersTheVerdict(t *testing.T) {
	s, _ := newGitTestStore(t)
	remember(t, s, RememberArgs{Type: "gotcha", Title: "handler drops the error"})
	var buf bytes.Buffer
	if err := s.Get(&buf, lastID(t, s), false, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "tree   tree unchanged since stored") {
		t.Errorf("get did not render the verdict:\n%s", buf.String())
	}
}

// A pinned gotcha is read by every session, so a stale one is the most dangerous kind; the
// brief flags it.
func TestBriefFlagsAStalePinnedGotcha(t *testing.T) {
	s, dir := newGitTestStore(t)
	remember(t, s, RememberArgs{Type: "gotcha", Title: "handler drops the error", Pin: true})
	commit(t, dir, "two")
	var buf bytes.Buffer
	if err := s.Brief(&buf, BriefArgs{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "1 commit since") {
		t.Errorf("brief did not flag the stale pinned gotcha:\n%s", buf.String())
	}
}

// Six results must not mean six git subprocesses. Memories written at the same commit share
// one answer, and the common case (everything at HEAD) costs none at all.
func TestOneGitCallPerDistinctCommitNotPerMemory(t *testing.T) {
	s, dir := newGitTestStore(t)
	for _, title := range []string{"alpha handler", "beta handler", "gamma handler"} {
		remember(t, s, RememberArgs{Type: "gotcha", Title: title})
	}
	commit(t, dir, "two")
	tree := s.TreeState()
	ms, err := s.queryMemories("SELECT " + memoryCols + ", 0.0 AS bm FROM memories m")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		tree.For(m, s.Env.Project)
	}
	if len(tree.dist) != 1 {
		t.Errorf("memoised %d distances for one distinct commit", len(tree.dist))
	}
}
