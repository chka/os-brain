package brain

import (
	"database/sql"
	"fmt"
	"sort"
	"strconv"
)

// Migration rules, for anyone adding a step:
//
//   - Additive only. A step adds a column or a table, or rewrites values in place; it never
//     drops user data. An install's brain.db is the only copy of that project's memory.
//   - All or nothing. Every step runs in one transaction with the version stamp, because a
//     half-applied migration fails on every later invocation and locks the owner out of
//     their own memory.
//   - Never backfill a guess. A new column's default must state what is actually known
//     about an old row, which is usually "nothing".
//   - The SQLite format is shared with ns-brain, so a brain can move between the two
//     editions. Never change a step that has shipped.

// legacyFTSTriggers belonged to the old external-content index. The v6 step drops them
// and never creates them: see ftsDDL for why the index is written in code instead.
var legacyFTSTriggers = []string{"mem_ai", "mem_ad", "mem_au"}

// migrate brings an older brain.db up to SchemaVersion, opens a newer one only when it
// declares this client compatible, and never migrates downward.
func (s *Store) migrate() error {
	have := s.storedSchemaVersion()
	switch {
	case have > SchemaVersion:
		return s.acceptNewerSchema(have)
	case have == SchemaVersion:
		return s.healCompatStamp()
	case have == 0:
		fresh, err := s.isEmptyUnversioned()
		if err != nil {
			return err
		}
		if fresh {
			return s.stampBrandNew()
		}
	}
	return s.migrateFrom(have)
}

// storedSchemaVersion is the version in meta, or 0 for a database that has none: either
// brand new or written before versions were recorded.
func (s *Store) storedSchemaVersion() int {
	var raw string
	have := 0
	if err := s.DB.QueryRow("SELECT v FROM meta WHERE k='schema_version'").Scan(&raw); err == nil {
		have, _ = strconv.Atoi(raw)
	}
	return have
}

// acceptNewerSchema decides whether this client may open a database a newer client has
// migrated. Newer is not the same as unusable: most steps add a defaulted column or a
// table, and an older client reads and writes around those correctly. Only a change to the
// MEANING of what is already stored shuts one out, and the database itself carries that
// number (see compat.go for why it has to be the one to say).
func (s *Store) acceptNewerSchema(have int) error {
	need, known := recordedMinClient(s.DB, "meta")
	switch {
	case !known:
		return fmt.Errorf("this database is schema v%d; this binary understands v%d, and "+
			"the database predates the compatibility register so it cannot say whether "+
			"that is safe.\nUpgrade this client before it touches %s:\n"+
			"  install a newer os-brain release",
			have, SchemaVersion, s.Env.DBPath)
	case SchemaVersion < need:
		return fmt.Errorf("this database is schema v%d and needs a client understanding "+
			"v%d or later; this binary is v%d.\nUpgrade it before it touches %s:\n"+
			"  install a newer os-brain release",
			have, need, SchemaVersion, s.Env.DBPath)
	}
	// Compatible. Open it as it is: do NOT migrate, and do not restamp the version down to
	// this client's, which would tell the next reader the newer columns are not there.
	return nil
}

// healCompatStamp gives a current-version store the compatibility stamp if it was written
// before the register existed. Safe to assert here and only here: this client is at the
// store's own version, so it is the authority on what that version requires.
func (s *Store) healCompatStamp() error {
	if need, known := recordedMinClient(s.DB, "meta"); !known || need != MinCompatibleSchema {
		return stampCompat(s.DB, "meta", MinCompatibleSchema)
	}
	return nil
}

// isEmptyUnversioned tells a brand-new file (no version, no memories) from a pre-version
// one that holds data and must be migrated.
func (s *Store) isEmptyUnversioned() (bool, error) {
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM memories").Scan(&n); err != nil {
		return false, err
	}
	return n == 0, nil
}

// stampBrandNew marks a new database as current. The schema has already built it at the
// current version, and stamping it instead of running the steps avoids DDL that several
// agents starting at once would deadlock on.
func (s *Store) stampBrandNew() error {
	if _, err := s.DB.Exec("INSERT OR REPLACE INTO meta(k,v) VALUES('schema_version',?)",
		strconv.Itoa(SchemaVersion)); err != nil {
		return err
	}
	return stampCompat(s.DB, "meta", MinCompatibleSchema)
}

