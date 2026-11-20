package brain

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// backup and export write the whole brain to a file: every table, in a stable order, with
// owner-only permissions, and only inside the project.

// A hand-written table list is a list somebody forgets to extend: a newly added table
// (measures) was once silently missing from every backup taken on the day it shipped.
// Backup enumerates the tables from the schema instead.
func TestBackupEnumeratesTablesFromTheSchema(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "a fact", Body: "x"})
	if _, err := s.DB.Exec("INSERT INTO measures(project,scope,key,label,value,unit,taken_on,"+
		"taken_at,source,confidence,ref,note,created_at) VALUES(?,'global','req_s','Req/S',1.4,"+
		"'','2026-07-29',?,'session','reported','','',?)", "testproj", Now(), Now()); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(filepath.Dir(s.Env.Here), "dump.json")
	if err := s.Backup(io.Discard, BackupArgs{Out: out, JSON: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var dump map[string]interface{}
	if err := json.Unmarshal(data, &dump); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"memories", "measures", "events", "tombstones", "history"} {
		if _, ok := dump[table]; !ok {
			t.Errorf("backup dropped %q", table)
		}
	}
	if len(dump["measures"].([]interface{})) != 1 {
		t.Error("the measure did not survive the backup")
	}
	st, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("a dump is the densest copy of the brain; mode is %v", st.Mode().Perm())
	}
}

func TestBackupRefusesToWriteOutsideTheProject(t *testing.T) {
	s := newTestStore(t)
	if err := s.Backup(io.Discard, BackupArgs{Out: filepath.Join(os.TempDir(), "escaped.json"), JSON: true}); err == nil {
		t.Error("a dump must not be written outside the project directory")
	}
}

// Exports from os-brain and the team edition must diff cleanly against each other. Go's
// map marshalling sorts keys alphabetically, which would put schema_version before project
// at the top and scope before id inside each row.
func TestExportKeepsColumnOrderSoTheTwoClientsDiffCleanly(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "nginx on 8443", Type: "infra"})
	out := filepath.Join(s.Env.Here, "dump.json")
	if err := s.Export(io.Discard, ExportArgs{Out: out}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if strings.Index(text, `"project"`) > strings.Index(text, `"schema_version"`) {
		t.Error("top-level keys are alphabetical, not the documented order")
	}
	if strings.Index(text, `"id"`) > strings.Index(text, `"scope"`) {
		t.Error("row columns are alphabetical, not SELECT order")
	}
}
