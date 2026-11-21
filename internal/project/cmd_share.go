package project

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chka/os-brain/internal/diag"
	"github.com/chka/os-brain/internal/dump"
	"github.com/chka/os-brain/internal/fsguard"
	"github.com/chka/os-brain/internal/install"
	"github.com/chka/os-brain/internal/pyfmt"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/textguard"
)

// `share` moves a project between its own file and the shared store, as one command.
//
// Doing it by hand (`absorb` plus an edit of brain.json) goes wrong in three ways, and each
// is worse than usual because the source is the only copy of the project's memory:
//
//   - `absorb` moves memories and events. It does not move history, tombstones, measures,
//     tasks, session_log, leads or limits, and it says nothing about the ones it skipped,
//     so a move that reports success leaves a third of the store behind.
//   - `"shared": true` edited by hand takes effect on the next command, but the shared
//     INDEX does not learn about it until something unrelated runs - the same defect that
//     made `private` a command rather than an edit.
//   - Nothing renames the local file afterwards, so `brain.db` sits beside a config that
//     no longer reads it, one edit away from being silently authoritative again.
//
// The tables are DERIVED, never listed: every table carrying a `project` column moves, and
// anything with rows that does not is named in the output rather than dropped in silence.
// A list in this file would be one more thing to forget on the next schema bump, and the
// failure mode of forgetting is a memory that quietly does not arrive.

type ShareArgs struct {
	Off   bool
	Apply bool
	JSON  bool
}

type tableMove struct {
	Table    string `json:"table"`
	Rows     int    `json:"rows"`
	Skipped  int    `json:"skipped"`
	Unlinked int    `json:"unlinked,omitempty"`
}

// Share moves this project's rows into the shared store, or back out to its own file.
// Without --apply it only reports what would move.
func (s *Store) Share(w io.Writer, a ShareArgs) error {
	want := !a.Off
	shared := fsguard.Realpath(store.SharedDBPath())
	local := fsguard.Realpath(filepath.Join(s.Env.Here, "brain.db"))

	if want == s.Env.Shared {
		return s.reportAlreadyThere(w, a)
	}
	if shared == local {
		return fmt.Errorf("this project's own database IS the shared file (%s); there is "+
			"nothing to move", store.SharedDBPath())
	}

	dstPath := store.SharedDBPath()
	if !want {
		dstPath = filepath.Join(s.Env.Here, "brain.db")
		// Coming back out onto an existing local file - the pre-move copy, usually - the
		// rows merge in and the ones already present are skipped. The merge is
		// identity-safe, so refusing here would protect nothing.
		if _, err := os.Stat(dstPath); err == nil {
			fmt.Fprintf(w, "  %s already exists: merging into it, rows already present are skipped\n", dstPath)
		}
	}

	// Read even on --apply: a file that exists and is not a brain is refused either way.
	dstTables, dstSchema, err := readOnlyProjectTables(dstPath)
	if err != nil {
		return err
	}
	if !a.Apply {
		return s.planShare(w, a, want, dstPath, dstTables, dstSchema)
	}
	return s.applyShare(w, a, want, dstPath, shared)
}

func (s *Store) reportAlreadyThere(w io.Writer, a ShareArgs) error {
	if s.Env.Shared {
		return store.Out(w, a.JSON, fmt.Sprintf("%s is already on the shared store\n  %s",
			pyfmt.Repr(s.Env.Project), store.SharedDBPath()),
			map[string]interface{}{"project": s.Env.Project, "shared": true, "changed": false})
	}
	return store.Out(w, a.JSON, fmt.Sprintf("%s already has its own database\n  %s",
		pyfmt.Repr(s.Env.Project), s.Env.DBPath),
		map[string]interface{}{"project": s.Env.Project, "shared": false, "changed": false})
}

func (s *Store) shareHead(want bool, dstPath string) string {
	dir := "into the shared store"
	if !want {
		dir = "out to its own database"
	}
	return fmt.Sprintf("move %s %s\n  from %s\n  to   %s",
		pyfmt.Repr(s.Env.Project), dir, s.Env.DBPath, dstPath)
}

