package store_test

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/fsguard"
	"github.com/chka/os-brain/internal/memory"
	"github.com/chka/os-brain/internal/recall"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/testutil"
	"github.com/chka/os-brain/internal/timefmt"
)

// Tests for project identity on a shared store: how a project's name is bound to its
// directory.
//
// Several projects can share one database. A project's memories are rows labelled with its
// name, so the name is what stands between one project and another's memories. A name is
// cheap to claim: a clone carries a committed brain.json, and an environment variable can
// say anything. So a name is bound to the directory that first used it. Each test below is
// one way a project once claimed, or could have claimed, another's name. The tests of what
// each command may then read, write, count and back up are in internal/integration.

// The basic attack: a second directory, such as a clone carrying a committed brain.json,
// declares an existing project's name. It is refused whether the name comes from the file
// or the environment, and whether or not it also claims the victim's directory as its root.
// The refusal names the way out for a project that really moved.
func TestASecondDirectoryClaimingAProjectsNameIsRefused(t *testing.T) {
	base := testutil.SharedLab(t)
	victim := testutil.MustOpenAt(t, filepath.Join(base, "victim"), `{"project":"victim","shared":true}`)
	remember(t, victim, memory.RememberArgs{Title: "victim prod database password rotation", Body: "SECRET-VICTIM-BODY"})

	_, err := testutil.OpenAt(t, filepath.Join(base, "clone"), `{"project":"victim","shared":true}`)
	if err == nil || !strings.Contains(err.Error(), "another project in the shared store") {
		t.Fatalf("a clone declaring victim's name opened its memories: %v", err)
	}
	if !strings.Contains(err.Error(), "relabel --claim") {
		t.Errorf("the refusal does not name the way out: %v", err)
	}

	// Through the environment instead of the file.
	t.Setenv("OS_BRAIN_PROJECT", "victim")
	if _, err := testutil.OpenAt(t, filepath.Join(base, "envclone"), `{"project":"evil","shared":true}`); err == nil {
		t.Fatal("OS_BRAIN_PROJECT=victim from another directory opened victim's memories")
	}
	t.Setenv("OS_BRAIN_PROJECT", "")

	// brain.json's root set to the victim's real directory borrows nothing.
	cfg, _ := json.Marshal(map[string]interface{}{"project": "victim", "shared": true,
		"root": filepath.Join(base, "victim")})
	if _, err := testutil.OpenAt(t, filepath.Join(base, "rootclone"), string(cfg)); err == nil {
		t.Fatal("a brain.json root pointing at the victim borrowed its identity")
	}
	// OS_BRAIN_PROJECT_ROOT is the same claim from the environment.
	t.Setenv("OS_BRAIN_PROJECT_ROOT", filepath.Join(base, "victim"))
	if _, err := testutil.OpenAt(t, filepath.Join(base, "envroot"), `{"project":"victim","shared":true}`); err == nil {
		t.Fatal("OS_BRAIN_PROJECT_ROOT pointing at the victim borrowed its identity")
	}
	t.Setenv("OS_BRAIN_PROJECT_ROOT", "")

	// The real directory still opens, and still holds its memory.
	again := testutil.MustOpenAt(t, filepath.Join(base, "victim"), `{"project":"victim","shared":true}`)
	if n := len(recallJSON(t, again, recall.RecallArgs{Query: "rotation"})["results"].([]interface{})); n != 1 {
		t.Errorf("the owner lost its memory: %d results", n)
	}
}

