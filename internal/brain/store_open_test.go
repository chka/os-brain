package brain

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Opening the store file: exactly the path given, and never a schema this client is too old
// to write safely.

// Several clients (os-brain and the team edition, old and new versions) may open the same
// file, and an older one must not write rows a newer schema depends on. The schema version
// alone does not decide that: the database declares the oldest client it can be shared
// with (see compat.go), and this is the case where the answer is "not you".
func TestOpenRefusesADatabaseThatDemandsANewerClient(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.DB.Exec("INSERT OR REPLACE INTO meta(k,v) VALUES('schema_version','99')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT OR REPLACE INTO meta(k,v) VALUES('min_client_schema','98')"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(NewEnv(s.Env.Here)); err == nil {
		t.Error("expected a refusal on a schema past a compatibility break")
	} else if !strings.Contains(err.Error(), "v99") {
		t.Errorf("the refusal must name the version, got %v", err)
	}
}

// SQLite reads a file: name as a URI, so a # or ? in a directory name opened a different
// file than the one asked for.
func TestSQLiteURIOpensExactlyThePathGiven(t *testing.T) {
	for _, name := range []string{"c#x", "q?y", "p%41"} {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "brain.db")
		db, err := sql.Open("sqlite", dsnFor(path))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("CREATE TABLE t(x)"); err != nil {
			t.Fatal(err)
		}
		db.Close()
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s: the database is not at the path asked for: %v", name, err)
		}
	}
}