// planShare is the dry run. Opening a store CREATES and MIGRATES it, and a plan may do
// neither: against a missing file an open leaves an empty database behind, which then makes
// the real run refuse, and against an existing one it silently moves the shared store
// forward a schema version. So the plan works from the destination's table list read
// through a read-only handle (dstTables), or, if there is no destination yet, from this
// store: a file this binary is about to create has every table this binary knows.
func (s *Store) planShare(w io.Writer, a ShareArgs, want bool, dstPath string,
	dstTables []string, dstSchema int) error {
	// The plan has to describe what --apply will do, and --apply migrates an older
	// destination first. Reporting the tables it has TODAY named session_log or measures
	// as unmovable and then moved them anyway: a plan that under-reports is as wrong as one
	// that over-reports.
	willMigrate := dstTables != nil && dstSchema < store.SchemaVersion
	var err error
	switch {
	case willMigrate:
		if dstTables, err = s.withOwnTables(dstTables); err != nil {
			return err
		}
	case dstTables == nil:
		if dstTables, err = dump.New(s.Store).ProjectTables(); err != nil {
			return err
		}
	}

	moves, unmoved, err := s.planMove(dstTables)
	if err != nil {
		return err
	}
	total := sumRows(moves)
	lines := []string{"DRY RUN — nothing has moved. " + s.shareHead(want, dstPath), ""}
	if willMigrate {
		lines = append(lines, fmt.Sprintf("  the destination is on schema v%d and this "+
			"move migrates it to v%d\n  (that is a write to a file other projects "+
			"share, and it happens on --apply, not now)", dstSchema, store.SchemaVersion), "")
	}
	lines = append(lines, moveLines(moves, unmoved, total)...)
	lines = append(lines, "", "Run again with --apply to do it.")
	return store.Out(w, a.JSON, strings.Join(lines, "\n"), map[string]interface{}{
		"project": s.Env.Project, "to": dstPath, "from": s.Env.DBPath,
		"tables": moves, "unmoved": unmoved, "rows": total, "applied": false})
}

// withOwnTables adds this store's project tables to dstTables: what a destination migrated
// to this binary's schema will have.
func (s *Store) withOwnTables(dstTables []string) ([]string, error) {
	mine, err := dump.New(s.Store).ProjectTables()
	if err != nil {
		return nil, err
	}
	for _, t := range mine {
		if !store.Contains(dstTables, t) {
			dstTables = append(dstTables, t)
		}
	}
	sort.Strings(dstTables)
	return dstTables, nil
}

func sumRows(moves []tableMove) int {
	total := 0
	for _, m := range moves {
		total += m.Rows
	}
	return total
}

// applyShare does the move: back up, copy in one transaction, switch the config, tell the
// index, then set the old file aside (moving in) or delete the rows left behind (moving out).
func (s *Store) applyShare(w io.Writer, a ShareArgs, want bool, dstPath, shared string) error {
	dstEnv := s.Env
	dstEnv.DBPath = fsguard.Realpath(dstPath)
	dstEnv.Shared = dstEnv.DBPath == shared
	dst, err := open(dstEnv)
	if err != nil {
		return fmt.Errorf("cannot open the destination %s: %w", dstPath, err)
	}
	defer dst.DB.Close()
	dstTables, err := dump.New(dst.Store).ProjectTables()
	if err != nil {
		return err
	}
	moves, unmoved, err := s.planMove(dstTables)
	if err != nil {
		return err
	}
	total := sumRows(moves)

	addedIgnore, err := install.EnsureIgnored(s.Env.ProjectRoot(), filepath.Base(s.Env.Here))
	if err != nil {
		return err
	}
	stamp := time.Now().Format("20060102-150405")
	dumpPath, err := s.backupBeforeMove(stamp)
	if err != nil {
		return err
	}

	applied, err := s.applyMove(dst, dstTables)
	if err != nil {
		return fmt.Errorf("nothing was committed; the source is untouched (backup at %s): %w",
			dumpPath, err)
	}

	cfg := s.Env.Config
	cfg.Shared = want
	if err := store.SaveConfig(s.Env.Here, cfg); err != nil {
		return err
	}
	// Immediately, like `private`: the index is what other projects consult, and a
	// declaration nothing has read yet has changed nothing.
	dst.Env.Config = cfg
	dst.RegisterProject()

	lines := []string{s.shareHead(want, dstPath), ""}
	lines = append(lines, moveLines(applied, unmoved, total)...)
	lines = append(lines, "", "backup (this project's rows only, mode 600): "+dumpPath)

	kept := ""
	if want {
		var after []string
		if kept, after, err = s.setAsideSource(stamp, addedIgnore); err != nil {
			return err
		}
		lines = append(lines, after...)
	} else {
		line, err := s.removeFromShared(dstPath)
		if err != nil {
			return err
		}
		lines = append(lines, line)
	}

	return store.Out(w, a.JSON, strings.Join(lines, "\n"), map[string]interface{}{
		"project": s.Env.Project, "shared": want, "changed": true,
		"from": s.Env.DBPath, "to": dstPath, "tables": applied, "unmoved": unmoved,
		"backup": dumpPath, "kept": kept, "applied": true})
}

