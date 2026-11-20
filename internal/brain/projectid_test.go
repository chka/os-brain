package brain

// Tests for project identity and isolation on a shared store.
//
// Several projects can share one database. A project's memories are rows labelled with its
// name, so the name is what stands between one project and another's memories. A name is
// cheap to claim: a clone carries a committed brain.json, and an environment variable can
// say anything. So a name is bound to the directory that first used it, and every read,
// write, count and backup has to stay inside what the current project may see. Each test
// below is one way a project once reached, or could have reached, another's data.

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sharedLab pins every store path inside a fresh directory, so projects opened with
// "shared":true in this test meet in one scratch shared store and nowhere else.
func sharedLab(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(base, "config"))
	t.Setenv("OS_BRAIN_GLOBAL_DB", filepath.Join(base, "config", "brain", "brain.db"))
	t.Setenv("OS_BRAIN_DB", "")
	t.Setenv("OS_BRAIN_PROJECT", "")
	t.Setenv("OS_BRAIN_PROJECT_ROOT", "")
	return base
}

// openAtDir writes dir/brain/brain.json and opens it.
func openAtDir(t *testing.T, dir, cfg string) (*Store, error) {
	t.Helper()
	here := filepath.Join(dir, "brain")
	if err := os.MkdirAll(here, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(here, "brain.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(NewEnv(here))
	if err == nil {
		t.Cleanup(func() { s.Close() })
	}
	return s, err
}

// mustOpenAt is openAtDir for a directory the test expects to open.
func mustOpenAt(t *testing.T, dir, cfg string) *Store {
	t.Helper()
	s, err := openAtDir(t, dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The basic attack: a second directory, such as a clone carrying a committed brain.json,
// declares an existing project's name. It is refused whether the name comes from the file
// or the environment, and whether or not it also claims the victim's directory as its root.
// The refusal names the way out for a project that really moved.
func TestASecondDirectoryClaimingAProjectsNameIsRefused(t *testing.T) {
	base := sharedLab(t)
	victim := mustOpenAt(t, filepath.Join(base, "victim"), `{"project":"victim","shared":true}`)
	remember(t, victim, RememberArgs{Title: "victim prod database password rotation", Body: "SECRET-VICTIM-BODY"})

	_, err := openAtDir(t, filepath.Join(base, "clone"), `{"project":"victim","shared":true}`)
	if err == nil || !strings.Contains(err.Error(), "another project in the shared store") {
		t.Fatalf("a clone declaring victim's name opened its memories: %v", err)
	}
	if !strings.Contains(err.Error(), "relabel --claim") {
		t.Errorf("the refusal does not name the way out: %v", err)
	}

	// Through the environment instead of the file.
	t.Setenv("OS_BRAIN_PROJECT", "victim")
	if _, err := openAtDir(t, filepath.Join(base, "envclone"), `{"project":"evil","shared":true}`); err == nil {
		t.Fatal("OS_BRAIN_PROJECT=victim from another directory opened victim's memories")
	}
	t.Setenv("OS_BRAIN_PROJECT", "")

	// brain.json's root set to the victim's real directory borrows nothing.
	cfg, _ := json.Marshal(map[string]interface{}{"project": "victim", "shared": true,
		"root": filepath.Join(base, "victim")})
	if _, err := openAtDir(t, filepath.Join(base, "rootclone"), string(cfg)); err == nil {
		t.Fatal("a brain.json root pointing at the victim borrowed its identity")
	}
	// OS_BRAIN_PROJECT_ROOT is the same claim from the environment.
	t.Setenv("OS_BRAIN_PROJECT_ROOT", filepath.Join(base, "victim"))
	if _, err := openAtDir(t, filepath.Join(base, "envroot"), `{"project":"victim","shared":true}`); err == nil {
		t.Fatal("OS_BRAIN_PROJECT_ROOT pointing at the victim borrowed its identity")
	}
	t.Setenv("OS_BRAIN_PROJECT_ROOT", "")

	// The real directory still opens, and still holds its memory.
	again := mustOpenAt(t, filepath.Join(base, "victim"), `{"project":"victim","shared":true}`)
	if n := len(recall(t, again, RecallArgs{Query: "rotation"})["results"].([]interface{})); n != 1 {
		t.Errorf("the owner lost its memory: %d results", n)
	}
}

// A name with no binding yet, which the project index shows living in another directory
// (a store written before bindings existed), is not handed to the first new directory that
// asks for it. A name nothing knows about is.
func TestAnUnboundNameTheIndexPlacesElsewhereIsRefused(t *testing.T) {
	base := sharedLab(t)
	first := mustOpenAt(t, filepath.Join(base, "first"), `{"project":"first","shared":true}`)
	if err := first.RegisterProject(); err != nil {
		t.Fatal(err)
	}
	if _, err := first.DB.Exec("INSERT INTO projects(root_id,name,last_seen) VALUES(?,?,?)",
		hashRoot(filepath.Join(base, "old")), "legacy", Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := openAtDir(t, filepath.Join(base, "new"), `{"project":"legacy","shared":true}`); err == nil {
		t.Fatal("a name the index places in another directory was bound to a new one")
	}
	// Nothing anywhere says otherwise: the first directory gets it.
	if _, err := openAtDir(t, filepath.Join(base, "fresh"), `{"project":"fresh","shared":true}`); err != nil {
		t.Fatalf("a brand-new name was refused: %v", err)
	}
}

// A project that really moved needs its name back. relabel --claim moves the binding, but
// only when a person confirms it: with no confirmer, or a declined one, it fails. After the
// claim the new directory opens and the old one no longer does.
func TestClaimMovesTheBindingOnlyWithAPerson(t *testing.T) {
	base := sharedLab(t)
	old := mustOpenAt(t, filepath.Join(base, "old"), `{"project":"mover","shared":true}`)
	remember(t, old, RememberArgs{Title: "mover keeps this fact about kestrels", Body: "x"})
	old.Close()

	moved := filepath.Join(base, "moved")
	if _, err := openAtDir(t, moved, `{"project":"mover","shared":true}`); err == nil {
		t.Fatal("the moved copy opened without a claim")
	}
	env := NewEnv(filepath.Join(moved, "brain"))
	env.Claim = true
	s, err := Open(env)
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
	if _, err := openAtDir(t, moved, `{"project":"mover","shared":true}`); err != nil {
		t.Fatalf("the claimed directory does not open: %v", err)
	}
	if _, err := openAtDir(t, filepath.Join(base, "old"), `{"project":"mover","shared":true}`); err == nil {
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
		e := Env{Here: here, Config: Config{Root: root}}
		if got := e.ProjectRoot(); realpath(got) != realpath(want) {
			t.Errorf("root %q: ProjectRoot is %s, want %s", root, got, want)
		}
	}
	// Outside a repository only the brain's parent, or a root whose own hook names it.
	plain := filepath.Join(base, "plain", "sub", "brain")
	if err := os.MkdirAll(plain, 0o700); err != nil {
		t.Fatal(err)
	}
	e := Env{Here: plain, Config: Config{Root: filepath.Join(base, "plain")}}
	if got := e.ProjectRoot(); realpath(got) != realpath(filepath.Join(base, "plain", "sub")) {
		t.Errorf("an unhooked root outside a repository was believed: %s", got)
	}
	hook := `{"hooks":{"SessionStart":[{"hooks":[{"command":"OS_BRAIN_HOME=\"$CLAUDE_PROJECT_DIR/sub/brain\" os-brain wakeup"}]}]}}`
	if err := os.MkdirAll(filepath.Join(base, "plain", ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "plain", ".claude", "settings.json"), []byte(hook), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := e.ProjectRoot(); realpath(got) != realpath(filepath.Join(base, "plain")) {
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
	if got, ok := DiscoverHome(repo); ok {
		t.Errorf("discovery followed a hook out of the repository to %s", got)
	}
	t.Setenv("OS_BRAIN_DB", "")
	t.Setenv("CLAUDE_PROJECT_DIR", repo)
	if err := CheckHookHome(sib); err == nil {
		t.Error("a hook's OS_BRAIN_HOME outside its project was accepted")
	}
	if err := CheckHookHome(filepath.Join(repo, "brain")); err != nil {
		t.Errorf("the project's own brain was refused: %v", err)
	}
	t.Setenv("OS_BRAIN_DB", filepath.Join(sib, "brain.db"))
	if err := CheckHookHome(filepath.Join(repo, "brain")); err == nil {
		t.Error("a hook's OS_BRAIN_DB outside its project was accepted")
	}
}

// The index can hold more than one row for a name: a stale one from an old directory, or a
// duplicate. A row saying private=0 must not make a private project readable again, through
// --all-projects recall, the OTHER PROJECTS list or `global`.
func TestAStaleOrDuplicateIndexRowDoesNotUnprivateAProject(t *testing.T) {
	base := sharedLab(t)
	victim := mustOpenAt(t, filepath.Join(base, "victim"), `{"project":"victim","shared":true}`)
	remember(t, victim, RememberArgs{Title: "zebracorp acquisition target confidential", Body: "x"})
	if err := victim.Private(io.Discard, PrivateArgs{}); err != nil {
		t.Fatal(err)
	}
	other := mustOpenAt(t, filepath.Join(base, "other"), `{"project":"other","shared":true}`)
	if err := other.RegisterProject(); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{filepath.Join(base, "stale"), filepath.Join(base, "dup")} {
		if _, err := other.DB.Exec("INSERT INTO projects(root_id,name,private,last_seen) VALUES(?,?,0,?)",
			hashRoot(root), "victim", Now()); err != nil {
			t.Fatal(err)
		}
	}
	if contains(other.readableProjects(), "victim") {
		t.Fatal("a private project is readable through a stale or duplicate index row")
	}
	got := recall(t, other, RecallArgs{Query: "zebracorp", AllProjects: true, Strict: true, NoTouch: true})
	if n := len(got["results"].([]interface{})); n != 0 {
		t.Errorf("--all-projects returned %d of a private project's memories", n)
	}
	for _, sb := range other.OtherProjects() {
		if sb.Name == "victim" {
			t.Error("a private project is listed under OTHER PROJECTS")
		}
	}
	var buf bytes.Buffer
	if err := other.Global(&buf, GlobalArgs{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "victim") {
		t.Errorf("global lists a private project:\n%s", buf.String())
	}
}

// A project of the same name with its own database opens, but the name in the shared index
// is bound elsewhere. private must fail loudly rather than report a declaration the index
// never received, must not write private into brain.json, and the project must not read
// that name's mail.
func TestPrivateFailsLoudlyWhenTheIndexIsNotWritten(t *testing.T) {
	base := sharedLab(t)
	owner := mustOpenAt(t, filepath.Join(base, "owner"), `{"project":"app","shared":true}`)
	if err := owner.RegisterProject(); err != nil {
		t.Fatal(err)
	}
	// A project of the same name with its own database: it opens, but the name is not its.
	other := mustOpenAt(t, filepath.Join(base, "other"), `{"project":"app"}`)
	err := other.Private(io.Discard, PrivateArgs{})
	if err == nil || !strings.Contains(err.Error(), "index was not updated") {
		t.Fatalf("private reported success over an index it did not write: %v", err)
	}
	cfg, _ := LoadConfig(filepath.Join(base, "other", "brain", "brain.json"))
	if cfg.Private {
		t.Error("brain.json says private although the index was never told")
	}
	// And the name-addressed mail is not that project's either.
	if err := other.ownsName(other.globalConnect(false)); err == nil {
		t.Error("a project whose name is bound elsewhere reads that name's mail")
	}
}

// Approving a probe lets it run commands. The approval belongs to the directory, not the
// name, or any directory naming the project would inherit it. An approval recorded by name
// before this change still counts in its own directory.
func TestProbeApprovalIsKeyedToTheDirectory(t *testing.T) {
	s := newTestStore(t)
	if !strings.Contains(s.trustKey(), hashRoot(s.Env.ProjectRoot())) {
		t.Fatalf("the trust key carries no directory: %s", s.trustKey())
	}
	if _, err := s.DB.Exec("INSERT INTO meta(k,v) VALUES(?,?)", s.legacyTrustKey(), "abc"); err != nil {
		t.Fatal(err)
	}
	if !s.trusted()["abc"] {
		t.Error("an approval recorded by name before the upgrade was lost in its own directory")
	}
	named := *s
	named.Env.Named = true
	if named.trusted()["abc"] {
		t.Error("a caller naming the project inherited its approvals")
	}
	moved := *s
	moved.Env.Here = filepath.Join(t.TempDir(), "brain")
	if err := moved.setTrusted([]string{"def"}); err != nil {
		t.Fatal(err)
	}
	if s.trusted()["def"] {
		t.Error("an approval from another directory applies here")
	}
}

// A memory id is a guessable number. get by id, the filtered-out count in recall's JSON and
// the use counter must all stay inside what this project may read: a private project's
// memory answers as missing, its matches are not even counted, and searching another
// project on purpose must not change that project's ranking.
func TestAPrivateProjectsMemoryIsNotReachableById(t *testing.T) {
	base := sharedLab(t)
	victim := mustOpenAt(t, filepath.Join(base, "victim"), `{"project":"victim","shared":true}`)
	remember(t, victim, RememberArgs{Title: "victim private payroll zebracorn figure", Body: "250k"})
	var id int64
	if err := victim.DB.QueryRow("SELECT id FROM memories WHERE project='victim'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := victim.Private(io.Discard, PrivateArgs{}); err != nil {
		t.Fatal(err)
	}
	alpha := mustOpenAt(t, filepath.Join(base, "alpha"), `{"project":"alpha","shared":true}`)
	if err := alpha.RegisterProject(); err != nil {
		t.Fatal(err)
	}
	for _, all := range []bool{false, true} {
		var buf bytes.Buffer
		err := alpha.Get(&buf, id, all, true)
		if err == nil || err.Error() != NoMemory(id).Error() {
			t.Errorf("get #%d (all-projects=%v) of a private project: %v %s", id, all, err, buf.String())
		}
	}
	var buf bytes.Buffer
	if err := alpha.Recall(&buf, RecallArgs{Query: "zebracorn", Type: "decision", JSON: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "filtered_out") {
		t.Errorf("the filtered-out count reached a private project: %s", buf.String())
	}

	// A readable project, searched on purpose, is still not this project's to rerank.
	if err := victim.Private(io.Discard, PrivateArgs{Off: true, Yes: true}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		recall(t, alpha, RecallArgs{Query: "zebracorn", AllProjects: true})
	}
	var used int
	if err := alpha.DB.QueryRow("SELECT use_count FROM memories WHERE id=?", id).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != 0 {
		t.Errorf("another project's recall bumped this memory's use count to %d", used)
	}
	if err := alpha.Get(io.Discard, id, true, false); err != nil {
		t.Errorf("a readable project's memory, asked for across projects: %v", err)
	}
}

// The MCP server lets a caller name the project it wants. Naming a private project is
// refused, and the roster reports it as private. Naming a new project still works.
func TestNamingAPrivateProjectToTheServerIsRefused(t *testing.T) {
	base := sharedLab(t)
	victim := mustOpenAt(t, filepath.Join(base, "victim"), `{"project":"victim","shared":true}`)
	remember(t, victim, RememberArgs{Title: "zzzsecret holds nothing", Body: "x"})
	if err := victim.Private(io.Discard, PrivateArgs{}); err != nil {
		t.Fatal(err)
	}
	srv := mustOpenAt(t, filepath.Join(base, "srv"), `{"project":"srv","shared":true}`)
	if !srv.PrivateProject("victim") {
		t.Error("a private project is not reported private to the roster")
	}
	env := NewEnvFor(filepath.Join(base, "srv", "brain"), "victim")
	if !env.Named {
		t.Fatal("an explicitly named project is not marked Named")
	}
	if s, err := Open(env); err == nil {
		s.Close()
		t.Fatal("a named private project opened through the server")
	}
	if s, err := Open(NewEnvFor(filepath.Join(base, "srv", "brain"), "brandnew")); err != nil {
		t.Errorf("naming a new project to the server was refused: %v", err)
	} else {
		s.Close()
	}
}

// Leads and measures are addressed by id too. Another project's id must answer as missing,
// for reads and for writes, and must not be changed.
func TestLeadsAndMeasuresByIdStayInTheirProject(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.DB.Exec("INSERT INTO leads(project,name,email,status,created_at,updated_at) "+
		"VALUES('alpha','Jane Private','jane@acme.example','new',?,?)", Now(), Now()); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := s.Lead(&buf, LeadArgs{Sub: "show", ID: 1}); err == nil || strings.Contains(buf.String(), "Jane") {
		t.Errorf("lead show read another project's lead: %v %s", err, buf.String())
	}
	st := "lost"
	if err := s.Lead(io.Discard, LeadArgs{Sub: "update", ID: 1, Status: &st}); err == nil {
		t.Error("lead update wrote another project's lead")
	}
	var status string
	_ = s.DB.QueryRow("SELECT status FROM leads WHERE id=1").Scan(&status)
	if status != "new" {
		t.Errorf("another project's lead was changed to %q", status)
	}
	if _, err := s.DB.Exec("INSERT INTO measures(project,key,value,unit,taken_on,taken_at,created_at) "+
		"VALUES('alpha','weight',80,'kg','2026-11-01',?,?)", Now(), Now()); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"forget", "relabel"} {
		err := s.Measure(io.Discard, MeasureArgs{Sub: sub, ID: 1, Unit: "lb"})
		if err == nil || err.Error() != "no measure #1" {
			t.Errorf("measure %s of another project's id: %v", sub, err)
		}
	}
}

// standard add --from copies a memory into the machine-wide standards every project reads.
// It may lift only this project's own memory, and not while the project is private.
func TestStandardFromLiftsOnlyThisProjectsMemory(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.DB.Exec("INSERT INTO memories(project,scope,mem_type,title,body,source,"+
		"importance,pinned,confidence,source_date,happened_on,hash,created_at,updated_at) "+
		"VALUES('alpha','global','note','alpha secret diagnosis','condition X','session',3,0,"+
		"'reported','','',?,?,?)", "h1", Now(), Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Standard(io.Discard, StandardArgs{Confirm: yes, Sub: "add", FromID: 1}); err == nil {
		t.Fatal("standard --from lifted another project's memory into the spine")
	}
	remember(t, s, RememberArgs{Title: "testproj convention about tabs", Body: "tabs"})
	id := lastID(t, s)
	s.Env.Private = true
	if err := s.Standard(io.Discard, StandardArgs{Confirm: yes, Sub: "add", FromID: id}); err == nil {
		t.Error("standard --from lifted a private project's memory into the spine")
	}
	s.Env.Private = false
	if err := s.Standard(io.Discard, StandardArgs{Confirm: yes, Sub: "add", FromID: id}); err != nil {
		t.Errorf("lifting this project's own memory: %v", err)
	}
}

// relabel renames this project's rows and recomputes their hashes. It must not touch
// another project's rows, and a duplicate check on remember must not echo another
// project's body back.
func TestRelabelRehashesOnlyThisProject(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "mine under an old name entirely", Body: "x"})
	if _, err := s.DB.Exec("UPDATE memories SET project='oldlabel'"); err != nil {
		t.Fatal(err)
	}
	victim := *s
	victim.Env.Project = "victim"
	remember(t, &victim, RememberArgs{Title: "victim prod database password rotation procedure", Body: "SECRET-VICTIM-BODY"})
	var before string
	if err := s.DB.QueryRow("SELECT hash FROM memories WHERE project='victim'").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := s.Relabel(io.Discard, RelabelArgs{Old: "oldlabel", Apply: true, IMeanIt: true}); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := s.DB.QueryRow("SELECT hash FROM memories WHERE project='victim'").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("relabel rehashed another project's memory")
	}
	var buf bytes.Buffer
	_ = s.Remember(&buf, RememberArgs{Scope: "global", Type: "note", Importance: 3, Source: "session",
		Title: "victim prod database password rotation procedure", Body: "mine"})
	if strings.Contains(buf.String(), "SECRET-VICTIM-BODY") {
		t.Error("remember echoed another project's body")
	}
}

// A backup lands in the project directory, where it may be committed or shared. On a shared
// store it holds this project's rows only: no other project's memories or notes, and no
// machine-wide tables. A whole-store backup needs a person to confirm it.
func TestABackupOfASharedStoreHoldsOnlyThisProject(t *testing.T) {
	base := sharedLab(t)
	victim := mustOpenAt(t, filepath.Join(base, "victim"), `{"project":"victim","shared":true}`)
	remember(t, victim, RememberArgs{Title: "victim memory about zebracorn deals", Body: "x"})
	if err := victim.Private(io.Discard, PrivateArgs{}); err != nil {
		t.Fatal(err)
	}
	other := mustOpenAt(t, filepath.Join(base, "other"), `{"project":"other","shared":true}`)
	if err := other.RegisterProject(); err != nil {
		t.Fatal(err)
	}
	if _, err := other.DB.Exec("INSERT INTO notes(to_project,from_project,body,created_at) "+
		"VALUES('victim','other','PRIVATE-NOTE-FROM-OTHER-TO-VICTIM',?)", Now()); err != nil {
		t.Fatal(err)
	}
	third := mustOpenAt(t, filepath.Join(base, "third"), `{"project":"third","shared":true}`)
	remember(t, third, RememberArgs{Title: "third keeps one fact of its own", Body: "y"})
	out := filepath.Join(base, "third", "bk.json")
	if err := third.Backup(io.Discard, BackupArgs{Out: out, AllowTracked: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"PRIVATE-NOTE", "zebracorn", `"victim"`, "lesson_fts"} {
		if strings.Contains(string(data), leak) {
			t.Errorf("the backup carries %s", leak)
		}
	}
	if !strings.Contains(string(data), "third keeps one fact") {
		t.Error("the backup lost this project's own memory")
	}
	if err := third.Backup(io.Discard, BackupArgs{Out: filepath.Join(base, "third", "w.json"),
		AllowTracked: true, WholeStore: true}); err == nil {
		t.Error("a whole-store backup ran with no person confirming it")
	}
}

// bootstrap --done marks this project as bootstrapped, not every project in the store.
func TestBootstrapDoneIsPerProject(t *testing.T) {
	base := sharedLab(t)
	a := mustOpenAt(t, filepath.Join(base, "a"), `{"project":"a","shared":true}`)
	b := mustOpenAt(t, filepath.Join(base, "b"), `{"project":"b","shared":true}`)
	if err := a.Bootstrap(io.Discard, BootstrapArgs{Done: true}); err != nil {
		t.Fatal(err)
	}
	if b.bootstrappedAt() != 0 {
		t.Error("one project's --done marked another bootstrapped")
	}
	if a.bootstrappedAt() == 0 {
		t.Error("--done did not mark its own project")
	}
}

// prune refuses to delete half a brain. That guard has to count the rows this prune can
// reach, this project's alone, or a large neighbour hides a small project being emptied.
// An --all-projects prune backs up every row it deletes and tombstones each under its own
// project.
func TestPruneCountsAndBacksUpWhatItReaches(t *testing.T) {
	base := sharedLab(t)
	fill := func(s *Store, prefix, typ string, n int) {
		for i := 0; i < n; i++ {
			remember(t, s, RememberArgs{Title: prefix + " " + string(rune('a'+i)), Type: typ,
				Body: "x", AllowDuplicate: true})
		}
	}
	big := mustOpenAt(t, filepath.Join(base, "big"), `{"project":"big","shared":true}`)
	if err := big.RegisterProject(); err != nil {
		t.Fatal(err)
	}
	fill(big, "big durable fact", "infra", 30)
	fill(big, "big decayed status", "state", 2)
	small := mustOpenAt(t, filepath.Join(base, "small"), `{"project":"small","shared":true}`)
	fill(small, "small decayed status", "state", 6)
	fill(small, "small durable fact", "infra", 5)
	if _, err := small.DB.Exec("UPDATE memories SET updated_at=? WHERE mem_type='state'",
		Now()-90*86400); err != nil {
		t.Fatal(err)
	}
	err := small.Prune(io.Discard, PruneArgs{Tier: "D", OlderThan: 30, Apply: true, MaxDelete: 25,
		AllowTracked: true})
	if err == nil || !strings.Contains(err.Error(), "refusing to delete 6 of 11") {
		t.Fatalf("the half-brain guard did not count this project alone: %v", err)
	}
	bk := filepath.Join(base, "small", "prune.json")
	if err := small.Prune(io.Discard, PruneArgs{Tier: "D", OlderThan: 30, Apply: true, Yes: true,
		AllProjects: true, IMeanIt: true, MaxDelete: 25, Backup: bk, AllowTracked: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(bk)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "big decayed status") {
		t.Error("the backup left out the other project's rows the prune deleted")
	}
	var c int
	if err := small.DB.QueryRow("SELECT COUNT(*) FROM tombstones WHERE project='big' AND " +
		"reason='pruned'").Scan(&c); err != nil {
		t.Fatal(err)
	}
	if c != 2 {
		t.Errorf("the other project's pruned rows have %d tombstones under their own name, want 2", c)
	}
}
