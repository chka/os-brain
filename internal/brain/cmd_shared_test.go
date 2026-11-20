package brain

// Tests for state kept per project on the shared store (the spine), which several projects
// read and write at once.

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"
)

// sharedAs returns a copy of the store bound to a different project, for simulating a second
// (or third) project using the same spine. cmd_wipe_test.go and cmd_uninstall_test.go use
// the same pattern for "another project touched the shared store".
func sharedAs(s *Store, project string) *Store {
	other := *s
	other.Env.Project = project
	return &other
}

// Up to spine v13, a broadcast note (to_project '*') kept read, acked and accepted state on
// the note row itself. A broadcast has many recipients, so that state belonged to whichever
// one acted first, nobody recorded which, and every other recipient saw the note as already
// handled. v14 moves recipient state into note_reads. The migration must take the stale
// state off broadcast rows, so every recipient sees them again, and leave addressed notes,
// whose single recipient the state does belong to, untouched.
func TestSpineV14TakesRecipientStateOffBroadcasts(t *testing.T) {
	dir := t.TempDir()
	g, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "spine.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	g.SetMaxOpenConns(1)
	for _, stmt := range splitStatements(globalSchemaDDL) {
		if _, err := g.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	// Wind the current schema back to v13: no note_reads table, the v13 note columns, one
	// broadcast and one addressed note, each carrying recipient state.
	for _, stmt := range []string{
		"DROP TABLE note_reads",
		"ALTER TABLE notes ADD COLUMN payload TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE notes ADD COLUMN authored_by TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE notes ADD COLUMN title TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE notes ADD COLUMN accepted_as INTEGER",
		"INSERT INTO notes(id,to_project,from_project,body,created_at,read_at,acked_at,accepted_as) " +
			"VALUES(1,'*','one','for all',100,200,300,7)",
		"INSERT INTO notes(id,to_project,from_project,body,created_at,read_at,acked_at,accepted_as) " +
			"VALUES(2,'two','one','for two',100,200,300,8)",
		"INSERT INTO gmeta(k,v) VALUES('schema_version','13')",
	} {
		if _, err := g.Exec(stmt); err != nil {
			t.Fatal(stmt, err)
		}
	}
	if err := (&Store{}).ensureGlobalSchema(g); err != nil {
		t.Fatal(err)
	}
	var r1, a1, x1, r2, a2, x2 sql.NullInt64
	if err := g.QueryRow("SELECT read_at, acked_at, accepted_as FROM notes WHERE id=1").Scan(&r1, &a1, &x1); err != nil {
		t.Fatal(err)
	}
	if r1.Valid || a1.Valid || x1.Valid {
		t.Errorf("the broadcast row still carries one recipient's state: %v %v %v", r1, a1, x1)
	}
	if err := g.QueryRow("SELECT read_at, acked_at, accepted_as FROM notes WHERE id=2").Scan(&r2, &a2, &x2); err != nil {
		t.Fatal(err)
	}
	if r2.Int64 != 200 || a2.Int64 != 300 || x2.Int64 != 8 {
		t.Errorf("addressed mail lost its state: %v %v %v", r2, a2, x2)
	}
	var n int
	if err := g.QueryRow("SELECT COUNT(*) FROM note_reads").Scan(&n); err != nil {
		t.Fatalf("no note_reads table after the migration: %v", err)
	}
	var v string
	_ = g.QueryRow("SELECT v FROM gmeta WHERE k='schema_version'").Scan(&v)
	if v != strconv.Itoa(GlobalSchemaVersion) || GlobalSchemaVersion < 14 {
		t.Errorf("schema_version is %s", v)
	}
}