// backupBeforeMove writes this project's rows to a JSON file beside brain.json. The dump is
// project-filtered rather than a copy of the file: on the way out the source is shared, and
// a whole-file dump would write every other project's memories into this project's
// directory in cleartext, where the next `git add -A` finds them.
func (s *Store) backupBeforeMove(stamp string) (string, error) {
	dumpPath := filepath.Join(s.Env.Here, "brain-move-backup-"+stamp+".json")
	dumpOut := fsguard.Dump{Path: dumpPath, Root: s.Env.ProjectRoot(), Marker: dump.BackupMarker}
	if err := dumpOut.Check(); err != nil {
		return "", err
	}
	if _, err := dump.New(s.Store).DumpProject(dumpOut); err != nil {
		return "", fmt.Errorf("refusing to move without a backup: %w", err)
	}
	return dumpPath, nil
}

// setAsideSource renames the project's own file once its rows are in the shared store. The
// file is the only unedited copy of what was here, and it is what a verification pass
// compares against, so it is renamed rather than deleted, and renamed rather than left as
// brain.db, which a config edit would make live again. It returns the kept path ("" when the
// rename failed, which is a warning, not an error) and the lines to report.
func (s *Store) setAsideSource(stamp, addedIgnore string) (string, []string, error) {
	kept := s.Env.DBPath + "-premove-" + stamp
	// Checkpoint and close FIRST. Renaming a database out from under an open connection and
	// then removing its -wal discards any frame not yet folded into the main file, which is
	// a corrupt copy of the one thing this rename exists to preserve. After a TRUNCATE
	// checkpoint the sidecars hold nothing.
	if _, err := s.DB.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return "", nil, fmt.Errorf("the move is committed, but the old store could not be "+
			"checkpointed before being set aside: %w", err)
	}
	if err := s.DB.Close(); err != nil {
		return "", nil, fmt.Errorf("the move is committed, but the old store could not be "+
			"closed before being set aside: %w", err)
	}
	// A hard link and then the remove, never a rename: a rename replaces whatever is
	// already at the kept name.
	err := os.Link(s.Env.DBPath, kept)
	if err == nil {
		err = os.Remove(s.Env.DBPath)
	}
	if err != nil {
		diag.Warn("could not rename %s: %v — it is still there, and a brain.json without "+
			"\"shared\" would read it again", s.Env.DBPath, err)
		return "", nil, nil
	}
	for _, side := range []string{"-wal", "-shm"} {
		_ = os.Remove(s.Env.DBPath + side)
	}
	lines := []string{"the store it came from is kept at: " + kept,
		"  nothing reads it now. Verify against it, then delete it."}
	if addedIgnore != "" {
		lines = append(lines, uncommittedIgnoreWarning(addedIgnore)...)
	}
	return kept, lines, nil
}

// uncommittedIgnoreWarning is said when the move had to add .gitignore lines. The backup and
// the kept file are every memory in cleartext, and what keeps them out of git is a
// .gitignore edit this command just made: uncommitted, and easy for a session that did not
// run the move to read as stray noise and discard. The protection is real right now and one
// `git checkout .gitignore` from not being.
func uncommittedIgnoreWarning(addedIgnore string) []string {
	return []string{"",
		"THE TWO FILES ABOVE ARE THIS PROJECT'S WHOLE MEMORY IN CLEARTEXT.",
		"  What keeps them out of git is the .gitignore change this command just",
		"  made (" + addedIgnore + "), and that change is NOT COMMITTED.",
		"  Commit .gitignore, or delete both files now. Do not discard that hunk:",
		"  it reads as unrelated noise and it is the only thing standing between",
		"  these files and the next `git add -A`."}
}

