package store_test

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/chka/os-brain/internal/recall"
	"github.com/chka/os-brain/internal/store"
)

// Tests for the schema migrations, run against real old databases.
//
// Testing a migration against a fresh database proves nothing: a fresh database is already
// at the current version and skips every step. Both migration bugs that made real brains
// unopenable (a unique-constraint collision while rehashing, and a corrupted search index)
// were invisible that way. So these tests build a genuine v1 database by hand, and step it
// back to every intermediate version, then let Open walk it forward.

// v1Schema is the genuine first shape: no superseded_by, no confidence, no project, no
// happened_on, and an EXTERNAL-content FTS table that was kept in sync by triggers.
const v1Schema = `
CREATE TABLE memories (
    id INTEGER PRIMARY KEY AUTOINCREMENT, scope TEXT NOT NULL DEFAULT 'global',
    mem_type TEXT NOT NULL DEFAULT 'note', title TEXT NOT NULL, body TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT 'session', importance INTEGER NOT NULL DEFAULT 3,
    pinned INTEGER NOT NULL DEFAULT 0, use_count INTEGER NOT NULL DEFAULT 0,
    last_used INTEGER, hash TEXT UNIQUE, created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL);
CREATE INDEX idx_mem_scope ON memories(scope);
CREATE VIRTUAL TABLE mem_fts USING fts5(title, body, content='memories', content_rowid='id');
CREATE TABLE events (id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL,
    scope TEXT NOT NULL DEFAULT 'global', kind TEXT NOT NULL DEFAULT 'note',
    actor TEXT NOT NULL DEFAULT 'claude', summary TEXT NOT NULL, ref TEXT NOT NULL DEFAULT '');
CREATE TABLE leads (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL,
    company TEXT NOT NULL DEFAULT '', role TEXT NOT NULL DEFAULT '',
    channel TEXT NOT NULL DEFAULT '', url TEXT NOT NULL DEFAULT '',
    email TEXT NOT NULL DEFAULT '', segment TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'new', tier TEXT NOT NULL DEFAULT '',
    value_est INTEGER NOT NULL DEFAULT 0, next_action TEXT NOT NULL DEFAULT '',
    next_date TEXT NOT NULL DEFAULT '', notes TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
`

