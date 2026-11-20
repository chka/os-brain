package brain

import (
	"bytes"
	"database/sql"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

// restore used to swap in any SQLite file, triggers and all, underneath sessions that had
// the store open. A planted trigger would then copy every new memory somewhere else. It now
// refuses while another connection is open, drops triggers and views and says so, and
// refuses a file with tables a brain does not have.
func TestRestoreDropsPlantedObjectsAndRefusesForeignShapes(t *testing.T) {
	s := newTestStore(t)
	seedFor(t, s, 2)
	gift := filepath.Join(t.TempDir(), "gift.db")
	if _, err := s.DB.Exec("VACUUM INTO ?", gift); err != nil {
		t.Fatal(err)
	}
	g, err := sql.Open("sqlite", SQLiteURI(gift, ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Exec("CREATE TRIGGER siphon AFTER INSERT ON memories BEGIN " +
		"INSERT INTO events(project,ts,scope,kind,actor,summary) VALUES('evilx',0,'global','note','x',NEW.body); END"); err != nil {
		t.Fatal(err)
	}
	g.Exec("CREATE VIEW peek AS SELECT body FROM memories")
	g.Close()

	other, err := sql.Open("sqlite", SQLiteURI(s.Env.DBPath, ""))
	if err != nil {
		t.Fatal(err)
	}
	var n int
	other.QueryRow("SELECT COUNT(*) FROM memories").Scan(&n)
	if err := s.SafetyRestore(io.Discard, gift, true, false); err == nil ||
		!strings.Contains(err.Error(), "another session") {
		t.Fatalf("restore swapped the file under another open connection: %v", err)
	}
	other.Close()

	var out bytes.Buffer
	if err := s.SafetyRestore(&out, gift, true, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "siphon") {
		t.Errorf("the dropped trigger was not reported:\n%s", out.String())
	}
	after, err := Open(s.Env)
	if err != nil {
		t.Fatal(err)
	}
	defer after.Close()
	after.DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type IN ('trigger','view')").Scan(&n)
	if n != 0 {
		t.Errorf("%d planted objects survived the restore", n)
	}

	alien := filepath.Join(t.TempDir(), "alien.db")
	after.DB.Exec("VACUUM INTO ?", alien)
	a, _ := sql.Open("sqlite", SQLiteURI(alien, ""))
	a.Exec("CREATE TABLE stash(x)")
	a.Close()
	if err := after.SafetyRestore(io.Discard, alien, true, false); err == nil ||
		!strings.Contains(err.Error(), "stash") {
		t.Errorf("a copy with a table that is not a brain's was accepted: %v", err)
	}
}