// removeFromShared deletes this project's rows from the shared store once they have been
// copied out, leaving every other project's rows alone.
func (s *Store) removeFromShared(dstPath string) (string, error) {
	tables, err := dump.New(s.Store).ProjectTables()
	if err != nil {
		return "", err
	}
	deleted, err := dump.New(s.Store).PurgeProject(tables, nil)
	if err != nil {
		return "", fmt.Errorf("the rows were copied out to %s but could not be removed "+
			"from the shared store: %w", dstPath, err)
	}
	return fmt.Sprintf("removed %d rows from the shared store; "+
		"other projects there are untouched", dump.SumCounts(deleted)), nil
}

func moveLines(moves []tableMove, unmoved []string, total int) []string {
	var lines []string
	if total == 0 {
		lines = append(lines, "  nothing to move: this project has no rows here")
	}
	for _, m := range moves {
		var notes []string
		if m.Skipped > 0 {
			notes = append(notes, fmt.Sprintf("%d already there", m.Skipped))
		}
		if m.Unlinked > 0 {
			// Dangling in the source too: the memory they belonged to was forgotten
			// before the move. Said out loud, because a count that does not add up is
			// the thing a verification pass will stop on.
			notes = append(notes, fmt.Sprintf("%d no longer linked to a memory", m.Unlinked))
		}
		note := ""
		if len(notes) > 0 {
			note = " (" + strings.Join(notes, ", ") + ")"
		}
		lines = append(lines, fmt.Sprintf("  %-16s %4d%s", m.Table, m.Rows, note))
	}
	if len(unmoved) > 0 {
		// Named, not dropped quietly. A table with rows and no project column cannot be
		// filed under this project on the other side, and the caller is the only one who
		// can say whether that matters.
		lines = append(lines, "", "NOT moved (no project column, so these rows belong to "+
			"the file rather than to this project):")
		for _, u := range unmoved {
			lines = append(lines, "  "+u)
		}
	}
	return lines
}

// movableTables is every table in BOTH stores that carries a project column, with
// utterances and memories first because the rest reference their ids. unmoved names each
// table that cannot move, and why.
func (s *Store) movableTables(dstTables []string) (move []string, unmoved []string, err error) {
	srcTables, err := dump.New(s.Store).ProjectTables()
	if err != nil {
		return nil, nil, err
	}
	var rest []string
	for _, t := range srcTables {
		if !store.Contains(dstTables, t) {
			unmoved = append(unmoved, t+" (the destination has no such table)")
			continue
		}
		if t != "utterances" && t != "memories" {
			rest = append(rest, t)
		}
	}
	fileTables, err := s.tablesWithoutProject(srcTables)
	if err != nil {
		return nil, nil, err
	}
	unmoved = append(unmoved, fileTables...)
	sort.Strings(rest)
	sort.Strings(unmoved)
	for _, t := range []string{"utterances", "memories"} {
		if store.Contains(srcTables, t) && store.Contains(dstTables, t) {
			move = append(move, t)
		}
	}
	return append(move, rest...), unmoved, nil
}

// tablesWithoutProject names each table that has rows but no project column. It holds
// something belonging to the FILE, not to this project (the legacy local `notes` table is
// the live example); the caller is the only one who can decide whether it matters.
func (s *Store) tablesWithoutProject(projectTables []string) ([]string, error) {
	all, err := s.TableNames()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, t := range all {
		if store.Contains(projectTables, t) || strings.Contains(t, "_fts") || t == "meta" ||
			strings.HasPrefix(t, "sqlite_") {
			continue
		}
		var n int
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM " + store.QuoteIdent(t)).Scan(&n); err == nil && n > 0 {
			out = append(out, fmt.Sprintf("%s (%d rows, no project column)", t, n))
		}
	}
	return out, nil
}

func tableColumnsOf(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query("PRAGMA table_info(" + store.QuoteIdent(table) + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols = append(cols, name)
	}
	return cols, rows.Err()
}

// planMove counts this project's rows in each table that would move.
func (s *Store) planMove(dstTables []string) ([]tableMove, []string, error) {
	tables, unmoved, err := s.movableTables(dstTables)
	if err != nil {
		return nil, nil, err
	}
	var moves []tableMove
	for _, t := range tables {
		var n int
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM "+store.QuoteIdent(t)+" WHERE project=?",
			s.Env.Project).Scan(&n); err != nil {
			return nil, nil, err
		}
		if n > 0 {
			moves = append(moves, tableMove{Table: t, Rows: n})
		}
	}
	return moves, unmoved, nil
}

