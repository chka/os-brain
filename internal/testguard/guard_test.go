package testguard

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// A guard nobody has seen fail is a guard nobody knows works: a row added after Start is
// reported, and the rows that were already there are not.
func TestAGuardReportsOnlyTheProjectsAddedDuringTheRun(t *testing.T) {
	dir := t.TempDir()
	spine := filepath.Join(dir, "brain.db")
	db, err := sql.Open("sqlite", "file:"+spine)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE projects (root_id TEXT PRIMARY KEY, name TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projects VALUES ('aaaa', 'real')`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OS_BRAIN_GLOBAL_DB", spine)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "nothing-here"))

	g := Start()
	if leaks := g.Leaks(); len(leaks) != 0 {
		t.Fatalf("nothing was added yet, got %v", leaks)
	}
	if _, err := db.Exec(`INSERT INTO projects VALUES ('bbbb', '001')`); err != nil {
		t.Fatal(err)
	}
	leaks := g.Leaks()
	if len(leaks) != 1 || !strings.Contains(leaks[0], `"001"`) {
		t.Fatalf("expected exactly the new project, got %v", leaks)
	}
}
