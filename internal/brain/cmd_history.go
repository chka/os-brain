package brain

import (
	"database/sql"
	"fmt"
	"io"
	"strings"
)

// `history` and `restore`: every update, forget, prune and wipe archives the old version
// of a memory into mem_history first, and these read it back. A backup nobody can read
// back is not a safety net.

type HistoryRow struct {
	ID           int64  `json:"id"`
	Project      string `json:"project"`
	MemID        int64  `json:"mem_id"`
	Scope        string `json:"scope"`
	MemType      string `json:"mem_type"`
	Title        string `json:"title"`
	Body         string `json:"body"`
	Source       string `json:"source"`
	SourceDate   string `json:"source_date"`
	SourceCommit string `json:"source_commit"`
	Confidence   string `json:"confidence"`
	Importance   int    `json:"importance"`
	Pinned       int    `json:"pinned"`
	SupersededBy *int64 `json:"superseded_by"`
	Action       string `json:"action"`
	Reason       string `json:"reason"`
	ArchivedAt   int64  `json:"archived_at"`
}

const historyRowCols = `id, project, mem_id, scope, mem_type, title, body, source,
	source_date, source_commit, confidence, importance, pinned, superseded_by, action,
	reason, archived_at`

func scanHistory(s interface{ Scan(...interface{}) error }) (*HistoryRow, error) {
	var h HistoryRow
	err := s.Scan(&h.ID, &h.Project, &h.MemID, &h.Scope, &h.MemType, &h.Title, &h.Body,
		&h.Source, &h.SourceDate, &h.SourceCommit, &h.Confidence, &h.Importance, &h.Pinned,
		&h.SupersededBy, &h.Action, &h.Reason, &h.ArchivedAt)
	if err != nil {
		return nil, err
	}
	return &h, nil
}

type HistoryArgs struct {
	ID   int64
	N    int
	JSON bool
}