// makeV1 writes a real v1 database and returns the Env pointing at it, without opening it
// through the package. The seed rows include Greek text, which the v1 index could not fold,
// and extra statements let a test add rows or objects of its own.
func makeV1(t *testing.T, extra ...string) store.Env {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "brain")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "brain.json"),
		[]byte(`{"project":"Test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(home, "brain.db")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("OS_BRAIN_GLOBAL_DB", filepath.Join(dir, "config", "brain", "brain.db"))
	t.Setenv("OS_BRAIN_DB", dbPath)
	t.Setenv("OS_BRAIN_PROJECT", "Test")

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range store.SplitStatements(v1Schema) {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("v1 schema: %v", err)
		}
	}
	seed := []string{
		`INSERT INTO memories(scope,mem_type,title,body,hash,created_at,updated_at)
		 VALUES('global','infra','nginx on 8443','tls at nginx','h1',1700000000,1700000000)`,
		`INSERT INTO memories(scope,mem_type,title,body,hash,created_at,updated_at)
		 VALUES('channel:diet','gotcha','φώσφορος στόχος','από τα λαμπ','h2',1700000000,1700000000)`,
		`INSERT INTO events(ts,scope,kind,actor,summary,ref)
		 VALUES(1700000000,'global','labs','claude','λήψη','')`,
	}
	for _, stmt := range append(seed, extra...) {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	db.Close()
	return store.NewEnv(home)
}

// openMigrated opens env, which runs every pending migration, and closes it after the test.
func openMigrated(t *testing.T, env store.Env) *store.Store {
	t.Helper()
	s, err := store.Open(env)
	if err != nil {
		t.Fatalf("open a v1 database: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// The minimum: a v1 brain opens, keeps every row and ends at the current version.
func TestV1OpensAndKeepsEverything(t *testing.T) {
	s := openMigrated(t, makeV1(t))
	n, err := s.Count("SELECT COUNT(*) FROM memories")
	if err != nil || n != 2 {
		t.Errorf("memories: %d (%v)", n, err)
	}
	if n, _ := s.Count("SELECT COUNT(*) FROM events"); n != 1 {
		t.Errorf("events: %d", n)
	}
	var v string
	if err := s.DB.QueryRow("SELECT v FROM meta WHERE k='schema_version'").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != strconv.Itoa(store.SchemaVersion) {
		t.Errorf("schema_version is %q", v)
	}
}

// v1's index was external-content with triggers, which cannot fold Greek. The v6 step drops
// it and rebuilds an own-content one written in code. Both a Latin query and an unaccented
// Greek query must find their memory afterwards.
func TestV1RebuildsTheIndexForSearch(t *testing.T) {
	s := openMigrated(t, makeV1(t))
	got := titles(recallJSON(t, s, recall.RecallArgs{Query: "nginx"}))
	if len(got) != 1 || got[0] != "nginx on 8443" {
		t.Errorf("latin recall after migration: %v", got)
	}
	got = titles(recallJSON(t, s, recall.RecallArgs{Query: "φωσφορος", Scope: "channel:diet"}))
	if len(got) != 1 {
		t.Errorf("greek recall after migration: %v", got)
	}
}

// v1 hashed scope+title+body, so two rows could differ only by body. Under the v2 rule they
// collide, and giving one row its new hash while another still holds it violates the
// unique index. That bug made a real brain unopenable.
func TestV1DuplicateTitlesBothKeepAnIdentity(t *testing.T) {
	env := makeV1(t, `INSERT INTO memories(scope,mem_type,title,body,hash,created_at,updated_at)
		 VALUES('global','infra','NGINX ON 8443!','other','h3',1700000001,1700000001)`)
	s := openMigrated(t, env)
	n, err := s.Count("SELECT COUNT(*) FROM memories WHERE hash IS NULL OR hash=''")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d memories lost their identity in the rehash", n)
	}
	if n, _ := s.Count("SELECT COUNT(*) FROM memories"); n != 3 {
		t.Errorf("the rehash dropped a row: %d left", n)
	}
	// The most recently updated row keeps the natural hash. The other gets a stable hash of
	// its own rather than a NULL it can never recover from.
	var distinct int
	if err := s.DB.QueryRow("SELECT COUNT(DISTINCT hash) FROM memories").Scan(&distinct); err != nil {
		t.Fatal(err)
	}
	if distinct != 3 {
		t.Errorf("expected 3 distinct hashes, got %d", distinct)
	}
}

// Every open runs the migration check, so a second open of a migrated brain must change
// nothing.
func TestMigrationIsIdempotent(t *testing.T) {
	env := makeV1(t)
	s := openMigrated(t, env)
	before, _ := s.Count("SELECT COUNT(*) FROM memories")
	s.Close()
	s2 := openMigrated(t, env)
	after, _ := s2.Count("SELECT COUNT(*) FROM memories")
	if before != after {
		t.Errorf("reopening changed the row count: %d -> %d", before, after)
	}
}

// One bug that made existing brains unopenable was invisible against a fresh database and
// against v1: it appeared only at one intermediate version. So the version stamp is set
// back to every version in turn and the brain must reach the current one with its rows and
// recall intact.
func TestEveryIntermediateVersionMigratesForward(t *testing.T) {
	for v := 1; v < store.SchemaVersion; v++ {
		t.Run(fmt.Sprintf("v%d", v), func(t *testing.T) {
			env := makeV1(t)
			s := openMigrated(t, env) // bring it fully current
			if _, err := s.DB.Exec("UPDATE meta SET v=? WHERE k='schema_version'",
				strconv.Itoa(v)); err != nil {
				t.Fatal(err)
			}
			s.Close()

			s2 := openMigrated(t, env)
			var got string
			if err := s2.DB.QueryRow("SELECT v FROM meta WHERE k='schema_version'").Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != strconv.Itoa(store.SchemaVersion) {
				t.Errorf("stuck at v%s", got)
			}
			if n, _ := s2.Count("SELECT COUNT(*) FROM memories"); n != 2 {
				t.Errorf("memories lost migrating from v%d: %d", v, n)
			}
			if got := titles(recallJSON(t, s2, recall.RecallArgs{Query: "nginx"})); len(got) != 1 {
				t.Errorf("recall broken migrating from v%d: %v", v, got)
			}
		})
	}
}

// v8 made the project a column of every row. Rows written before that belong to the
// project whose brain.json sits beside the database. A label mismatch here once made a whole
// brain's memories invisible with no error.
func TestV8LabelsExistingRowsWithThisProject(t *testing.T) {
	s := openMigrated(t, makeV1(t))
	for _, table := range []string{"memories", "events"} {
		n, err := s.Count("SELECT COUNT(*) FROM " + table + " WHERE project=''")
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%d unlabelled rows left in %s", n, table)
		}
	}
	if n, _ := s.Count("SELECT COUNT(*) FROM memories WHERE project=?", "Test"); n != 2 {
		t.Errorf("rows not labelled with this project: %d", n)
	}
	// ...and they are visible, which is the thing the label mismatch broke.
	if got := titles(recallJSON(t, s, recall.RecallArgs{Query: "nginx"})); len(got) != 1 {
		t.Errorf("migrated rows are invisible to their own project: %v", got)
	}
}

// The columns later versions added must exist on a migrated brain, and confidence must
// default to the value fresh rows get, not to an empty string.
func TestV4And9AddTheirColumnsWithUsableDefaults(t *testing.T) {
	s := openMigrated(t, makeV1(t))
	cols, err := s.ColumnsOf("memories")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"superseded_by", "confidence", "source_date", "verified_at",
		"happened_on", "snooze_until", "review_on", "project"} {
		if !cols[c] {
			t.Errorf("column %q was never added", c)
		}
	}
	var conf string
	if err := s.DB.QueryRow("SELECT confidence FROM memories WHERE id=1").Scan(&conf); err != nil {
		t.Fatal(err)
	}
	if conf != store.DefaultConfidence {
		t.Errorf("confidence defaulted to %q", conf)
	}
}

// source_commit records the commit a memory was checked against. The column has to arrive
// on both tables, and existing rows must stay blank. Backfilling them with today's HEAD
// would claim every memory had been checked against the current code, the false confidence
// the column exists to remove, and worse than having no column at all.
func TestV11AddsSourceCommitAndRefusesToInventOne(t *testing.T) {
	s := openMigrated(t, makeV1(t))
	for _, table := range []string{"memories", "mem_history"} {
		cols, err := s.ColumnsOf(table)
		if err != nil {
			t.Fatal(err)
		}
		if !cols["source_commit"] {
			t.Errorf("%s never got source_commit", table)
		}
	}
	n, err := s.Count("SELECT COUNT(*) FROM memories WHERE source_commit != ''")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d pre-existing memories were stamped with a commit they were never checked against", n)
	}
	// ...and the migrated gotcha therefore renders no verdict rather than a wrong one.
	m, err := s.Own(2, "read")
	if err != nil {
		t.Fatal(err)
	}
	s.StampFreshness([]*store.Memory{m})
	if m.Fresh != nil {
		t.Errorf("a migrated memory got a verdict out of nowhere: %v", m.Fresh)
	}
}

// Migrations are all or nothing: a half-applied migration fails on every later run and locks
// the owner out of their own memory.
//
// To force a failure midway, the fixture adds a view named mem_history. It satisfies
// CREATE TABLE IF NOT EXISTS, so the schema builds, and the v8 step then fails when it
// tries to ALTER it. Any failure midway would do; this one needs no change to the code.
func TestAFailedMigrationLeavesTheDatabaseUnchanged(t *testing.T) {
	env := makeV1(t, "CREATE VIEW mem_history AS SELECT id FROM memories")

	if _, err := store.Open(env); err == nil {
		t.Fatal("expected the migration to fail")
	}

	db, err := sql.Open("sqlite", "file:"+env.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Nothing was stamped as done, so the next run starts the ladder again rather than
	// believing a half-applied database is current.
	var v string
	err = db.QueryRow("SELECT v FROM meta WHERE k='schema_version'").Scan(&v)
	if err == nil && v == strconv.Itoa(store.SchemaVersion) {
		t.Error("a failed migration stamped the version anyway")
	}
	// ...and the rows the migration would have touched are untouched.
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM memories WHERE hash IN ('h1','h2')").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("the rolled-back migration left %d of 2 original hashes", n)
	}
}

// --- The spine (the machine-wide database) ---------------------------------------------

// Upgrading an old brain adds the trust and validity columns without guessing at values: an
// existing row IS 'stated', and an empty valid_to means "no known end", not "expired".
func TestMigratedRowsGetStatedTrustAndNoValidityWindow(t *testing.T) {
	s := openMigrated(t, makeV1(t))
	cols, err := s.ColumnsOf("memories")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"trust", "valid_from", "valid_to"} {
		if !cols[c] {
			t.Fatalf("column %q was never added", c)
		}
	}
	n, err := s.Count("SELECT COUNT(*) FROM memories WHERE trust='stated' AND valid_to=''")
	if err != nil {
		t.Fatal(err)
	}
	total, _ := s.Count("SELECT COUNT(*) FROM memories")
	if n != total {
		t.Errorf("%d of %d migrated rows were not left as stated/open-ended", total-n, total)
	}
}