// migrateFrom runs every step after have, then stamps the new version and its compatibility
// requirement, all in one transaction.
func (s *Store) migrateFrom(have int) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	if err := s.migrateSteps(tx, have); err != nil {
		tx.Rollback()
		return fmt.Errorf("migration to v%d failed and was rolled back: %w\n        %s is "+
			"unchanged. Copy it, then report this", SchemaVersion, err, s.Env.DBPath)
	}
	if _, err := tx.Exec("INSERT OR REPLACE INTO meta(k,v) VALUES('schema_version',?)",
		strconv.Itoa(SchemaVersion)); err != nil {
		tx.Rollback()
		return err
	}
	// In the same transaction as the version it describes: a store that claims v14 but
	// still declares v8's requirement would hand the next client the wrong answer.
	if err := stampCompat(tx, "meta", MinCompatibleSchema); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// migrateSteps runs the steps in ascending order: each assumes only what the previous ones
// guarantee. The one exception is the v9 step running before v6, an order inherited from
// the first implementation and kept because v6 reindexes from the memories table, so the
// columns v9 adds must already exist.
//
// v10 (measures, measure_limits), v14 (session_log) and v15 (tasks) have no step: they only
// add tables, which schemaDDL's CREATE TABLE IF NOT EXISTS has already built by the time
// this runs, and none of them has anything to backfill. Existing `commitment` memories in particular are left
// where they are rather than moved into tasks: that would rewrite what somebody stored as a
// fact into something with a state machine, a judgement no migration gets to make.
func (s *Store) migrateSteps(tx *sql.Tx, have int) error {
	steps := []struct {
		before int
		run    func(*sql.Tx) error
	}{
		{2, s.migrateV2SupersedeAndTitleIdentity},
		{4, migrateV4Provenance},
		{5, s.migrateV5FoldAccents},
		{9, migrateV9Dates},
		{6, s.migrateV6IndexInCode},
		{8, s.migrateV8ProjectColumn},
		{11, migrateV11SourceCommit},
		{12, migrateV12TrustAndValidity},
		{13, migrateV13Session},
		{16, migrateV16Ladder},
	}
	for _, step := range steps {
		if have < step.before {
			if err := step.run(tx); err != nil {
				return err
			}
		}
	}
	return nil
}

// v2: retraction links, and identity by title. v1 hashed scope+title+body, so a reworded
// body duplicated the memory; v2 hashes scope and the normalised title.
func (s *Store) migrateV2SupersedeAndTitleIdentity(tx *sql.Tx) error {
	if err := addMissingStoreColumns(tx, "memories", column{"superseded_by", "INTEGER"}); err != nil {
		return err
	}
	return s.rehash(tx, "")
}

// v4: how good the source was, and when it was checked.
func migrateV4Provenance(tx *sql.Tx) error {
	return addMissingStoreColumns(tx, "memories",
		column{"confidence", "TEXT NOT NULL DEFAULT 'reported'"},
		column{"source_date", "TEXT NOT NULL DEFAULT ''"},
		column{"verified_at", "INTEGER"},
	)
}

// v5: unicode61 only folds Latin-1 diacritics by default, so a Greek or accented query
// missed its own memory. Identity now ignores accents too, so every hash is recomputed.
// The same step settles confidence onto the canonical vocabulary: aliases become their
// canonical name and anything unknown becomes the default.
func (s *Store) migrateV5FoldAccents(tx *sql.Tx) error {
	for alias, canon := range confidenceAliases {
		if _, err := tx.Exec("UPDATE memories SET confidence=? WHERE confidence=?",
			canon, alias); err != nil {
			return err
		}
	}
	known := make([]string, 0, len(Confidence))
	for k := range Confidence {
		known = append(known, k)
	}
	sort.Strings(known)
	args := append([]interface{}{DefaultConfidence}, toIfaces(known)...)
	if _, err := tx.Exec("UPDATE memories SET confidence=? WHERE confidence NOT IN ("+
		placeholders(len(known))+")", args...); err != nil {
		return err
	}
	return s.rehash(tx, "")
}

