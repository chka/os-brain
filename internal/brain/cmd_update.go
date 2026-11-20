package brain

import (
	"database/sql"
	"fmt"
	"io"
	"strings"
)

// columnSet collects the assignments of one UPDATE statement, in the order they are added.
type columnSet struct {
	exprs []string
	vals  []interface{}
}

// set assigns a bound value to a column.
func (c *columnSet) set(col string, v interface{}) {
	c.exprs = append(c.exprs, col+"=?")
	c.vals = append(c.vals, v)
}

// raw adds an assignment that binds nothing, such as "pinned=1".
func (c *columnSet) raw(expr string) {
	c.exprs = append(c.exprs, expr)
}

func (c *columnSet) sql() string {
	return strings.Join(c.exprs, ", ")
}

type UpdateArgs struct {
	ID          int64
	Title       *string
	Body        *string
	Type        *string
	Scope       *string
	Source      *string
	SourceDate  *string
	Confidence  *string
	Importance  *int
	Happened    string
	SnoozeUntil string
	ReviewOn    string
	NoReview    bool
	Refs        []string
	Pin         bool
	Unpin       bool
	Unsupersede bool
	AllowSecret bool
	// Force is only consulted for the one destructive edit here: emptying a body that
	// was not empty.
	Force bool
	JSON  bool
}

// Update edits one memory in place. Every field it accepts goes through the same checks
// remember applies, because an edit that skipped one would be the way round that guard,
// one command later.
func (s *Store) Update(w io.Writer, a UpdateArgs) error {
	m, err := s.Own(a.ID, "edit")
	if err != nil {
		return err
	}
	var cols columnSet
	newScope, newTitle, err := s.updateIdentity(a, m, &cols)
	if err != nil {
		return err
	}

	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := refuseBlankingBody(a, m); err != nil {
		return err
	}
	bodyChanges := a.Body != nil || len(AppendRefs("", a.Refs)) > 0
	if bodyChanges {
		if err := updateBody(a, m, &cols); err != nil {
			return err
		}
	}
	// Archived before a title OR a body changes, once per update. Archiving only on a body
	// change left a title edit as the one overwrite with nothing behind it: `history`
	// showed nothing and `restore` could not bring the old wording back.
	if bodyChanges || (a.Title != nil && newTitle != m.Title) {
		if err := s.archive(tx, m, "update", "os-brain update"); err != nil {
			return err
		}
	}
	newType := m.MemType
	if a.Type != nil {
		if !AllTypes()[*a.Type] {
			return fmt.Errorf("%s", UnknownType(*a.Type))
		}
		newType = *a.Type
		cols.set("mem_type", newType)
	}
	// A rewritten claim is a claim about the tree it was rewritten against; retyping
	// something into a volatile type asserts it about the code for the first time. Metadata
	// edits (importance, pin, scope) touch no claim and leave the stamp alone.
	if (bodyChanges || (a.Type != nil && !VolatileTypes[m.MemType])) && VolatileTypes[newType] {
		if commit := s.headCommit(); commit != "" {
			cols.set("source_commit", commit)
		}
	}
	if a.Importance != nil {
		cols.set("importance", ClampImportance(*a.Importance))
	}
	if err := updateProvenance(a, m, &cols); err != nil {
		return err
	}
	if err := updateDates(a, &cols); err != nil {
		return err
	}
	if err := updatePin(a, &cols); err != nil {
		return err
	}
	if a.Unsupersede {
		if err := unsupersede(tx, a.ID, &cols); err != nil {
			return err
		}
	}
	if len(cols.exprs) == 0 {
		return fmt.Errorf("nothing to update")
	}
	cols.set("updated_at", Now())

	if _, err := tx.Exec("UPDATE memories SET "+cols.sql()+" WHERE id=?",
		append(cols.vals, a.ID)...); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return fmt.Errorf("another memory already holds that title in scope %s; "+
				"merge them instead", Repr(newScope))
		}
		return err
	}
	if err := s.reindex(tx, a.ID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return Out(w, a.JSON, fmt.Sprintf("updated #%d", a.ID),
		map[string]interface{}{"id": a.ID, "updated": true})
}