// moveRun is one applyMove: the transaction on the destination and the id maps that let
// later rows follow the earlier rows they point at.
type moveRun struct {
	src, dst *Store
	tx       *sql.Tx
	// memMap and uttMap map a source id to the id the same row got in the destination.
	memMap map[int64]int64
	uttMap map[int64]int64
	// supersessions are (source id, source superseded_by) pairs, linked once every memory
	// is in, because the row a memory points at may not be inserted yet.
	supersessions [][2]int64
}

// applyMove copies every project-scoped row into dst in one transaction, remapping the
// ids that other rows point at. Memories are matched on their content hash, so a move
// interrupted halfway is safe to run again: what is already there is skipped, not doubled.
func (s *Store) applyMove(dst *Store, dstTables []string) ([]tableMove, error) {
	tables, _, err := s.movableTables(dstTables)
	if err != nil {
		return nil, err
	}
	dstCols := map[string][]string{}
	for _, t := range tables {
		cols, err := tableColumnsOf(dst.DB, t)
		if err != nil {
			return nil, err
		}
		dstCols[t] = cols
	}

	tx, err := dst.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	run := &moveRun{src: s, dst: dst, tx: tx, memMap: map[int64]int64{}, uttMap: map[int64]int64{}}
	var moves []tableMove
	for _, t := range tables {
		mv, err := run.moveTable(t, dstCols[t])
		if err != nil {
			return nil, err
		}
		if mv.Rows > 0 || mv.Skipped > 0 {
			moves = append(moves, mv)
		}
	}
	if err := run.linkSupersessions(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return moves, nil
}

// insertColumns is the columns to copy: the intersection of source and destination, not the
// destination's columns. A source on an older schema has fewer, and naming a column it does
// not have binds NULL into a NOT NULL column and fails the whole move - every project older
// than the shared store, which is all of them the first time. Omitted columns take the
// destination's own default, which is what a migration would have given them. The id is
// never copied: the destination assigns its own.
func insertColumns(table string, dstCols []string, srcRow map[string]interface{}) []string {
	var cols []string
	for _, c := range dstCols {
		if _, ok := srcRow[c]; c == "id" || !ok {
			continue
		}
		cols = append(cols, c)
	}
	if table == "memories" && !store.Contains(cols, "hash") && store.Contains(dstCols, "hash") {
		// Identity is the content hash, and it is recomputed for the new project label
		// rather than carried. Without it the row is stored unaddressable.
		cols = append(cols, "hash")
	}
	return cols
}

func (r *moveRun) moveTable(t string, dstCols []string) (tableMove, error) {
	mv := tableMove{Table: t}
	src, err := rowsRaw(r.src.DB, "SELECT * FROM "+store.QuoteIdent(t)+" WHERE project=? ORDER BY rowid",
		r.src.Env.Project)
	if err != nil || len(src) == 0 {
		return mv, err
	}
	hasID := store.Contains(dstCols, "id")
	cols := insertColumns(t, dstCols, src[0])
	stmt := fmt.Sprintf("INSERT INTO %s(%s) VALUES(%s)", store.QuoteIdent(t),
		strings.Join(store.QuoteAll(cols), ","), strings.TrimSuffix(strings.Repeat("?,", len(cols)), ","))

	for _, row := range src {
		oldID := i64of(row["id"])
		var mem memoryMove
		if t == "memories" {
			if mem, err = r.prepareMemory(row, oldID); err != nil {
				return mv, err
			}
			if mem.skip {
				mv.Skipped++
				continue
			}
		}
		r.relinkToMemory(t, row, &mv)

		res, err := r.tx.Exec(stmt, r.insertArgs(cols, row)...)
		if err != nil {
			// measure_limits and session_log have composite keys; a re-run finds them
			// already there rather than failing the whole move.
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				mv.Skipped++
				continue
			}
			return mv, fmt.Errorf("%s: %w", t, err)
		}
		if hasID {
			if err := r.recordInsert(t, row, oldID, mem, res); err != nil {
				return mv, err
			}
		}
		mv.Rows++
	}
	return mv, nil
}

