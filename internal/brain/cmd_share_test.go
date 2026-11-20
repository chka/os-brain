package brain

// Tests for `share`, which moves a project's rows from its own brain.db into the shared
// store (and `share --off`, which moves them back). It moves the only copy of a project's
// memory, so it must be a dry run until --apply, must carry every table and every supersede
// link, must refuse rather than guess on a collision, and must leave the old file and a dump
// behind. The file also holds the tests for the other things a project publishes to the
// shared layer (its index row under `private`, notes between projects, axioms), backup's
// own output marker, and update's refusal to blank a body.

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// projectWithEverything is an installed project with rows in more tables than `absorb`
// knows about. The point of `share` is the tables nobody remembers, so the fixture has to
// have some: tombstones, measures, measure limits, tasks and the session log.
func projectWithEverything(t *testing.T) (string, *Store) {
	t.Helper()
	root, st := installedProject(t)
	remember(t, st, RememberArgs{Scope: "channel:deploy", Type: "gotcha",
		Title: "the certificate renews on the edge box", Body: "certbot lives there"})
	if err := st.Log(devNull{}, LogArgs{Scope: "global", Kind: "deploy", Summary: "shipped v3"}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO tombstones(hash,project,scope,title,reason,forgotten_at)
		 VALUES('h1','` + st.Env.Project + `','global','a wrong fact','it was wrong',1)`,
		`INSERT INTO measures(project,scope,key,label,value,unit,taken_on,taken_at,created_at)
		 VALUES('` + st.Env.Project + `','global','latency','p99',42,'ms','2026-08-01',1,1)`,
		`INSERT INTO measure_limits(project,key,low,high,better,unit,why,updated_at)
		 VALUES('` + st.Env.Project + `','latency',0,100,'lower','ms','slo',1)`,
		`INSERT INTO tasks(project,title,created_at,updated_at) VALUES('` + st.Env.Project + `','ship it',1,1)`,
		`INSERT INTO session_log(project,session,started_at,last_seen) VALUES('` + st.Env.Project + `','s1',1,2)`,
	} {
		if _, err := st.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return root, st
}

// devNull is an io.Writer that discards everything.
type devNull struct{}

func (devNull) Write(p []byte) (int, error) { return len(p), nil }

// reopenShare opens the project at root the way the next session would, so the test sees
// whichever database brain.json now points at.
func reopenShare(t *testing.T, root string) *Store {
	t.Helper()
	s, err := Open(NewEnv(filepath.Join(root, "brain")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// countOwnedBy counts the rows a project owns in one table.
func countOwnedBy(t *testing.T, s *Store, table, project string) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE project=?", project).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// plantOldSharedStore puts a shared store at the default path that an older client left
// behind: the tables a schema v8 store had, and its version stamp.
func plantOldSharedStore(t *testing.T) string {
	t.Helper()
	shared := sharedDBPath()
	if err := os.MkdirAll(filepath.Dir(shared), 0o700); err != nil {
		t.Fatal(err)
	}
	old, err := sql.Open("sqlite", "file:"+shared)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	for _, q := range []string{
		`CREATE TABLE meta (k TEXT PRIMARY KEY, v TEXT NOT NULL)`,
		`INSERT INTO meta VALUES('schema_version','8')`,
		`CREATE TABLE memories (id INTEGER PRIMARY KEY AUTOINCREMENT, project TEXT NOT NULL
		 DEFAULT '', scope TEXT NOT NULL DEFAULT 'global', title TEXT NOT NULL,
		 body TEXT NOT NULL DEFAULT '', hash TEXT UNIQUE, created_at INTEGER NOT NULL,
		 updated_at INTEGER NOT NULL)`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return shared
}

// --- the plan (dry run) ---

// Every bulk write is a dry run until --apply, and this one moves the only copy of a
// project's memory. The plan must say it is a plan, name every table with rows, and leave
// the source database and brain.json exactly as they were.
func TestShareIsADryRunUntilApply(t *testing.T) {
	root, st := projectWithEverything(t)
	var buf strings.Builder
	if err := st.Share(&buf, ShareArgs{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "DRY RUN") {
		t.Errorf("a plan that does not say it is a plan:\n%s", buf.String())
	}
	for _, table := range []string{"memories", "events", "tombstones", "measures", "tasks"} {
		if !strings.Contains(buf.String(), table) {
			t.Errorf("the plan does not name %s, which has rows:\n%s", table, buf.String())
		}
	}
	st.Close()
	if _, err := os.Stat(filepath.Join(root, "brain", "brain.db")); err != nil {
		t.Errorf("the source database is gone after a dry run: %v", err)
	}
	after := reopenShare(t, root)
	if after.Env.Shared {
		t.Error("a dry run moved the project onto the shared store")
	}
}

// Tables that belong to the file rather than to a project cannot be filed under this
// project on the other side. Naming them in the plan is the difference between a move and a
// silent loss.
func TestShareNamesWhatItCannotMove(t *testing.T) {
	_, st := projectWithEverything(t)
	if _, err := st.DB.Exec(`CREATE TABLE oddity (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`INSERT INTO oddity(v) VALUES('x')`); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	if err := st.Share(&buf, ShareArgs{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "oddity") {
		t.Errorf("a table with rows was dropped from the plan in silence:\n%s", buf.String())
	}
}

// --json is what a script reads, and a move it cannot parse is a move it cannot verify.
func TestShareJSONReportsWhatMoved(t *testing.T) {
	_, st := projectWithEverything(t)
	var buf strings.Builder
	if err := st.Share(&buf, ShareArgs{JSON: true}); err != nil {
		t.Fatal(err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(buf.String()), &out); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	if out["applied"] != false {
		t.Errorf("a dry run reports applied=%v", out["applied"])
	}
	if _, ok := out["tables"]; !ok {
		t.Errorf("no per-table breakdown in the JSON: %s", buf.String())
	}
}

// A plan is not allowed to write. Opening a store migrates it, so a dry run that opened the
// destination moved the SHARED file, every other project's database, forward a schema
// version, silently, on a command whose whole promise is that it changed nothing.
func TestShareDryRunDoesNotMigrateTheDestination(t *testing.T) {
	_, st := projectWithEverything(t)
	shared := plantOldSharedStore(t)

	if err := st.Share(devNull{}, ShareArgs{}); err != nil {
		t.Fatal(err)
	}

	check, err := sql.Open("sqlite", "file:"+shared+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var v string
	if err := check.QueryRow("SELECT v FROM meta WHERE k='schema_version'").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != "8" {
		t.Errorf("the dry run migrated the shared store from v8 to v%s", v)
	}
}

// The plan describes what --apply will do, and --apply migrates the destination first.
// Reading an older destination's tables as they are today reported session_log and
// measures as unmovable, and then --apply moved them anyway.
func TestSharePlanAccountsForTheMigrationApplyPerforms(t *testing.T) {
	_, st := projectWithEverything(t)
	plantOldSharedStore(t)

	var buf strings.Builder
	if err := st.Share(&buf, ShareArgs{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "session_log (the destination has no such table)") {
		t.Errorf("the plan calls session_log unmovable, and --apply moves it:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "migrates it to v") {
		t.Errorf("the plan does not say it will migrate the shared file:\n%s", buf.String())
	}
}

// --- the move ---

// Every table with a project column moves, not only the two that `absorb` knows about. The
// moved memories must be in the search index, the old file must be kept under a name that
// is no longer brain.db, and a dump only the owner can read must be written first.
func TestShareMovesEveryProjectScopedTable(t *testing.T) {
	root, st := projectWithEverything(t)
	project := st.Env.Project
	before := map[string]int{}
	tables, err := st.projectTables()
	if err != nil {
		t.Fatal(err)
	}
	for _, tb := range tables {
		before[tb] = countOwnedBy(t, st, tb, project)
	}

	var buf strings.Builder
	if err := st.Share(&buf, ShareArgs{Apply: true}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	after := reopenShare(t, root)
	if !after.Env.Shared {
		t.Fatal("brain.json still does not say shared")
	}
	if realpath(after.Env.DBPath) != realpath(sharedDBPath()) {
		t.Fatalf("the project reads %s, not the shared store", after.Env.DBPath)
	}
	for tb, n := range before {
		if n == 0 {
			continue
		}
		if got := countOwnedBy(t, after, tb, project); got != n {
			t.Errorf("%s: %d rows here, %d arrived", tb, n, got)
		}
	}

	// The search index holds its own copy of the content: rows copied with raw SQL would sit
	// in the table and be invisible to recall, a failure that looks like success.
	var found strings.Builder
	if err := after.Recall(&found, RecallArgs{Query: "certificate renews", N: 5}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(found.String(), "certificate renews") {
		t.Errorf("the moved memory is not in the index:\n%s", found.String())
	}

	// The file it came from is kept, and is no longer named brain.db: a brain.json without
	// "shared" would read that file again, and by then it would be out of date.
	if _, err := os.Stat(filepath.Join(root, "brain", "brain.db")); err == nil {
		t.Error("brain.db is still there, one config edit from being authoritative again")
	}
	kept, _ := filepath.Glob(filepath.Join(root, "brain", "brain.db-premove-*"))
	if len(kept) != 1 {
		t.Fatalf("the store it came from was not kept: %v", kept)
	}
	if !strings.Contains(buf.String(), kept[0]) {
		t.Errorf("the output does not say where the old store is:\n%s", buf.String())
	}
	dumps, _ := filepath.Glob(filepath.Join(root, "brain", "brain-move-backup-*.json"))
	if len(dumps) != 1 {
		t.Fatalf("no backup was written before the move: %v", dumps)
	}
	if info, err := os.Stat(dumps[0]); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the backup is the whole project in cleartext and is mode %v", info.Mode().Perm())
	}
}

// Memory ids change in the move. A correction that arrives superseding nothing puts the
// retracted fact back into recall as current, so the link must be remapped the right way
// round.
func TestShareCarriesSupersessionForward(t *testing.T) {
	root, st := installedProject(t)
	remember(t, st, RememberArgs{Title: "the queue runs on redis", Body: "old"})
	var id int64
	if err := st.DB.QueryRow("SELECT id FROM memories WHERE title LIKE 'the queue%'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	remember(t, st, RememberArgs{Title: "the queue runs on postgres now", Body: "new",
		Supersedes: id})
	if err := st.Share(devNull{}, ShareArgs{Apply: true}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	after := reopenShare(t, root)
	var oldTitle, newTitle string
	err := after.DB.QueryRow(`SELECT o.title, n.title FROM memories o
		JOIN memories n ON n.id = o.superseded_by WHERE o.project=?`, after.Env.Project).
		Scan(&oldTitle, &newTitle)
	if err != nil {
		t.Fatalf("the supersession link did not come across: %v", err)
	}
	if !strings.Contains(oldTitle, "redis") || !strings.Contains(newTitle, "postgres") {
		t.Errorf("the link points the wrong way: %q superseded by %q", oldTitle, newTitle)
	}
}

// The test above uses two different titles, which is what hid this bug. A correction stored
// under the SAME title is the common shape (the fact changed, the subject did not), and it
// is the one the move dropped: the retracted half survived, the correction did not arrive,
// and the retracted row's superseded_by was remapped onto its own id.
func TestShareKeepsBothHalvesOfASameTitleSupersession(t *testing.T) {
	root, st := installedProject(t)
	remember(t, st, RememberArgs{Title: "who has root on the boxes", Body: "two keys"})
	var id int64
	if err := st.DB.QueryRow("SELECT id FROM memories WHERE title LIKE 'who has root%'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	remember(t, st, RememberArgs{Title: "who has root on the boxes", Body: "five keys",
		Supersedes: id})
	if err := st.Share(devNull{}, ShareArgs{Apply: true}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	after := reopenShare(t, root)
	var n int
	if err := after.DB.QueryRow("SELECT COUNT(*) FROM memories WHERE project=? AND "+
		"title LIKE 'who has root%'", after.Env.Project).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("both halves should have moved, got %d rows", n)
	}
	var self int
	if err := after.DB.QueryRow("SELECT COUNT(*) FROM memories WHERE superseded_by=id").
		Scan(&self); err != nil {
		t.Fatal(err)
	}
	if self != 0 {
		t.Errorf("%d memories supersede themselves; the id remap collapsed the pair", self)
	}
	var live string
	if err := after.DB.QueryRow("SELECT body FROM memories WHERE project=? AND "+
		"title LIKE 'who has root%' AND superseded_by IS NULL", after.Env.Project).
		Scan(&live); err != nil {
		t.Fatalf("no live memory survived: %v", err)
	}
	if live != "five keys" {
		t.Errorf("the surviving memory is the retracted one: body %q", live)
	}
	// The successor holds the identity hash and the retracted row holds one of its own, so
	// re-remembering the title finds the successor rather than a stale row.
	var liveID, hashOwner int64
	if err := after.DB.QueryRow("SELECT id FROM memories WHERE project=? AND "+
		"title LIKE 'who has root%' AND superseded_by IS NULL", after.Env.Project).
		Scan(&liveID); err != nil {
		t.Fatal(err)
	}
	h := ContentHash(after.Env.Project, "global", "who has root on the boxes")
	if err := after.DB.QueryRow("SELECT id FROM memories WHERE hash=?", h).Scan(&hashOwner); err != nil {
		t.Fatalf("nothing holds the live identity after the move: %v", err)
	}
	if hashOwner != liveID {
		t.Errorf("the retracted memory kept the identity: #%d holds it, #%d is live",
			hashOwner, liveID)
	}
}

// An archived version whose memory was forgotten before the move already dangles in the
// source. Dropping it destroys the only copy of that memory's text; keeping its id points it
// at whatever row holds that number in the shared file, which is another project's. So it
// moves with the link cleared, and the output says so.
func TestShareCarriesOrphanedHistoryWithTheLinkCleared(t *testing.T) {
	root, st := installedProject(t)
	project := st.Env.Project
	if _, err := st.DB.Exec(`INSERT INTO mem_history(project,mem_id,scope,mem_type,title,
		body,action,archived_at) VALUES(?,4242,'global','note','a forgotten fact',
		'its body','forget',1)`, project); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	if err := st.Share(&buf, ShareArgs{Apply: true}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	after := reopenShare(t, root)
	var mem int64
	var title string
	if err := after.DB.QueryRow(`SELECT mem_id, title FROM mem_history
		WHERE project=? AND title='a forgotten fact'`, project).Scan(&mem, &title); err != nil {
		t.Fatalf("the archived version was dropped by the move: %v", err)
	}
	if mem != 0 {
		t.Errorf("it still points at memory %d, which on this side belongs to whoever holds that id", mem)
	}
	if !strings.Contains(buf.String(), "no longer linked") {
		t.Errorf("the output does not say a row arrived unlinked:\n%s", buf.String())
	}
}

// Every project moving in for the first time is on an OLDER schema than the shared store,
// so the source has fewer columns than the destination. Naming a column the source does not
// have binds NULL into a NOT NULL column and fails the entire move; the missing columns must
// take the destination's defaults instead.
func TestShareMovesASourceOnAnOlderSchema(t *testing.T) {
	root, st := installedProject(t)
	project := st.Env.Project
	// A source predating v12: no trust column, and no session column either.
	for _, q := range []string{
		`DROP INDEX IF EXISTS idx_mem_session`,
		`ALTER TABLE memories DROP COLUMN trust`,
		`ALTER TABLE memories DROP COLUMN session`,
	} {
		if _, err := st.DB.Exec(q); err != nil {
			t.Skipf("this sqlite build cannot drop a column: %v", err)
		}
	}
	if _, err := st.DB.Exec(`INSERT INTO memories(project,scope,mem_type,title,body,hash,
		created_at,updated_at) VALUES(?,'global','note','an older row','body','oldhash',1,1)`,
		project); err != nil {
		t.Fatal(err)
	}
	if err := st.Share(devNull{}, ShareArgs{Apply: true}); err != nil {
		t.Fatalf("a source on an older schema could not be moved: %v", err)
	}
	st.Close()

	after := reopenShare(t, root)
	var trust, session string
	if err := after.DB.QueryRow(`SELECT trust, session FROM memories
		WHERE project=? AND title='an older row'`, project).Scan(&trust, &session); err != nil {
		t.Fatalf("the row did not arrive: %v", err)
	}
	if trust != "stated" {
		t.Errorf("the column the source did not have arrived as %q, not the schema default", trust)
	}
	if session != "" {
		t.Errorf("session should be empty for a row written before the brain tracked sessions, got %q", session)
	}
}

// Two answers to one question, one on each side of the move. Picking a winner by insert
// order is the widest wrong action, so the move refuses and names the way to reconcile.
func TestShareRefusesASameIdentityCollisionWithADifferentBody(t *testing.T) {
	root, st := installedProject(t)
	remember(t, st, RememberArgs{Title: "the queue runs on redis", Body: "first answer"})
	if err := st.Share(devNull{}, ShareArgs{Apply: true}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// Put the pre-move file back as this project's own store, edit the body, and move it in
	// again: the shared store still holds the row under the same identity. That is the shape
	// of a project somebody kept working in after a half-finished move.
	kept, _ := filepath.Glob(filepath.Join(root, "brain", "brain.db-premove-*"))
	if len(kept) != 1 {
		t.Fatalf("no pre-move store to work from: %v", kept)
	}
	if err := os.Rename(kept[0], filepath.Join(root, "brain", "brain.db")); err != nil {
		t.Fatal(err)
	}
	cfg := mustConfig(t, filepath.Join(root, "brain", "brain.json"))
	cfg.Shared = false
	if err := SaveConfig(filepath.Join(root, "brain"), cfg); err != nil {
		t.Fatal(err)
	}

	local := reopenShare(t, root)
	if local.Env.Shared {
		t.Fatal("the fixture is still pointed at the shared store")
	}
	if _, err := local.DB.Exec("UPDATE memories SET body=? WHERE title LIKE 'the queue%'",
		"a different answer"); err != nil {
		t.Fatal(err)
	}
	err := local.Share(devNull{}, ShareArgs{Apply: true})
	if err == nil {
		t.Fatal("moved a colliding memory instead of refusing")
	}
	for _, want := range []string{"different body", "os-brain update"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
}

// A move interrupted halfway is run again. What is already in the shared store, under the
// same content hash, is skipped and never doubled.
func TestShareRunAgainSkipsWhatIsAlreadyThere(t *testing.T) {
	root, st := projectWithEverything(t)
	project := st.Env.Project
	if err := st.Share(devNull{}, ShareArgs{Apply: true}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	moved := reopenShare(t, root)
	n := countOwnedBy(t, moved, "memories", project)

	// Move it back out to a local file and in a second time.
	if err := moved.Share(devNull{}, ShareArgs{Off: true, Apply: true}); err != nil {
		t.Fatal(err)
	}
	moved.Close()
	local := reopenShare(t, root)
	var buf strings.Builder
	if err := local.Share(&buf, ShareArgs{Apply: true}); err != nil {
		t.Fatal(err)
	}
	local.Close()
	again := reopenShare(t, root)
	if got := countOwnedBy(t, again, "memories", project); got != n {
		t.Errorf("a second move produced %d memories where the first produced %d", got, n)
	}
}

// The move leaves two files that hold the project's whole memory in cleartext (the pre-move
// store and the dump), and what keeps them out of git is a .gitignore edit the same command
// just made: uncommitted, and easily read by the next session as unrelated noise and
// discarded. So the output says both things, and a project whose .gitignore already covers
// them is not warned, because a warning that fires every time is a warning nobody reads.
func TestShareSaysTheIgnoreLinesItWroteAreNotCommitted(t *testing.T) {
	root, st := projectWithEverything(t)
	// An install from before those ignore lines existed: init wrote brain/brain.db and
	// nothing covering the dumps or the pre-move copy.
	ign := filepath.Join(root, ".gitignore")
	body0, err := os.ReadFile(ign)
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, ln := range strings.Split(string(body0), "\n") {
		if strings.Contains(ln, "brain.db*") || strings.Contains(ln, "-backup-") ||
			strings.Contains(ln, "prune") {
			continue
		}
		kept = append(kept, ln)
	}
	if err := os.WriteFile(ign, []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	if err := st.Share(&buf, ShareArgs{Apply: true}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "CLEARTEXT") {
		t.Errorf("the move does not say what it left on disk:\n%s", out)
	}
	if !strings.Contains(out, "NOT COMMITTED") {
		t.Errorf("the move does not say the protection it wrote is uncommitted:\n%s", out)
	}
	body, err := os.ReadFile(ign)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "brain/brain.db*") {
		t.Errorf("the ignore line that covers the premove file was never written:\n%s", body)
	}

	_, st2 := projectWithEverything(t)
	var buf2 strings.Builder
	if err := st2.Share(&buf2, ShareArgs{Apply: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf2.String(), "NOT COMMITTED") {
		t.Errorf("it warns about ignore lines it did not have to write:\n%s", buf2.String())
	}
}

// The declaration has to reach the shared index, or every other project keeps reading the
// row that was true before the move. This is the same reason `private` is a command and not
// a config edit.
func TestShareTellsTheSharedIndex(t *testing.T) {
	root, st := projectWithEverything(t)
	project := st.Env.Project
	if err := st.Share(devNull{}, ShareArgs{Apply: true}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	after := reopenShare(t, root)
	g := after.globalConnect(false)
	if g == nil {
		t.Fatal("no spine")
	}
	defer after.closeGlobal(g)
	var name string
	if err := g.QueryRow("SELECT name FROM projects WHERE name=?", project).Scan(&name); err != nil {
		t.Fatalf("the project is not in the shared index after moving into it: %v", err)
	}
}

// --- moving back out (share --off) ---

// Coming back out takes this project's rows and nothing else's, leaves no index rows
// pointing at memories that are gone, and lands the rows in the project's own database.
func TestShareOffLeavesTheOtherProjectsAlone(t *testing.T) {
	root, st := projectWithEverything(t)
	project := st.Env.Project
	if err := st.Share(devNull{}, ShareArgs{Apply: true}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	moved := reopenShare(t, root)

	neighbour := sharedAs(moved, "somebody-else")
	remember(t, neighbour, RememberArgs{Title: "their own fact", Body: "not yours"})

	var buf strings.Builder
	if err := moved.Share(&buf, ShareArgs{Off: true, Apply: true}); err != nil {
		t.Fatal(err)
	}
	if n := countOwnedBy(t, moved, "memories", project); n != 0 {
		t.Errorf("%d of this project's memories are still in the shared store", n)
	}
	if n := countOwnedBy(t, moved, "memories", "somebody-else"); n != 1 {
		t.Errorf("the neighbour has %d memories left, not 1", n)
	}
	var orphans int
	if err := moved.DB.QueryRow(`SELECT COUNT(*) FROM mem_fts f
		WHERE NOT EXISTS (SELECT 1 FROM memories m WHERE m.id = f.rowid)`).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Errorf("%d index rows point at memories that are gone", orphans)
	}
	moved.Close()

	out := reopenShare(t, root)
	if out.Env.Shared {
		t.Fatal("brain.json still says shared")
	}
	if countOwnedBy(t, out, "memories", project) == 0 {
		t.Error("the memories did not arrive in the project's own database")
	}
}

// A local brain.db left behind by an earlier move is a store, and coming back out merges
// into it, skipping rows already present. A file at that path that is not a database at all
// is refused, because there is nothing to merge into and writing over it would destroy
// whatever it is.
func TestShareOffRefusesALocalFileThatIsNotADatabase(t *testing.T) {
	root, st := projectWithEverything(t)
	if err := st.Share(devNull{}, ShareArgs{Apply: true}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	moved := reopenShare(t, root)
	if err := os.WriteFile(filepath.Join(root, "brain", "brain.db"), []byte("not empty"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := moved.Share(devNull{}, ShareArgs{Off: true, Apply: true}); err == nil {
		t.Fatal("it wrote into a file that is not a database")
	}
}

// Opening a database creates it, so a dry run that opened the destination left an empty
// file behind, and then the real run refused because the guard found what the dry run made.
func TestShareDryRunDoesNotCreateTheDestination(t *testing.T) {
	root, st := projectWithEverything(t)
	if err := st.Share(devNull{}, ShareArgs{Apply: true}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	moved := reopenShare(t, root)
	kept, _ := filepath.Glob(filepath.Join(root, "brain", "brain.db-premove-*"))
	if len(kept) == 1 {
		if err := os.Remove(kept[0]); err != nil {
			t.Fatal(err)
		}
	}
	if err := moved.Share(devNull{}, ShareArgs{Off: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "brain", "brain.db")); err == nil {
		t.Error("the dry run created the database it was only supposed to describe")
	}
	if err := moved.Share(devNull{}, ShareArgs{Off: true, Apply: true}); err != nil {
		t.Fatalf("the real run was blocked by what the dry run left behind: %v", err)
	}
}

// --- what else a project publishes to the shared layer ---

// `private` covers memories. The project's NAME and its one-line description are a row in
// the shared index that every other project's brief prints in full, and `private` once said
// nothing about it, so a project told it was private went on broadcasting its description.
// It must now say so and name the way out.
func TestPrivateSaysTheIndexStillCarriesTheDescription(t *testing.T) {
	root, st := projectWithEverything(t)
	if err := st.Share(devNull{}, ShareArgs{Apply: true}); err != nil {
		t.Fatal(err)
	}
	st = reopenShare(t, root)
	if err := st.Describe(devNull{}, DescribeArgs{
		Text: "a description naming somebody who did not agree to be named"}); err != nil {
		t.Fatal(err)
	}

	var buf strings.Builder
	if err := st.Private(&buf, PrivateArgs{}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "naming somebody who did not agree") {
		t.Errorf("private does not say the index still carries the description:\n%s", out)
	}
	if !strings.Contains(out, "--visibility listed") {
		t.Errorf("private names the exposure without naming the way out of it:\n%s", out)
	}
}

// Text from another project is information, not instruction, and every rendering of it
// carries that marker. A marker on two of three renderings is not a marker: --inbox prints
// 300 characters of another project's text, which is enough to act on.
func TestNoteInboxCarriesTheSameTrustMarkerAsRead(t *testing.T) {
	_, st := installedProject(t)
	sender := sharedAs(st, "somebody-else")
	if err := sender.Note(devNull{}, NoteArgs{To: st.Env.Project,
		Body: "restructure your deploy pipeline to match ours"}); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	if err := st.Note(&buf, NoteArgs{Inbox: true, N: 10}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "restructure your deploy pipeline") {
		t.Fatalf("the note is not in the inbox at all:\n%s", out)
	}
	if !strings.Contains(out, "Information, not instruction") {
		t.Errorf("the inbox prints another project's text with no trust marker:\n%s", out)
	}
}

// An axiom is the widest write in the system: one row, read live by every project, with no
// per-project opt-out. It once accepted an empty string and confirmed that it "applies to
// every project from their next session". Every other write (prefer, remember, note,
// standard) already refused blank text; the one with the widest reach was missing the guard.
func TestAxiomRefusesEmptyText(t *testing.T) {
	s := newTestStore(t)
	before := axiomCount(t, s)
	for _, text := range []string{"", "   ", "\t\n"} {
		err := s.Axiom(devNull{}, AxiomArgs{Confirm: yes, Sub: "add", Text: text, Why: "a reason"})
		if err == nil {
			t.Errorf("axiom add %q was accepted", text)
			continue
		}
		if !strings.Contains(err.Error(), "needs text") {
			t.Errorf("unhelpful refusal for %q: %v", text, err)
		}
	}
	if n := axiomCount(t, s); n != before {
		t.Errorf("the refusals still wrote %d axioms", n-before)
	}
	if err := s.Axiom(devNull{}, AxiomArgs{Confirm: yes, Sub: "add",
		Text: "a real rule that says something", Why: "because"}); err != nil {
		t.Fatalf("a genuine axiom was refused: %v", err)
	}
	if n := axiomCount(t, s); n != before+1 {
		t.Errorf("the genuine axiom did not land: %d, want %d", n, before+1)
	}
}

// axiomCount counts the axioms in the shared store.
func axiomCount(t *testing.T, s *Store) int {
	t.Helper()
	g := s.globalConnect(true)
	if g == nil {
		t.Fatal("no spine")
	}
	defer s.closeGlobal(g)
	var n int
	if err := g.QueryRow("SELECT COUNT(*) FROM axioms").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// --- output markers ---

// backup refused to overwrite its own output: Go's JSON encoder sorts map keys, so `axioms`
// and `events` came before `generated_by` and the marker fell outside the 4096 bytes the
// output guard reads. `backup --out <path>` worked exactly once per path and then blamed the
// file on somebody else.
func TestBackupCanOverwriteItsOwnOutput(t *testing.T) {
	root, st := projectWithEverything(t)
	out := filepath.Join(root, "brain", "bk.json")
	if err := st.Backup(devNull{}, BackupArgs{Out: out}); err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 4096)
	fh, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := fh.Read(head)
	fh.Close()
	if !strings.Contains(string(head[:n]), `"generated_by": "os-brain`) {
		t.Errorf("the marker is not in the first 4096 bytes, which is all the guard reads")
	}
	if err := st.Backup(devNull{}, BackupArgs{Out: out}); err != nil {
		t.Errorf("a second backup to the same path was refused: %v", err)
	}
}

// --- update ---

// update with an empty body is unrecoverable from the caller's side and is almost never
// meant: it happened to a memory holding a whole deploy runbook, because a shell command
// that was supposed to compute the new body failed and `--body ""` reached update, which
// reported success. It now takes --force, and an already-empty body is not a blanking.
func TestUpdateRefusesToBlankANonEmptyBody(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "the deploy runbook", Body: "rsync, then bump the file last"})
	var id int64
	if err := s.DB.QueryRow("SELECT id FROM memories WHERE title='the deploy runbook'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	empty := ""
	err := s.Update(devNull{}, UpdateArgs{ID: id, Body: &empty})
	if err == nil {
		t.Fatal("it emptied the body and reported success")
	}
	for _, want := range []string{"--force", "supersede"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q as a way through: %v", want, err)
		}
	}
	var body string
	if err := s.DB.QueryRow("SELECT body FROM memories WHERE id=?", id).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body == "" {
		t.Error("the refusal still wrote the empty body")
	}

	if err := s.Update(devNull{}, UpdateArgs{ID: id, Body: &empty, Force: true}); err != nil {
		t.Errorf("--force did not get through: %v", err)
	}
	if err := s.Update(devNull{}, UpdateArgs{ID: id, Body: &empty}); err != nil {
		t.Errorf("emptying an already-empty body should be a no-op, not a refusal: %v", err)
	}
}