// v9: when the thing happened, and the snooze and review clocks.
func migrateV9Dates(tx *sql.Tx) error {
	return addMissingStoreColumns(tx, "memories",
		column{"happened_on", "TEXT NOT NULL DEFAULT ''"},
		column{"snooze_until", "INTEGER"},
		column{"review_on", "INTEGER"},
	)
}

// v6: replace the trigger-maintained index with one written in code, and fill it.
func (s *Store) migrateV6IndexInCode(tx *sql.Tx) error {
	for _, trig := range legacyFTSTriggers {
		if _, err := tx.Exec("DROP TRIGGER IF EXISTS " + trig); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("DROP TABLE IF EXISTS mem_fts"); err != nil {
		return err
	}
	if _, err := tx.Exec(ftsDDL); err != nil {
		return err
	}
	return s.reindexTx(tx)
}

// v8: one store, with project as a first-class column. Existing rows belong to the project
// whose brain.json sits beside this database, so they are stamped with it. A table that
// does not exist in this old file is skipped.
func (s *Store) migrateV8ProjectColumn(tx *sql.Tx) error {
	for _, table := range []string{"memories", "events", "leads", "tombstones", "mem_history"} {
		have, err := txColumns(tx, table)
		if err != nil {
			return err
		}
		if len(have) == 0 {
			continue
		}
		if err := addColumnsNotIn(tx, table, have,
			column{"project", "TEXT NOT NULL DEFAULT ''"}); err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE "+table+" SET project=? WHERE project=''",
			s.Env.Project); err != nil {
			return err
		}
	}
	return nil
}

// v11: the commit a claim was true against. Deliberately not backfilled: nothing knows
// which tree an existing memory was recorded against, and stamping them all with today's
// HEAD would assert that every one of them has been checked against the current code. That
// is the exact false confidence this column exists to remove.
func migrateV11SourceCommit(tx *sql.Tx) error {
	for _, table := range []string{"memories", "mem_history"} {
		have, err := txColumns(tx, table)
		if err != nil {
			return err
		}
		if len(have) == 0 {
			continue
		}
		if err := addColumnsNotIn(tx, table, have,
			column{"source_commit", "TEXT NOT NULL DEFAULT ''"}); err != nil {
			return err
		}
	}
	return nil
}

// v12: trust and the validity window. Every existing row is 'stated' because that is what
// it was, and an empty valid_to means "no known end", not "expired".
func migrateV12TrustAndValidity(tx *sql.Tx) error {
	return addMissingStoreColumns(tx, "memories",
		column{"trust", "TEXT NOT NULL DEFAULT 'stated'"},
		column{"valid_from", "TEXT NOT NULL DEFAULT ''"},
		column{"valid_to", "TEXT NOT NULL DEFAULT ''"},
	)
}

// v13: which session wrote a memory or an event. Before this, events had an actor column
// holding the constant 'claude' and memories had nothing, so after two sessions disagreed no
// query could answer "was this one agent or two?", which you need to know to trust either.
// Not backfilled: an empty session means "written before the brain tracked sessions", which
// is true, where stamping today's id on every old row would be a lie about who wrote them.
func migrateV13Session(tx *sql.Tx) error {
	if err := addMissingStoreColumns(tx, "memories",
		column{"session", "TEXT NOT NULL DEFAULT ''"}); err != nil {
		return err
	}
	return addMissingStoreColumns(tx, "events", column{"session", "TEXT NOT NULL DEFAULT ''"})
}

// v16: the ladder. Repetition is authority: a memory restated after a RECORDED objection
// is hardened, and restated in a different session it is law. All of it is derived from
// what the log can show, never set by hand, and never backfilled: a memory written before
// this has stated_count 1 and no hardened_at, which is true, where inventing a history
// would assert an argument that never happened. last_stated_at defaults to 0, which the
// read path treats as "unknown"; copying updated_at in would claim the memory was restated
// on the day it was last edited, a different fact.
//
// from_utterance is the citation half of the same idea. An uncited memory is not wrong, it
// is one whose provenance predates the column.
func migrateV16Ladder(tx *sql.Tx) error {
	return addMissingStoreColumns(tx, "memories",
		column{"stated_count", "INTEGER NOT NULL DEFAULT 1"},
		column{"last_stated_at", "INTEGER NOT NULL DEFAULT 0"},
		column{"hardened_at", "INTEGER"},
		column{"law_at", "INTEGER"},
		column{"from_utterance", "INTEGER"},
		column{"rejected", "TEXT NOT NULL DEFAULT ''"},
	)
}