// memoryMove is what prepareMemory decided about one memory row.
type memoryMove struct {
	skip      bool   // the same memory is already in the destination
	retracted bool   // it is superseded, and its hash is rewritten after the insert
	liveHash  string // its content hash under this project's label
}

// prepareMemory rewrites one memory row for the destination: its hash, its utterance link,
// and its superseded_by, which is cleared and linked at the end.
func (r *moveRun) prepareMemory(row map[string]interface{}, oldID int64) (memoryMove, error) {
	h := store.ContentHash(r.src.Env.Project, dump.JSONStr(row["scope"], "global"), dump.JSONStr(row["title"], ""))
	mem := memoryMove{liveHash: h, retracted: i64of(row["superseded_by"]) != 0}
	// A RETRACTED memory does not hold its own identity: `remember --supersedes` hands
	// scope+title to the successor and rehashes the old row from its id. Recomputing the
	// content hash here undoes that, so a correction stored under the same title as the fact
	// it corrects arrived as a duplicate of a row already inserted, was skipped, and the store
	// kept the RETRACTED half, with its superseded_by remapped onto itself.
	//
	// The retracted hash is derived from the new id, which does not exist until the insert,
	// so the row goes in under a per-row placeholder and is rewritten in recordInsert. The
	// placeholder is unique because the source id is.
	if mem.retracted {
		row["hash"] = fmt.Sprintf("moving:%d:%s", oldID, h)
	} else {
		row["hash"] = h
		exists, err := r.sameMemoryInDestination(row, oldID, h)
		if err != nil {
			return mem, err
		}
		if exists != 0 {
			r.memMap[oldID] = exists
			mem.skip = true
			return mem, nil
		}
	}
	if u := i64of(row["from_utterance"]); u != 0 {
		if n, ok := r.uttMap[u]; ok {
			row["from_utterance"] = n
		} else {
			row["from_utterance"] = nil
		}
	}
	if sb := i64of(row["superseded_by"]); sb != 0 {
		row["superseded_by"] = nil
		r.supersessions = append(r.supersessions, [2]int64{oldID, sb})
	}
	return mem, nil
}

// sameMemoryInDestination returns the id of a destination memory with the same identity, or
// 0. Identical text is a re-run of an interrupted move and is skipped; different text is two
// answers to one question, and choosing one by insert order is the widest wrong action, so
// it refuses.
func (r *moveRun) sameMemoryInDestination(row map[string]interface{}, oldID int64, h string) (int64, error) {
	var exists int64
	var body string
	err := r.tx.QueryRow("SELECT id, body FROM memories WHERE hash=?", h).Scan(&exists, &body)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if body != dump.JSONStr(row["body"], "") {
		return 0, fmt.Errorf("memory %d (%s) in %s has the same scope "+
			"and title as #%d already in the destination, but a different "+
			"body. Moving it would silently keep one of the two.\n"+
			"Resolve it first: `os-brain update %d` to merge the wording, "+
			"or retitle one of them, then run the move again",
			oldID, textguard.Clip(dump.JSONStr(row["title"], ""), 60),
			dump.JSONStr(row["scope"], "global"), exists, exists)
	}
	return exists, nil
}

// relinkToMemory points an archived version or an objection at its memory's new id. One
// whose memory did not come across is carried with its link cleared rather than dropped.
// Keeping the old id would point it at whatever row holds that number on the other side -
// somebody else's memory - and dropping it would destroy the archived body text, which is
// the only copy of a memory that was already forgotten. Both commands that read these tables
// filter by project and list by their own id, so an unlinked row stays visible and
// restorable.
func (r *moveRun) relinkToMemory(table string, row map[string]interface{}, mv *tableMove) {
	col := map[string]string{"mem_history": "mem_id", "objections": "memory_id"}[table]
	if col == "" {
		return
	}
	if n, ok := r.memMap[i64of(row[col])]; ok {
		row[col] = n
	} else {
		row[col] = 0
		mv.Unlinked++
	}
}

// insertArgs is the row's values for cols, with the project column set to this project.
func (r *moveRun) insertArgs(cols []string, row map[string]interface{}) []interface{} {
	args := make([]interface{}, len(cols))
	for i, c := range cols {
		if c == "project" {
			args[i] = r.src.Env.Project
			continue
		}
		args[i] = row[c]
	}
	return args
}

