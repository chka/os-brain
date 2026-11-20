package brain

// Tests for what a project publishes about itself in the shared index (the projects table
// in the spine). Every project writes its row on every brief, into one file every other
// project reads, so the row must carry nothing that identifies the machine's layout, and a
// project that opts out must be able to take its row back.

import (
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// projectNames returns the name column of every row in the index, in name order.
func projectNames(t *testing.T, g *sql.DB) []string {
	t.Helper()
	rows, err := g.Query("SELECT name FROM projects ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	return names
}

// indexRowsFor counts the index rows identified as the project at root.
func indexRowsFor(t *testing.T, g *sql.DB, root string) int {
	t.Helper()
	var n int
	if err := g.QueryRow("SELECT COUNT(*) FROM projects WHERE root_id=?",
		hashRoot(root)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Until spine v9 the index stored the absolute path of each project directory: one user's
// layout sitting where every other project's session can read it. The row now identifies the
// project by a hash of its root, and no column may carry a path.
func TestTheIndexNeverStoresAFilesystemPath(t *testing.T) {
	s := newTestStore(t)
	if err := s.Describe(io.Discard, DescribeArgs{Text: "a honeypot", Does: "slows attackers down"}); err != nil {
		t.Fatal(err)
	}
	s.RegisterProject()

	g := s.globalConnect(false)
	if g == nil {
		t.Fatal("no spine")
	}
	defer s.closeGlobal(g)

	root := s.Env.ProjectRoot()
	rows, err := g.Query("SELECT root_id, name, description, does, stack FROM projects")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var rootID, name, desc, does, stack string
		if err := rows.Scan(&rootID, &name, &desc, &does, &stack); err != nil {
			t.Fatal(err)
		}
		seen++
		if rootID != hashRoot(root) {
			t.Errorf("root_id is not the hash of this project's directory: %q", rootID)
		}
		for _, v := range []string{rootID, name, desc, does, stack} {
			if strings.Contains(v, root) || strings.Contains(v, string(filepath.Separator)) {
				t.Errorf("index row carries a filesystem path: %q", v)
			}
		}
	}
	if seen == 0 {
		t.Fatal("the project did not register itself at all")
	}
}

// Hashing the root must not cost identity: `global` still marks exactly one row as this
// project's own, and another project's row is still somebody else's.
func TestTheIndexStillTellsMyRowFromSomebodyElses(t *testing.T) {
	s := newTestStore(t)
	if err := s.Describe(io.Discard, DescribeArgs{Text: "mine"}); err != nil {
		t.Fatal(err)
	}
	g := s.globalConnect(true)
	if g == nil {
		t.Fatal("no spine")
	}
	if _, err := g.Exec("INSERT INTO projects(root_id,name,description,last_seen) VALUES(?,?,?,?)",
		hashRoot(t.TempDir()), "somebody-else", "theirs", Now()); err != nil {
		t.Fatal(err)
	}
	s.closeGlobal(g)

	var buf strings.Builder
	if err := s.Global(&buf, GlobalArgs{}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "<- you are here") {
		t.Errorf("the index cannot identify this project's own row:\n%s", out)
	}
	if strings.Count(out, "<- you are here") != 1 {
		t.Errorf("more than one row claims to be this project:\n%s", out)
	}
}

// Paths an older client already wrote are REPLACED by their hashes, not left alone. A fix
// that only protects projects registered after it shipped protects nobody who already had a
// spine. The migration must also be idempotent.
func TestMigrationReplacesPathsAlreadyInTheIndex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spine.db")
	g, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	// The v8 projects table, keyed by the absolute path.
	if _, err := g.Exec(`CREATE TABLE projects (root TEXT PRIMARY KEY, name TEXT NOT NULL,
		private INTEGER NOT NULL DEFAULT 0, description TEXT NOT NULL DEFAULT '',
		does TEXT NOT NULL DEFAULT '', provides TEXT NOT NULL DEFAULT '',
		keywords TEXT NOT NULL DEFAULT '', stack TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT '', visibility TEXT NOT NULL DEFAULT 'described',
		scopes TEXT NOT NULL DEFAULT '', memories INTEGER NOT NULL DEFAULT 0,
		spec_updated_at INTEGER NOT NULL DEFAULT 0, last_seen INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	clientA := filepath.Join(dir, "clientA", "secret-project")
	clientB := filepath.Join(dir, "clientB", "other-project")
	for _, p := range []string{clientA, clientB} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := g.Exec("INSERT INTO projects(root,name,last_seen) VALUES(?,?,?)",
			p, filepath.Base(p), Now()); err != nil {
			t.Fatal(err)
		}
	}

	if err := migrateGlobal(g, 8); err != nil {
		t.Fatal(err)
	}

	rows, err := g.Query("SELECT root_id FROM projects ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
	}
	rows.Close()
	if len(got) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(got))
	}
	for _, v := range got {
		if strings.Contains(v, string(filepath.Separator)) {
			t.Errorf("a path survived the migration: %q", v)
		}
	}
	if got[0] != hashRoot(clientB) || got[1] != hashRoot(clientA) {
		t.Errorf("rows are not the hashes of the paths they replaced: %v", got)
	}

	// Running it twice must not hash the hash: identity would change under a migration
	// that was interrupted and run again.
	before := got[0]
	if err := migrateGlobal(g, 8); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := g.QueryRow("SELECT root_id FROM projects ORDER BY name").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("a second migration changed the identity: %q then %q", before, after)
	}
}

// With no path in the index there is no directory to stat, so stale rows are dropped by age
// instead: a live project is one that registers. A row seen yesterday stays even though no
// directory exists for it.
func TestStaleIndexRowsAgeOutRatherThanBeingStatted(t *testing.T) {
	s := newTestStore(t)
	g := s.globalConnect(true)
	if g == nil {
		t.Fatal("no spine")
	}
	old := Now() - int64(IndexUnseenDays+1)*86400
	if _, err := g.Exec("INSERT INTO projects(root_id,name,last_seen) VALUES(?,?,?)",
		hashRoot("/gone/long/ago"), "abandoned", old); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Exec("INSERT INTO projects(root_id,name,last_seen) VALUES(?,?,?)",
		hashRoot("/not/a/real/path"), "recent", Now()-86400); err != nil {
		t.Fatal(err)
	}
	dropped := s.pruneIndex(g)
	if dropped != 1 {
		t.Errorf("expected exactly the aged row to be dropped, got %d", dropped)
	}
	names := projectNames(t, g)
	s.closeGlobal(g)
	found := false
	for _, n := range names {
		if n == "abandoned" {
			t.Error("a row unseen for longer than IndexUnseenDays survived")
		}
		if n == "recent" {
			found = true
		}
	}
	if !found {
		t.Error("a recently-seen row was dropped because its directory does not exist")
	}
}

// "index": false in brain.json keeps a project out of the index while it still uses the
// shared store. The default stays on, because an index nobody is in answers nothing.
func TestAProjectCanStayOutOfTheIndexEntirely(t *testing.T) {
	s := newTestStore(t)
	off := false
	s.Env.Config.Index = &off
	s.RegisterProject()

	g := s.globalConnect(false)
	if g == nil {
		return // no spine written at all is a pass
	}
	defer s.closeGlobal(g)
	if n := indexRowsFor(t, g, s.Env.ProjectRoot()); n != 0 {
		t.Errorf("a project with index:false registered itself anyway")
	}

	s.Env.Config.Index = nil
	s.RegisterProject()
	if n := indexRowsFor(t, g, s.Env.ProjectRoot()); n != 1 {
		t.Errorf("a project with no index setting did not register: %d rows", n)
	}
}

// Opting out of the shared layer ("global": false) has to retract what was already
// published, not merely stop publishing. A project that opted out once kept its name,
// private flag and memory count in the spine, due to sit there for the whole
// IndexUnseenDays window.
func TestOptingOutOfTheIndexRemovesTheRowAlreadyThere(t *testing.T) {
	s := newTestStore(t)
	s.RegisterProject()

	g := s.globalConnect(false)
	if g == nil {
		t.Fatal("no spine")
	}
	n := indexRowsFor(t, g, s.Env.ProjectRoot())
	s.closeGlobal(g)
	if n != 1 {
		t.Fatalf("the project did not register in the first place (%d rows)", n)
	}

	off := false
	s.Env.Config.Global = &off
	s.RegisterProject()

	// Read the file directly: globalConnect refuses to open for an opted-out project, which
	// is the whole reason the retraction needs its own path.
	db, err := sql.Open("sqlite", "file:"+s.Env.GlobalDB)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if n := indexRowsFor(t, db, s.Env.ProjectRoot()); n != 0 {
		t.Errorf("the row survived opting out: %d rows still in the shared index", n)
	}
}

// An opted-out project must not bring a spine into existence on its way out.
func TestOptingOutNeverCreatesTheSpine(t *testing.T) {
	s := newTestStore(t)
	off := false
	s.Env.Config.Global = &off
	if err := os.Remove(s.Env.GlobalDB); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	s.RegisterProject()
	if _, err := os.Stat(s.Env.GlobalDB); err == nil {
		t.Error("an opted-out project created the shared store while retracting from it")
	}
}
