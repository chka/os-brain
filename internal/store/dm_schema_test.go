package store

// Tests for the spine migration that added message kinds and session muting (v10 to v11).

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// This migrates a genuine v10 spine, not a fresh one: migration bugs are invisible against
// a database created at the current version, and the ones this code has had were all of
// that kind. The v10 dms and live_sessions tables are built by hand exactly as v10 shipped
// them, a message is inserted, and the migration must add the columns without disturbing
// it. A message from before v11 must read as plain text, never as a remote-do request.
func TestSpineV11AddsKindAndMuteColumns(t *testing.T) {
	g, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "spine.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	for _, stmt := range []string{
		`CREATE TABLE gmeta (k TEXT PRIMARY KEY, v TEXT)`,
		`CREATE TABLE live_sessions (session TEXT PRIMARY KEY, project TEXT NOT NULL,
			private INTEGER NOT NULL DEFAULT 0, machine TEXT NOT NULL DEFAULT '',
			pid INTEGER NOT NULL DEFAULT 0, started_at INTEGER NOT NULL,
			last_seen INTEGER NOT NULL, listening_at INTEGER, ended_at INTEGER)`,
		`CREATE TABLE dms (id INTEGER PRIMARY KEY AUTOINCREMENT, to_session TEXT NOT NULL,
			to_project TEXT NOT NULL, from_session TEXT NOT NULL DEFAULT '',
			from_project TEXT NOT NULL DEFAULT '', from_machine TEXT NOT NULL DEFAULT '',
			body TEXT NOT NULL, created_at INTEGER NOT NULL, delivered_at INTEGER,
			delivered_via TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO gmeta(k,v) VALUES('schema_version','10')`,
		`INSERT INTO dms(to_session,to_project,body,created_at) VALUES('s','p','hello',1)`,
	} {
		if _, err := g.Exec(stmt); err != nil {
			t.Fatalf("building the v10 spine: %v", err)
		}
	}

	if err := MigrateGlobal(g, 10); err != nil {
		t.Fatalf("migrateGlobal 10->11: %v", err)
	}

	cols, err := dbColumns(g, "dms")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"kind", "accepted_at", "rejected_at"} {
		if !cols[c] {
			t.Errorf("dms.%s missing after v11", c)
		}
	}
	lcols, err := dbColumns(g, "live_sessions")
	if err != nil {
		t.Fatal(err)
	}
	if !lcols["muted_at"] {
		t.Errorf("live_sessions.muted_at missing after v11")
	}
	var kind string
	if err := g.QueryRow("SELECT kind FROM dms WHERE id=1").Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "text" {
		t.Errorf("a message stored before v11 must read kind=text, got %q", kind)
	}

	// Every step checks what is already there before it alters anything, so a second pass
	// over an already-migrated spine must not error.
	if err := MigrateGlobal(g, 10); err != nil {
		t.Fatalf("second migrateGlobal must be a no-op, got: %v", err)
	}
}