// recordInsert notes the new id of a memory or utterance so later rows can follow it, and
// finishes a memory: its final hash when retracted, and its search entry.
func (r *moveRun) recordInsert(table string, row map[string]interface{}, oldID int64,
	mem memoryMove, res sql.Result) error {
	newID, err := res.LastInsertId()
	if err != nil {
		return err
	}
	switch table {
	case "memories":
		r.memMap[oldID] = newID
		if mem.retracted {
			if _, err := r.tx.Exec("UPDATE memories SET hash=? WHERE id=?",
				store.SupersededHash(newID, mem.liveHash), newID); err != nil {
				return err
			}
		}
		return r.dst.FTSPut(r.tx, newID, dump.JSONStr(row["title"], ""), dump.JSONStr(row["body"], ""))
	case "utterances":
		r.uttMap[oldID] = newID
	}
	return nil
}

// linkSupersessions restores every superseded_by link under the new ids. It refuses rather
// than land a retraction that supersedes nothing: a correction sitting beside the error it
// retracted, at equal weight, is the one failure this store is built to make impossible.
func (r *moveRun) linkSupersessions() error {
	for _, pair := range r.supersessions {
		self, ok1 := r.memMap[pair[0]]
		next, ok2 := r.memMap[pair[1]]
		if !ok1 || !ok2 {
			return fmt.Errorf("memory %d supersedes %d, which did not come across; "+
				"moving it would republish a fact that was retracted on purpose", pair[0], pair[1])
		}
		if _, err := r.tx.Exec("UPDATE memories SET superseded_by=? WHERE id=?", next, self); err != nil {
			return err
		}
	}
	return nil
}

// rowsRaw reads rows keeping SQLite's own types (text as string). rowsFrom widens every
// integer to float64 for JSON, which is right for a dump and wrong for re-inserting a row.
func rowsRaw(db *sql.DB, q string, args ...interface{}) ([]map[string]interface{}, error) {
	return scanRowMaps(db, false, q, args...)
}

// scanRowMaps reads every row of q as a column-name map. Text comes back as a string; with
// widenInts, integers come back as float64, the type JSON decoding would give them.
func scanRowMaps(db *sql.DB, widenInts bool, q string, args ...interface{}) ([]map[string]interface{}, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]interface{}{}
	for rows.Next() {
		vals := make([]interface{}, len(names))
		ptrs := make([]interface{}, len(names))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		rec := map[string]interface{}{}
		for i, n := range names {
			switch v := vals[i].(type) {
			case []byte:
				rec[n] = string(v)
			case int64:
				if widenInts {
					rec[n] = float64(v)
				} else {
					rec[n] = v
				}
			default:
				rec[n] = v
			}
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func i64of(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}

// readOnlyProjectTables lists the destination's project-scoped tables and schema version
// without opening it as a store, which would create it and migrate it forward. nil means
// there is no file.
func readOnlyProjectTables(path string) ([]string, int, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, 0, nil
	}
	db, err := sql.Open("sqlite", store.SQLiteURI(path, "mode=ro"))
	if err != nil {
		return nil, 0, fmt.Errorf("cannot read the destination %s: %w", path, err)
	}
	defer db.Close()
	version := 0
	_ = db.QueryRow("SELECT v FROM meta WHERE k='schema_version'").Scan(&version)
	names, err := userTableNames(db, path)
	if err != nil {
		return nil, 0, err
	}
	var out []string
	for _, t := range names {
		cols, err := tableColumnsOf(db, t)
		if err != nil {
			return nil, 0, err
		}
		if store.Contains(cols, "project") {
			out = append(out, t)
		}
	}
	if out == nil {
		// An existing file with no project-scoped table at all is not a brain, and
		// answering "nothing moves" would read as a clean plan.
		return nil, 0, fmt.Errorf("%s exists but holds no project-scoped table; it is not a brain", path)
	}
	return out, version, nil
}

// userTableNames is every table of the database at path except SQLite's own and the
// full-text shadow tables.
func userTableNames(db *sql.DB, path string) ([]string, error) {
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' " +
		"AND name NOT LIKE 'sqlite_%' AND name NOT LIKE '%_fts%' ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("cannot read the destination %s: %w", path, err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}
