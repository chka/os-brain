package memory

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/textguard"
	"github.com/chka/os-brain/internal/timefmt"
)

// The ladder: repetition is authority.
//
//	stated   said once: a position, open to discussion.
//	hardened restated AFTER an objection was recorded against it. The objection keeps
//	         its date and its reasoning, which is what stops the third session
//	         reconstructing an argument that has already been won twice.
//	law      restated in a DIFFERENT session from the one that last stated it. Always
//	         injected; only the user can reopen it.
//
// Every transition here is DERIVED from what the store can already show (stated_count, the
// session column already on every memory, and rows in objections), never hand-set.
// Nothing exported lets a caller write hardened_at or law_at directly; the only door in
// is AdvanceLadder, and it only ever moves a null column to non-null (COALESCE), so the
// first time a state is reached is the only time it can be reached.

// Objection is one recorded row against a memory. why and raised_at are the two things
// that make a restatement worth anything: without them "we discussed this" is a claim
// nobody can check, and the third session re-runs the argument from nothing.
type Objection struct {
	ID         int64  `json:"id"`
	MemoryID   int64  `json:"memory_id"`
	Why        string `json:"why"`
	RaisedAt   int64  `json:"raised_at"`
	Session    string `json:"session"`
	AnsweredAt *int64 `json:"answered_at,omitempty"`
	Answer     string `json:"answer,omitempty"`
}

type ladderRow struct {
	session     string
	statedCount int
	hardenedAt  sql.NullInt64
	lawAt       sql.NullInt64
}