// updateIdentity applies --title and --scope, and rehashes when either changes, since the
// two of them are a memory's identity. It returns the scope and title the memory will have.
func (s *Store) updateIdentity(a UpdateArgs, m *Memory, cols *columnSet) (string, string, error) {
	newScope, newTitle := m.Scope, m.Title
	if a.Title != nil {
		newTitle = trim(*a.Title)
		if newTitle == "" {
			return "", "", fmt.Errorf("--title cannot be blank")
		}
		if err := CheckLine("--title", newTitle); err != nil {
			return "", "", err
		}
		// remember scans the title for credentials, so update must too, or a refused
		// credential goes in one command later as a retitle.
		if err := GuardText(a.AllowSecret, newTitle); err != nil {
			return "", "", err
		}
		if err := CheckMemorySize(newTitle, ""); err != nil {
			return "", "", err
		}
		cols.set("title", newTitle)
	}
	if a.Scope != nil {
		newScope = *a.Scope
		// The same validation remember does. Without it update was the way round the guard:
		// a scope with a slash, one over the length cap, or the empty string all went in
		// here, and an empty scope orphans a memory in a name nobody can type back and that
		// ':'-nesting cannot match.
		if err := CheckToken(newScope, "scope"); err != nil {
			return "", "", err
		}
		s.checkDeclared(newScope, "scope")
		cols.set("scope", newScope)
	}
	if a.Title != nil || a.Scope != nil {
		cols.set("hash", ContentHash(s.Env.Project, newScope, newTitle))
	}
	return newScope, newTitle, nil
}

// refuseBlankingBody stops an empty --body from erasing a body that holds text. Blanking a
// body is almost never what somebody meant, and it is unrecoverable from the caller's
// side: the text is gone from the row and only mem_history has it. It once happened to a
// memory holding a whole deploy runbook, because a shell command meant to compute the new
// body failed and `--body ""` reached update as an empty string, and update reported
// success.
func refuseBlankingBody(a UpdateArgs, m *Memory) error {
	if a.Body == nil || *a.Body != "" || strings.TrimSpace(m.Body) == "" || a.Force {
		return nil
	}
	return fmt.Errorf("refusing to blank the body of #%d (%s), which holds %d characters.\n"+
		"An empty --body is usually a command that failed to produce the text it meant "+
		"to pass.\nIf you do mean to empty it, pass --force. To retire the fact instead, "+
		"forget it or supersede it with the memory that replaces it", m.ID,
		Repr(Clip(m.Title, 60)), len(m.Body))
}

// updateBody applies --body and --ref. A --ref is appended the way remember appends one,
// and works on its own, so a citation can be added to a memory whose body is already right.
func updateBody(a UpdateArgs, m *Memory, cols *columnSet) error {
	newBody := m.Body
	if a.Body != nil {
		newBody = *a.Body
	}
	newBody = AppendRefs(newBody, a.Refs)
	if err := GuardText(a.AllowSecret, newBody); err != nil {
		return err
	}
	if err := CheckMemorySize("", newBody); err != nil {
		return err
	}
	cols.set("body", newBody)
	return nil
}

// updateProvenance applies --source, --source-date and --confidence. Provenance has to be
// repairable in place: doctor tells you to date an undated 'confirmed', and without these
// that advice could not be followed.
func updateProvenance(a UpdateArgs, m *Memory, cols *columnSet) error {
	if a.Source != nil {
		if err := CheckLine("--source", *a.Source); err != nil {
			return err
		}
		if err := GuardText(a.AllowSecret, *a.Source); err != nil {
			return err
		}
		cols.set("source", *a.Source)
	}
	if a.SourceDate != nil {
		d, err := ParseDate(*a.SourceDate, "--source-date")
		if err != nil {
			return err
		}
		cols.set("source_date", d)
	}
	if a.Confidence != nil {
		if !KnownConfidence(*a.Confidence) {
			return fmt.Errorf("--confidence must be one of confirmed, reported, assumed")
		}
		// Same rule as remember, and it has to be here too: otherwise `update --confidence
		// confirmed` is the way round the citation requirement, one command later.
		src := m.Source
		if a.Source != nil {
			src = *a.Source
		}
		bod := m.Body
		if a.Body != nil {
			bod = *a.Body
		}
		conf, err := EarnedConfidence(NormConfidence(*a.Confidence), src, a.Refs, bod)
		if err != nil {
			return err
		}
		cols.set("confidence", conf)
	}
	return nil
}