// History lists this project's archived versions, newest first, optionally of one memory.
func (s *Store) History(w io.Writer, a HistoryArgs) error {
	if a.N <= 0 {
		a.N = 20
	}
	where, params := "project=?", []interface{}{s.Env.Project}
	if a.ID > 0 {
		where = "project=? AND mem_id=?"
		params = append(params, a.ID)
	}
	rows, err := s.DB.Query("SELECT "+historyRowCols+" FROM mem_history WHERE "+where+
		// id breaks the tie: two versions archived in the same second once came back oldest
		// first, which reads as the wrong version being the latest.
		" ORDER BY archived_at DESC, id DESC LIMIT ?", append(params, a.N)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	hs := []*HistoryRow{}
	var lines []string
	for rows.Next() {
		h, err := scanHistory(rows)
		if err != nil {
			return err
		}
		hs = append(hs, h)
		lines = append(lines, historyLines(h)...)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	human := "(no history)"
	if len(hs) > 0 {
		lines = append(lines, "\nrestore one with: os-brain restore <history-id>")
		human = strings.Join(lines, "\n")
	}
	return Out(w, a.JSON, human, map[string]interface{}{"history": hs})
}

// historyLines is one archived version's human form: a header line, then the start of its
// body indented beneath it.
func historyLines(h *HistoryRow) []string {
	reason := ""
	if h.Reason != "" {
		reason = "  (" + OneLine(h.Reason) + ")"
	}
	lines := []string{fmt.Sprintf("h%d %s [%s] memory #%d [%s/%s] %s%s",
		h.ID, ISO(h.ArchivedAt), OneLine(h.Action), h.MemID, OneLine(h.Scope), h.MemType,
		OneLine(h.Title), reason)}
	if h.Body != "" {
		lines = append(lines, "      "+strings.ReplaceAll(BodyText(Clip(h.Body, 200)), "\n", "\n      "))
	}
	return lines
}

type RestoreArgs struct {
	HID   int64
	Force bool
	JSON  bool
}

// Restore brings an archived version back: over the memory it came from when that still
// exists (only with --force), or as a new memory when it was deleted.
func (s *Store) Restore(w io.Writer, a RestoreArgs) error {
	row := s.DB.QueryRow("SELECT "+historyRowCols+" FROM mem_history WHERE id=? AND project=?",
		a.HID, s.Env.Project)
	h, err := scanHistory(row)
	if err == sql.ErrNoRows {
		return fmt.Errorf("no history entry h%d in %s", a.HID, s.Env.Project)
	}
	if err != nil {
		return err
	}
	live, err := s.byID(h.MemID, false)
	if err != nil {
		return err
	}
	if live != nil {
		return s.rollBack(w, a, h, live)
	}
	return s.restoreDeleted(w, a, h)
}

// rollBack overwrites a live memory with an archived version of itself. The current
// version is archived first, so the roll-back can itself be rolled back.
func (s *Store) rollBack(w io.Writer, a RestoreArgs, h *HistoryRow, live *Memory) error {
	ts := Now()
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !a.Force {
		return fmt.Errorf("memory #%d still exists: %s\n  current: %s\n  archived: %s\n"+
			"Pass --force to roll it back to the archived version.",
			h.MemID, Repr(live.Title), BodyText(Clip(live.Body, 200)), BodyText(Clip(h.Body, 200)))
	}
	if err := s.archive(tx, live, "restore-overwrite", fmt.Sprintf("restored h%d", a.HID)); err != nil {
		return err
	}
	// The identity hash follows the title and scope, as it does on every other write to
	// them. Rolling a title back without it once left the row unreachable by identity: an
	// import of the same title then added a second row beside it.
	hh := ContentHash(s.Env.Project, h.Scope, h.Title)
	var clash int64
	if err := tx.QueryRow("SELECT id FROM memories WHERE hash=? AND id<>?", hh, h.MemID).Scan(&clash); err == nil {
		return fmt.Errorf("#%d now holds that title in %s; rolling #%d back would merge "+
			"two facts by accident. Merge by hand instead", clash, Repr(h.Scope), h.MemID)
	} else if err != sql.ErrNoRows {
		return err
	}
	if _, err := tx.Exec("UPDATE memories SET title=?, body=?, mem_type=?, scope=?, "+
		"source=?, source_date=?, confidence=?, importance=?, pinned=?, updated_at=?, hash=? "+
		"WHERE id=?",
		h.Title, h.Body, h.MemType, h.Scope, h.Source, h.SourceDate, h.Confidence,
		h.Importance, h.Pinned, ts, hh, h.MemID); err != nil {
		return err
	}
	if err := s.ftsPut(tx, h.MemID, h.Title, h.Body); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return Out(w, a.JSON, fmt.Sprintf("rolled #%d back to h%d", h.MemID, a.HID),
		map[string]interface{}{"id": h.MemID})
}

// restoreDeleted writes an archived version back as a new memory, refusing when another
// memory now holds the same title in the same scope.
func (s *Store) restoreDeleted(w io.Writer, a RestoreArgs, h *HistoryRow) error {
	ts := Now()
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	hh := ContentHash(s.Env.Project, h.Scope, h.Title)
	var clash int64
	if err := tx.QueryRow("SELECT id FROM memories WHERE hash=?", hh).Scan(&clash); err == nil {
		return fmt.Errorf("#%d now holds that title in %s; restore would duplicate it. "+
			"Merge by hand instead", clash, Repr(h.Scope))
	} else if err != sql.ErrNoRows {
		return err
	}
	res, err := tx.Exec("INSERT INTO memories(project,scope,mem_type,title,body,source,"+
		"source_date,source_commit,confidence,importance,pinned,session,hash,created_at,updated_at) "+
		"VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
		s.Env.Project, h.Scope, h.MemType, h.Title, h.Body, h.Source, h.SourceDate,
		h.SourceCommit, h.Confidence, h.Importance, h.Pinned, s.Env.SessionID(), hh, ts, ts)
	if err != nil {
		return err
	}
	newID, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if err := s.ftsPut(tx, newID, h.Title, h.Body); err != nil {
		return err
	}
	// Two identities can carry this fact's tombstone: its name, and, if it was retracted
	// when it was forgotten, the hash derived from its old id. Clearing only the first once
	// left a tombstone nothing could reach, which re-remembering then reported as a
	// forgotten fact.
	if _, err := tx.Exec("DELETE FROM tombstones WHERE hash IN (?,?)",
		hh, supersededHash(h.MemID, hh)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return Out(w, a.JSON, fmt.Sprintf("restored h%d as #%d: %s", a.HID, newID, OneLine(h.Title)),
		map[string]interface{}{"id": newID, "title": h.Title})
}