func (s *Store) ladderRow(id int64) (*ladderRow, error) {
	var r ladderRow
	err := s.DB.QueryRow(
		"SELECT session, stated_count, hardened_at, law_at FROM memories WHERE id=?", id).
		Scan(&r.session, &r.statedCount, &r.hardenedAt, &r.lawAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no memory #%d", id)
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (r *ladderRow) state() string {
	switch {
	case r.lawAt.Valid:
		return "law"
	case r.hardenedAt.Valid:
		return "hardened"
	default:
		return "stated"
	}
}

func ladderState(r *ladderRow) *LadderState {
	ls := &LadderState{State: r.state(), StatedCount: r.statedCount}
	if r.hardenedAt.Valid {
		v := r.hardenedAt.Int64
		ls.HardenedAt = &v
	}
	if r.lawAt.Valid {
		v := r.lawAt.Int64
		ls.LawAt = &v
	}
	return ls
}

// objectionCount is what "an objection was recorded" reads as: any row raised against
// this memory, answered or not. Whether it was later answered does not undo the fact
// that arguing it cost an exchange: that is the whole point of the rule.
func (s *Store) objectionCount(id int64) (int, error) {
	var n int
	err := s.DB.QueryRow("SELECT COUNT(*) FROM objections WHERE memory_id=?", id).Scan(&n)
	return n, err
}

// ladderTransitions answers the two questions the ladder asks, given the facts:
//
// hardened: an objection is already on record and this restatement is the first one since.
// law: the session restating it now is not the session that last stated it. Same-session
// repetition is a person saying the same sentence twice, not a second mind agreeing, which
// is the mistake this rule exists to catch.
//
// Hardening is a step BELOW law, so a memory that is already law cannot take it. Checking
// only that hardened_at was unset let an objection plus a restatement stamp hardened_at
// LATER than law_at: a timestamp asserting a transition that never happened, on a ladder
// whose whole claim is that each state is reached once and in order. The objection is still
// recorded; what it cannot do is rewrite the history of how this became law.
//
// An empty session never promotes to law. Compared against the stored session, "" would
// always look different, and a memory would become law on the strength of a missing id.
func ladderTransitions(hardenedSet, lawSet bool, objections int,
	lastSession, nowSession string) (hardened, law bool) {
	hardened = !hardenedSet && !lawSet && objections > 0
	law = !lawSet && lastSession != "" && strings.TrimSpace(nowSession) != "" &&
		!store.SameSession(lastSession, nowSession)
	return hardened, law
}

// AdvanceLadder is called when a memory is re-stated. It never creates a memory and never
// lowers a state; it only fills stated_count, last_stated_at and, the first time each
// becomes true, hardened_at and/or law_at.
func (s *Store) AdvanceLadder(id int64, session string) (*LadderState, error) {
	if _, err := s.Own(id, "advance the ladder on"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(session) == "" {
		session = s.Env.SessionID()
	}
	before, err := s.ladderRow(id)
	if err != nil {
		return nil, err
	}
	objections, err := s.objectionCount(id)
	if err != nil {
		return nil, err
	}
	now := timefmt.Now()

	becomesHardened, becomesLaw := ladderTransitions(before.hardenedAt.Valid,
		before.lawAt.Valid, objections, before.session, session)

	set := "stated_count = stated_count + 1, last_stated_at = ?, session = ?"
	args := []interface{}{now, session}
	if becomesHardened {
		set += ", hardened_at = COALESCE(hardened_at, ?)"
		args = append(args, now)
	}
	if becomesLaw {
		set += ", law_at = COALESCE(law_at, ?)"
		args = append(args, now)
	}
	args = append(args, id)
	if _, err := s.DB.Exec("UPDATE memories SET "+set+" WHERE id = ?", args...); err != nil {
		return nil, err
	}

	after, err := s.ladderRow(id)
	if err != nil {
		return nil, err
	}
	ls := ladderState(after)
	switch {
	case becomesLaw:
		ls.Why = "restated in a different session: law. Only the user can reopen it."
	case becomesHardened:
		ls.Why = "restated after a recorded objection: hardened."
	case ls.State == "law" && objections > 0:
		// Said plainly rather than as "law ... hardened", which read as one line
		// contradicting itself.
		ls.Why = "restated: still law, with " + timefmt.Plural(objections, "objection") +
			" on record. Only the user can reopen it."
	default:
		ls.Why = "restated: still " + ls.State + "."
	}
	return ls, nil
}

// LadderOf reads the current state without changing anything.
func (s *Store) LadderOf(id int64) (*LadderState, error) {
	if _, err := s.Own(id, "read"); err != nil {
		return nil, err
	}
	r, err := s.ladderRow(id)
	if err != nil {
		return nil, err
	}
	ls := ladderState(r)
	ls.Why = "current state: " + ls.State + "."
	return ls, nil
}

// Object records an objection against a memory. It never blocks: even a memory that has
// already reached law accepts the objection, because people may still disagree with it;
// what changes is what the objection can DO. On a law-state memory recording it is the
// whole effect; only the user, restating a fresh utterance, can reopen the memory itself.
func (s *Store) Object(id int64, why, session string) (*LadderState, error) {
	why = strings.TrimSpace(why)
	if why == "" {
		return nil, fmt.Errorf("an objection needs a reason: why should #%d be reopened?", id)
	}
	if err := textguard.GuardTextNoOverride(why); err != nil {
		return nil, err
	}
	if _, err := s.Own(id, "object to"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(session) == "" {
		session = s.Env.SessionID()
	}
	if _, err := s.DB.Exec(
		"INSERT INTO objections(project, memory_id, why, raised_at, session) VALUES(?,?,?,?,?)",
		s.Env.Project, id, why, timefmt.Now(), session); err != nil {
		return nil, err
	}
	r, err := s.ladderRow(id)
	if err != nil {
		return nil, err
	}
	ls := ladderState(r)
	if ls.State == "law" {
		ls.Why = fmt.Sprintf("objection recorded, but #%d is law: only the user can reopen it.", id)
	} else {
		ls.Why = "objection recorded. A restatement after this will harden the memory."
	}
	return ls, nil
}

// Objections returns everything recorded against a memory, newest first: the order a
// reader wants: what is being argued right now, before the history of what already was.
func (s *Store) Objections(id int64) ([]Objection, error) {
	if _, err := s.Own(id, "read"); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(
		"SELECT id, memory_id, why, raised_at, session, answered_at, answer "+
			"FROM objections WHERE memory_id = ? ORDER BY raised_at DESC, id DESC", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Objection{}
	for rows.Next() {
		var o Objection
		var answeredAt sql.NullInt64
		if err := rows.Scan(&o.ID, &o.MemoryID, &o.Why, &o.RaisedAt, &o.Session,
			&answeredAt, &o.Answer); err != nil {
			return nil, err
		}
		if answeredAt.Valid {
			v := answeredAt.Int64
			o.AnsweredAt = &v
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