// updateDates applies --date, --snooze-until, --review and --no-review.
func updateDates(a UpdateArgs, cols *columnSet) error {
	for _, f := range []struct{ flag, col string }{
		{a.Happened, "happened_on"}, {a.SnoozeUntil, "snooze_until"}, {a.ReviewOn, "review_on"},
	} {
		if f.flag == "" {
			continue
		}
		d, err := ParseDate(f.flag, f.col)
		if err != nil {
			return err
		}
		// happened_on is stored as a day; the other two are timestamps.
		if f.col == "happened_on" {
			cols.set("happened_on", d)
			continue
		}
		ts, err := DayToTS(d)
		if err != nil {
			return err
		}
		cols.set(f.col, ts)
	}
	// --review takes a date, and an empty string is indistinguishable from the flag being
	// absent, so without --no-review the brief's own advice for a memory you are done
	// reviewing could not be followed.
	if a.NoReview {
		if a.ReviewOn != "" {
			Warn("--no-review ignored: --review %s asked for a date", a.ReviewOn)
		} else {
			cols.raw("review_on=NULL")
		}
	}
	return nil
}

func updatePin(a UpdateArgs, cols *columnSet) error {
	if a.Pin && a.Unpin {
		return fmt.Errorf("--pin and --unpin are contradictory")
	}
	if a.Pin {
		cols.raw("pinned=1")
	}
	if a.Unpin {
		cols.raw("pinned=0")
	}
	return nil
}

// unsupersede clears a memory's superseded_by link. Without it a session that superseded
// the wrong memory could only fix it with raw SQL, which the FTS design forbids. It is
// narrow on purpose: it clears the link and leaves both memories standing, because the
// alternative reading (delete one of them) is a decision the caller has to make.
//
// It reads through tx, not s.DB: the transaction is already open and the store's single
// connection means a second connection request waits on a connection that can only be
// freed by the transaction doing the waiting.
func unsupersede(tx *sql.Tx, id int64, cols *columnSet) error {
	var by sql.NullInt64
	if err := tx.QueryRow("SELECT superseded_by FROM memories WHERE id=?",
		id).Scan(&by); err != nil {
		return err
	}
	if !by.Valid {
		return fmt.Errorf("#%d is not superseded by anything", id)
	}
	cols.raw("superseded_by=NULL")
	return nil
}

// reindex refreshes the full-text index from the row as it now stands.
func (s *Store) reindex(tx *sql.Tx, id int64) error {
	var title, body string
	if err := tx.QueryRow("SELECT title, body FROM memories WHERE id=?", id).Scan(&title, &body); err != nil {
		return err
	}
	return s.ftsPut(tx, id, title, body)
}

type VerifyArgs struct {
	ID         int64
	Confidence string
	Source     string
	JSON       bool
}

// Verify re-stamps a fact as checked today. Old truth reads exactly like current truth,
// which is the whole reason this exists.
func (s *Store) Verify(w io.Writer, a VerifyArgs) error {
	m, err := s.Own(a.ID, "verify")
	if err != nil {
		return err
	}
	ts := Now()
	var cols columnSet
	cols.set("verified_at", ts)
	// Verifying a claim about the code is checking it against the tree that is here now,
	// which is the whole content of the freshness verdict. Without this a memory would
	// still read as N commits out of date immediately after someone confirmed it at HEAD.
	if VolatileTypes[m.MemType] {
		if commit := s.headCommit(); commit != "" {
			cols.set("source_commit", commit)
		}
	}
	if a.Confidence != "" {
		if !KnownConfidence(a.Confidence) {
			return fmt.Errorf("--confidence must be one of confirmed, reported, assumed")
		}
		// Verifying is the natural place to claim `confirmed`, and the natural place to
		// claim it without having checked anything. --source is what says what was checked.
		src := a.Source
		if src == "" {
			src = m.Source
		}
		conf, err := EarnedConfidence(NormConfidence(a.Confidence), src, nil, m.Body)
		if err != nil {
			return err
		}
		cols.set("confidence", conf)
	}
	if a.Source != "" {
		if err := CheckLine("--source", a.Source); err != nil {
			return err
		}
		if err := GuardText(false, a.Source); err != nil {
			return err
		}
		cols.set("source", a.Source)
	}
	// Re-confirming refreshes decay too.
	cols.set("updated_at", ts)
	if _, err := s.DB.Exec("UPDATE memories SET "+cols.sql()+" WHERE id=?",
		append(cols.vals, a.ID)...); err != nil {
		return err
	}
	return Out(w, a.JSON, fmt.Sprintf("verified #%d at %s", a.ID, ISO(ts)),
		map[string]interface{}{"id": a.ID, "verified_at": ts})
}
