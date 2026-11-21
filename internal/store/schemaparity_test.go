package store_test

import (
	"database/sql"
	"sort"
	"strings"
	"testing"
)

// A fresh database and a migrated one must end up with the same shape. Two different pieces
// of code build them: SchemaDDL creates the tables at the current version, and migrateSteps
// walks an old database forward. Nothing else makes them agree.
//
// They once disagreed. v16 added six columns to migrateSteps and not to SchemaDDL, and
// migrate() has a fast path that stamps a brand new database at the current version
// without running the steps, on the fair assumption that the schema already built it
// correctly. So every new brain was created permanently missing those columns, never
// revisited the step that adds them, and the failure surfaced only as "no such column:
// stated_count" the first time somebody used the feature.
//
// A new column has two homes, and forgetting one is invisible until a user hits it. This
// test compares every table, so it covers the next such column too.
func TestAFreshDatabaseHasTheSameShapeAsAMigratedOne(t *testing.T) {
	fresh := columnsOf(t, newTestStore(t).DB)

	// A genuine v1 database walked all the way forward, built by the same fixtures as the
	// rest of the migration tests in migrate_test.go.
	s := openMigrated(t, makeV1(t))
	migrated := columnsOf(t, s.DB)

	for table, cols := range fresh {
		got, ok := migrated[table]
		if !ok {
			// A table only SchemaDDL creates is fine: CREATE TABLE IF NOT EXISTS runs on
			// every open, so a migrated database gets it too.
			continue
		}
		if missing := minus(cols, got); len(missing) > 0 {
			t.Errorf("table %s: a migrated database is missing %s",
				table, strings.Join(missing, ", "))
		}
		if extra := minus(got, cols); len(extra) > 0 {
			t.Errorf("table %s: a FRESH database is missing %s.\n"+
				"A migration step added a column that SchemaDDL's CREATE TABLE does not "+
				"have, and migrate() stamps a brand new database at the current version "+
				"without running the steps - so every new brain is created without it, "+
				"permanently", table, strings.Join(extra, ", "))
		}
	}
}

// columnsOf maps every ordinary table in db to its sorted column names. Search index
// tables are left out: FTS5 creates their shadow tables itself.
func columnsOf(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' " +
		"AND name NOT LIKE 'sqlite_%' AND name NOT LIKE '%_fts%'")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	rows.Close()
	for _, tbl := range tables {
		cols, err := db.Query("PRAGMA table_info(" + tbl + ")")
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for cols.Next() {
			var cid int
			var name, typ string
			var notnull, pk int
			var dflt interface{}
			if err := cols.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				t.Fatal(err)
			}
			names = append(names, name)
		}
		cols.Close()
		sort.Strings(names)
		out[tbl] = names
	}
	return out
}

// minus returns the entries of a that are not in b.
func minus(a, b []string) []string {
	have := map[string]bool{}
	for _, x := range b {
		have[x] = true
	}
	var out []string
	for _, x := range a {
		if !have[x] {
			out = append(out, x)
		}
	}
	return out
}
