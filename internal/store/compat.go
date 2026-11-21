package store

import (
	"database/sql"
	"strconv"
)

// One store is opened by several clients, and they are not all the same build: a laptop
// upgrades, a second machine does not, and the shared spine is touched by every project on
// the box. The old rule was that a client refused ANY database whose schema_version
// exceeded its own, which is far stricter than the truth: almost every migration here is a
// defaulted column or a new table, and an older client reads and writes those perfectly
// well by ignoring them. The cost of the strict rule is real: upgrading one project's brain
// locked out every other client until the binary was replaced everywhere.
//
// So compatibility is one number, not a history. MinCompatibleSchema is the oldest client
// this schema can still be shared with. It moves only when a migration changes the MEANING
// of what is already stored, and then it stays put through every additive version after it.
//
// The number travels IN the database (`min_client_schema` in meta / gmeta), written by
// whichever client migrated it. That is the half that matters: a client cannot reason about
// a break introduced after it was built, so it must be told rather than work it out. A
// newer database carrying no such key is refused, because silence there means "written
// before this existed", not "safe".

// MinCompatibleSchema is the oldest client that may share a store at the current
// SchemaVersion.
//
// It is 8 because v8 made `project` a first-class column: a v7 client writes rows whose
// project is the empty string, and every project-filtered read is then blind to them.
// Everything since (v9's date columns, v10's measures, v11's source_commit, v12's trust,
// v13's session, v14's session_log, v15's tasks, v16's ladder) only adds, and the defaults
// were chosen to mean exactly what an older client omitting them leaves behind.
//
// Bump this in the same commit as the migration that earns it, and only for a change an
// older client cannot survive: identity or hashing rules (v2, v5), how an index is
// maintained (v6), or a column that existing reads filter on (v8). Adding a table or a
// defaulted column is not one of those. Leave it alone otherwise: a number that creeps up
// with every release is the strict rule again, wearing a different hat.
const MinCompatibleSchema = 8

// MinCompatibleGlobalSchema is the same for the spine. Every spine migration so far has
// been additive, and v5's origin columns default to the empty string, which is already what
// the docs give for "nothing recorded who wrote this", so no client has ever been shut out.
const MinCompatibleGlobalSchema = 1

// execer and queryer let the register be written and read on a *sql.DB or inside a
// *sql.Tx, so a migration can stamp it in the same transaction as the version.
type execer interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
}

// stampCompat records what this store requires of a client. Written every time the schema
// version is written, so the two can never disagree.
func stampCompat(e execer, table string, min int) error {
	_, err := e.Exec("INSERT OR REPLACE INTO "+table+"(k,v) VALUES('min_client_schema',?)",
		strconv.Itoa(min))
	return err
}

type queryer interface {
	QueryRow(query string, args ...interface{}) *sql.Row
}

// recordedMinClient reads the number a store carries. `known` is false for a store written
// before this existed, which is the case that must fail closed.
func recordedMinClient(q queryer, table string) (need int, known bool) {
	var raw string
	if err := q.QueryRow("SELECT v FROM " + table + " WHERE k='min_client_schema'").Scan(&raw); err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