// column is a column a step adds: its name and the declaration that follows the name.
type column struct{ name, decl string }

// addMissingStoreColumns adds each column the table does not already have, so a step never
// fails on a column the file already holds.
func addMissingStoreColumns(tx *sql.Tx, table string, cols ...column) error {
	have, err := txColumns(tx, table)
	if err != nil {
		return err
	}
	return addColumnsNotIn(tx, table, have, cols...)
}

func addColumnsNotIn(tx *sql.Tx, table string, have map[string]bool, cols ...column) error {
	for _, c := range cols {
		if have[c.name] {
			continue
		}
		if _, err := tx.Exec("ALTER TABLE " + table + " ADD COLUMN " + c.name + " " + c.decl); err != nil {
			return err
		}
	}
	return nil
}

// rehash recomputes every hash under the current identity rule. Two passes: clear them
// all first, because assigning row A's new hash while row B still holds it violates the
// unique index. On a duplicate title the most recently updated row keeps the hash, and
// the loser gets its own stable identity rather than a NULL it can never recover from.
//
// project narrows it to one project's rows; "" is every row, for the migrations that change
// the rule itself. Each row is hashed under ITS OWN project, and a retracted row keeps a
// placeholder rather than the live identity, as remember --supersedes leaves it. Rehashing
// the whole shared store under one caller's name once let another project's remember match
// a private project's row and print its body.
func (s *Store) rehash(tx *sql.Tx, project string) error {
	all, err := s.clearHashes(tx, project)
	if err != nil {
		return err
	}
	seen := map[string]int64{}
	for _, r := range all {
		h := ContentHash(r.project, r.scope, r.title)
		if r.retracted {
			if err := setHash(tx, r.id, supersededHash(r.id, h)); err != nil {
				return err
			}
			continue
		}
		if loser, clash := seen[h]; clash {
			if err := setHash(tx, loser,
				ContentHash(r.project, r.scope, fmt.Sprintf("%s #%d", r.title, loser))); err != nil {
				return err
			}
			Warn("migration: #%d and #%d share a title in %s; review and merge them",
				loser, r.id, Repr(r.scope))
		}
		seen[h] = r.id
		if err := setHash(tx, r.id, h); err != nil {
			return err
		}
	}
	return nil
}

type hashRow struct {
	id                    int64
	project, scope, title string
	retracted             bool
}

// clearHashes NULLs the hashes rehash is about to recompute and returns those rows, oldest
// update first so the newest of a duplicate pair is assigned last and keeps the hash. A
// file older than v8 has no project column, and its rows belong to this project.
func (s *Store) clearHashes(tx *sql.Tx, project string) ([]hashRow, error) {
	cols, err := txColumns(tx, "memories")
	if err != nil {
		return nil, err
	}
	projCol, sel := "project", []interface{}{}
	if !cols["project"] {
		projCol, sel = "?", []interface{}{s.Env.Project}
	}
	where, args := "", []interface{}{}
	if project != "" && cols["project"] {
		where, args = " WHERE project = ?", []interface{}{project}
	}
	if _, err := tx.Exec("UPDATE memories SET hash=NULL"+where, args...); err != nil {
		return nil, err
	}
	rows, err := tx.Query("SELECT id, "+projCol+", scope, title, superseded_by IS NOT NULL "+
		"FROM memories"+where+" ORDER BY updated_at ASC, id ASC", append(sel, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []hashRow
	for rows.Next() {
		var r hashRow
		if err := rows.Scan(&r.id, &r.project, &r.scope, &r.title, &r.retracted); err != nil {
			return nil, err
		}
		all = append(all, r)
	}
	return all, nil
}

func setHash(tx *sql.Tx, id int64, hash string) error {
	_, err := tx.Exec("UPDATE memories SET hash=? WHERE id=?", hash, id)
	return err
}

// txColumns is the set of column names a table has; empty when the table does not exist.
func txColumns(tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}