// A name with no binding yet, which the project index shows living in another directory
// (a store written before bindings existed), is not handed to the first new directory that
// asks for it. A name nothing knows about is.
func TestAnUnboundNameTheIndexPlacesElsewhereIsRefused(t *testing.T) {
	base := testutil.SharedLab(t)
	first := testutil.MustOpenAt(t, filepath.Join(base, "first"), `{"project":"first","shared":true}`)
	if err := first.RegisterProject(); err != nil {
		t.Fatal(err)
	}
	if _, err := first.DB.Exec("INSERT INTO projects(root_id,name,last_seen) VALUES(?,?,?)",
		store.HashRoot(filepath.Join(base, "old")), "legacy", timefmt.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := testutil.OpenAt(t, filepath.Join(base, "new"), `{"project":"legacy","shared":true}`); err == nil {
		t.Fatal("a name the index places in another directory was bound to a new one")
	}
	// Nothing anywhere says otherwise: the first directory gets it.
	if _, err := testutil.OpenAt(t, filepath.Join(base, "fresh"), `{"project":"fresh","shared":true}`); err != nil {
		t.Fatalf("a brand-new name was refused: %v", err)
	}
}

// A project that really moved needs its name back. relabel --claim moves the binding, but
// only when a person confirms it: with no confirmer, or a declined one, it fails. After the
// claim the new directory opens and the old one no longer does.
func TestClaimMovesTheBindingOnlyWithAPerson(t *testing.T) {
	base := testutil.SharedLab(t)
	old := testutil.MustOpenAt(t, filepath.Join(base, "old"), `{"project":"mover","shared":true}`)
	remember(t, old, memory.RememberArgs{Title: "mover keeps this fact about kestrels", Body: "x"})
	old.Close()

	moved := filepath.Join(base, "moved")
	if _, err := testutil.OpenAt(t, moved, `{"project":"mover","shared":true}`); err == nil {
		t.Fatal("the moved copy opened without a claim")
	}
	env := store.NewEnv(filepath.Join(moved, "brain"))
	env.Claim = true
	s, err := store.Open(env)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Claim(io.Discard, nil, false); err == nil {
		t.Fatal("a claim with no person confirming it went through")
	}
	no := func(string) (bool, error) { return false, nil }
	if err := s.Claim(io.Discard, no, false); err == nil {
		t.Fatal("a declined claim reported success")
	}
	yes := func(string) (bool, error) { return true, nil }
	if err := s.Claim(io.Discard, yes, false); err != nil {
		t.Fatal(err)
	}
	if _, err := testutil.OpenAt(t, moved, `{"project":"mover","shared":true}`); err != nil {
		t.Fatalf("the claimed directory does not open: %v", err)
	}
	if _, err := testutil.OpenAt(t, filepath.Join(base, "old"), `{"project":"mover","shared":true}`); err == nil {
		t.Fatal("the old directory still opens after the claim")
	}
}

// A root declared in brain.json is believed only inside the brain's own repository, or,
// outside any repository, when that root's own hook names this brain. Otherwise a committed
// brain.json could declare any directory on the machine as its identity.
func TestADeclaredRootOutsideTheRepositoryIsIgnored(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	here := filepath.Join(repo, "marketing", "brain")
	if err := os.MkdirAll(here, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OS_BRAIN_PROJECT_ROOT", "")
	for root, want := range map[string]string{
		repo:                         repo,
		base:                         filepath.Join(repo, "marketing"),
		"~":                          filepath.Join(repo, "marketing"),
		filepath.Join(base, "other"): filepath.Join(repo, "marketing"),
	} {
		e := store.Env{Here: here, Config: store.Config{Root: root}}
		if got := e.ProjectRoot(); fsguard.Realpath(got) != fsguard.Realpath(want) {
			t.Errorf("root %q: ProjectRoot is %s, want %s", root, got, want)
		}
	}
	// Outside a repository only the brain's parent, or a root whose own hook names it.
	plain := filepath.Join(base, "plain", "sub", "brain")
	if err := os.MkdirAll(plain, 0o700); err != nil {
		t.Fatal(err)
	}
	e := store.Env{Here: plain, Config: store.Config{Root: filepath.Join(base, "plain")}}
	if got := e.ProjectRoot(); fsguard.Realpath(got) != fsguard.Realpath(filepath.Join(base, "plain", "sub")) {
		t.Errorf("an unhooked root outside a repository was believed: %s", got)
	}
	hook := `{"hooks":{"SessionStart":[{"hooks":[{"command":"OS_BRAIN_HOME=\"$CLAUDE_PROJECT_DIR/sub/brain\" os-brain wakeup"}]}]}}`
	if err := os.MkdirAll(filepath.Join(base, "plain", ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "plain", ".claude", "settings.json"), []byte(hook), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := e.ProjectRoot(); fsguard.Realpath(got) != fsguard.Realpath(filepath.Join(base, "plain")) {
		t.Errorf("a root whose hook names this brain was not believed: %s", got)
	}
}

// A cloned repository's committed hook could point OS_BRAIN_HOME or OS_BRAIN_DB at a brain
// outside the repository, in a sibling project. Discovery does not follow it and the hook
// check refuses it, while the project's own brain is still accepted.
func TestAHookPointingOutsideTheRepositoryIsRefused(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "evil")
	sib := filepath.Join(base, "loc", "brain")
	for _, d := range []string{filepath.Join(repo, ".claude"), sib} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sib, "brain.json"), []byte(`{"project":"loc"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	hook := `{"hooks":{"SessionStart":[{"hooks":[{"command":"OS_BRAIN_HOME=\"$CLAUDE_PROJECT_DIR/../loc/brain\" os-brain wakeup"}]}]}}`
	if err := os.WriteFile(filepath.Join(repo, ".claude", "settings.json"), []byte(hook), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := store.DiscoverHome(repo); ok {
		t.Errorf("discovery followed a hook out of the repository to %s", got)
	}
	t.Setenv("OS_BRAIN_DB", "")
	t.Setenv("CLAUDE_PROJECT_DIR", repo)
	if err := store.CheckHookHome(sib); err == nil {
		t.Error("a hook's OS_BRAIN_HOME outside its project was accepted")
	}
	if err := store.CheckHookHome(filepath.Join(repo, "brain")); err != nil {
		t.Errorf("the project's own brain was refused: %v", err)
	}
	t.Setenv("OS_BRAIN_DB", filepath.Join(sib, "brain.db"))
	if err := store.CheckHookHome(filepath.Join(repo, "brain")); err == nil {
		t.Error("a hook's OS_BRAIN_DB outside its project was accepted")
	}
}

// Approving a probe lets it run commands. The approval belongs to the directory, not the
// name, or any directory naming the project would inherit it. An approval recorded by name
// before this change still counts in its own directory.
func TestProbeApprovalIsKeyedToTheDirectory(t *testing.T) {
	s := newTestStore(t)
	if !strings.Contains(s.TrustKey(), store.HashRoot(s.Env.ProjectRoot())) {
		t.Fatalf("the trust key carries no directory: %s", s.TrustKey())
	}
	if _, err := s.DB.Exec("INSERT INTO meta(k,v) VALUES(?,?)", s.LegacyTrustKey(), "abc"); err != nil {
		t.Fatal(err)
	}
	if !s.Trusted()["abc"] {
		t.Error("an approval recorded by name before the upgrade was lost in its own directory")
	}
	named := testutil.Clone(s)
	named.Env.Named = true
	if named.Trusted()["abc"] {
		t.Error("a caller naming the project inherited its approvals")
	}
	moved := testutil.Clone(s)
	moved.Env.Here = filepath.Join(t.TempDir(), "brain")
	if err := moved.SetTrusted([]string{"def"}); err != nil {
		t.Fatal(err)
	}
	if s.Trusted()["def"] {
		t.Error("an approval from another directory applies here")
	}
}
