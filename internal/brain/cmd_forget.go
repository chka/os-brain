package brain

import (
	"database/sql"
	"fmt"
	"io"
	"strings"
)

type ForgetArgs struct {
	ID           int64
	Reason       string
	Force        bool
	All          bool
	Everything   bool
	Apply        bool
	IMeanIt      bool
	Backup       string
	AllowOutside bool
	AllowTracked bool
	JSON         bool
	// Purge destroys rather than archives: no version is written and every version already
	// held is deleted, so nothing can restore it. For a secret that should never have been
	// stored, which is the one case where archiving is the wrong half of forget. It stands
	// on its own: typing it is the decision, and it carries force with it.
	Purge bool
}

// dependant is a memory superseded by the one being forgotten.
type dependant struct {
	id    int64
	title string
}

// Forget deletes a memory and leaves a tombstone, so a wrong fact is not relearned on a
// loop. It refuses pinned memories and guardrails without --force, and refuses to delete
// the head of a supersession chain: nulling those links republishes facts that were
// retracted on purpose.
func (s *Store) Forget(w io.Writer, a ForgetArgs) error {
	if a.All || a.Everything {
		return s.ForgetAll(w, a)
	}
	m, err := s.Own(a.ID, "delete")
	if err != nil {
		return err
	}
	if a.Purge {
		a.Force = true
	}
	if err := refuseLoadBearing(a, m); err != nil {
		return err
	}
	deps, err := s.dependants(a.ID)
	if err != nil {
		return err
	}
	if err := refuseResurrection(a, m, deps); err != nil {
		return err
	}
	if err := s.deleteMemory(a, m); err != nil {
		return err
	}
	human := fmt.Sprintf("forgot #%d [%s/%s] %s", a.ID, OneLine(m.Scope), m.MemType, OneLine(m.Title))
	if len(deps) > 0 {
		target := "nothing"
		if m.SupersededBy != nil {
			target = fmt.Sprintf("#%d", *m.SupersededBy)
		}
		human += fmt.Sprintf("\n  %d superseded memories now point at %s", len(deps), target)
	}
	if a.Purge {
		human += "\n  purged: no version was kept and nothing can restore it"
	} else {
		human += fmt.Sprintf("\n  recoverable with: os-brain history --id %d / os-brain restore <history-id>", a.ID)
	}
	ids := make([]int64, len(deps))
	for i, d := range deps {
		ids[i] = d.id
	}
	return Out(w, a.JSON, human, map[string]interface{}{
		"id": a.ID, "deleted": true, "title": m.Title, "dependants": ids})
}

// refuseLoadBearing keeps pinned memories and guardrails from being deleted by accident.
func refuseLoadBearing(a ForgetArgs, m *Memory) error {
	if a.Force || (m.Pinned != 1 && !contains(GuardrailTypes, m.MemType)) {
		return nil
	}
	what := m.MemType
	if m.Pinned == 1 {
		what = "pinned"
	}
	return fmt.Errorf("#%d is %s: %s\nLoad-bearing memories are not deleted by accident. "+
		"Re-read it (os-brain get %d), then use --force, or supersede it instead.",
		a.ID, what, Repr(m.Title), a.ID)
}

func (s *Store) dependants(id int64) ([]dependant, error) {
	rows, err := s.DB.Query("SELECT id, title FROM memories WHERE superseded_by=?", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var deps []dependant
	for rows.Next() {
		var d dependant
		if err := rows.Scan(&d.id, &d.title); err != nil {
			return nil, err
		}
		deps = append(deps, d)
	}
	return deps, nil
}

// refuseResurrection refuses to delete the live head of a supersession chain: the memories
// it superseded would come back into recall as live facts.
func refuseResurrection(a ForgetArgs, m *Memory, deps []dependant) error {
	if len(deps) == 0 || m.SupersededBy != nil || a.Force {
		return nil
	}
	var listing []string
	for _, d := range deps {
		listing = append(listing, fmt.Sprintf("  #%d %s", d.id, d.title))
	}
	suffix := "ies"
	if len(deps) == 1 {
		suffix = "y"
	}
	return fmt.Errorf("#%d is the current version of %d superseded memor%s. Deleting it "+
		"would put these back into recall as live facts:\n%s\nSupersede it with a correct "+
		"version instead, or pass --force to accept the resurrection.",
		a.ID, len(deps), suffix, strings.Join(listing, "\n"))
}

// deleteMemory archives (or, with --purge, destroys every version of) the memory, deletes
// it, splices the supersession chain around it and leaves the tombstone, in one
// transaction.
func (s *Store) deleteMemory(a ForgetArgs, m *Memory) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.keepOrPurgeHistory(tx, a, m); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM memories WHERE id=?", a.ID); err != nil {
		return err
	}
	if err := s.ftsDel(tx, a.ID); err != nil {
		return err
	}
	// Splice the chain rather than cutting it: dependants inherit the deleted row's own
	// successor, so they stay superseded by whatever superseded it.
	if _, err := tx.Exec("UPDATE memories SET superseded_by=? WHERE superseded_by=?",
		m.SupersededBy, a.ID); err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT OR REPLACE INTO tombstones(hash,project,scope,title,reason,"+
		"forgotten_at) VALUES(?,?,?,?,?,?)",
		tombstoneHash(s.Env.Project, m), s.Env.Project, m.Scope, m.Title,
		a.Reason, Now()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) keepOrPurgeHistory(tx *sql.Tx, a ForgetArgs, m *Memory) error {
	if a.Purge {
		_, err := tx.Exec("DELETE FROM mem_history WHERE mem_id=?", a.ID)
		return err
	}
	return s.archive(tx, m, "forget", a.Reason)
}
