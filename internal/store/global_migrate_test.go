package store

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/timefmt"
)

// Spine v12 bound each project name to a directory. On a real v11 spine the migration has to
// seed those bindings from the old project index: a name with exactly one directory is
// bound to it, keeping its private flag, and a name with several directories is left
// unbound rather than guessed. It also replaces the paths stored in lessons with their
// hashes, so a lesson no longer reveals where a project lives.
func TestSpineV12SeedsBindingsFromAnOldIndex(t *testing.T) {
	dir := t.TempDir()
	g, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "spine.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	g.SetMaxOpenConns(1)
	for _, stmt := range SplitStatements(GlobalSchemaDDL) {
		if _, err := g.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.Exec("DROP TABLE IF EXISTS project_ids"); err != nil {
		t.Fatal(err)
	}
	rows := [][3]interface{}{{"one", "r1", 1}, {"two", "r2", 0}, {"two", "r3", 0}}
	for _, r := range rows {
		if _, err := g.Exec("INSERT INTO projects(root_id,name,private,last_seen) VALUES(?,?,?,?)",
			r[1], r[0], r[2], timefmt.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.Exec("INSERT INTO lessons(project,project_root,title,published_at) VALUES(?,?,?,?)",
		"one", "/home/someone/clients/secret-project", "a lesson", timefmt.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Exec("INSERT INTO gmeta(k,v) VALUES('schema_version','11')"); err != nil {
		t.Fatal(err)
	}
	if err := (&Store{}).ensureGlobalSchema(g); err != nil {
		t.Fatal(err)
	}
	var root string
	var priv int
	if err := g.QueryRow("SELECT root_id, private FROM project_ids WHERE name='one'").Scan(&root, &priv); err != nil {
		t.Fatalf("an unambiguous name was not bound: %v", err)
	}
	if root != "r1" || priv != 1 {
		t.Errorf("binding for one: %s private=%d", root, priv)
	}
	if err := g.QueryRow("SELECT root_id FROM project_ids WHERE name='two'").Scan(&root); err != sql.ErrNoRows {
		t.Errorf("an ambiguous name was bound to %s", root)
	}
	var pr string
	if err := g.QueryRow("SELECT project_root FROM lessons").Scan(&pr); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(pr, "/") || pr != HashRoot("/home/someone/clients/secret-project") {
		t.Errorf("a lesson still carries a path: %q", pr)
	}
	var v string
	_ = g.QueryRow("SELECT v FROM gmeta WHERE k='schema_version'").Scan(&v)
	if v != strconv.Itoa(GlobalSchemaVersion) {
		t.Errorf("schema_version is %s", v)
	}
}
