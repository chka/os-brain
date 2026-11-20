package brain

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
)

// `os-brain publish` shares one memory as a cross-project lesson in the spine, where any
// project can find it with `global --recall`. Nothing is published automatically: what one
// project learned is that project's to offer.

type PublishArgs struct {
	Retract bool
	ID      int64
	Why     string
	JSON    bool
}

func (s *Store) Publish(w io.Writer, a PublishArgs) error {
	if a.Retract {
		return s.retractLesson(w, a)
	}
	m, err := s.Own(a.ID, "publish")
	if err != nil {
		return err
	}
	if !s.globalEnabled() {
		return fmt.Errorf("this project has global: false in brain.json and does not publish")
	}
	g := s.globalConnect(true)
	if g == nil {
		return fmt.Errorf("the global brain is not writable")
	}
	defer s.closeGlobal(g)
	if err := CheckLines("the lesson's title", m.Title, "--why", a.Why); err != nil {
		return err
	}
	if err := GuardTextNoOverride(m.Title, m.Body, a.Why); err != nil {
		return err
	}
	lid, err := s.upsertLesson(g, m, a.Why)
	if err != nil {
		return err
	}
	if _, err := g.Exec("INSERT INTO lesson_fts(rowid,title,body) VALUES(?,?,?)",
		lid, StripAccents(m.Title), StripAccents(m.Body)); err != nil {
		return err
	}
	// The lesson's own id is what --retract takes, so it is printed here rather than left
	// to be read off a `global --recall` listing. In the JSON, id is the lesson and
	// published is the memory it came from.
	return Out(w, a.JSON, fmt.Sprintf("published #%d as lesson #%d, visible to every "+
		"project:\n  [%s] %s\n  %s\n  os-brain publish %d --retract   takes it back",
		m.ID, lid, OneLine(s.Env.Project), OneLine(m.Title), BodyText(Clip(m.Body, 200)), lid),
		map[string]interface{}{"id": lid, "published": m.ID, "title": m.Title})
}

// upsertLesson writes the lesson row and returns its id. A lesson is identified by this
// project's directory plus the memory's normalised title, so publishing the same memory
// again refreshes the existing lesson; its old search entry is removed so the caller can
// write the new one.
func (s *Store) upsertLesson(g *sql.DB, m *Memory, why string) (int64, error) {
	sum := sha256.Sum256([]byte(s.Env.ProjectRoot() + "\x00" + NormTitle(m.Title)))
	h := hex.EncodeToString(sum[:])
	res, err := g.Exec("INSERT INTO lessons(project,project_root,mem_type,title,body,"+
		"confidence,why,hash,published_at) VALUES(?,?,?,?,?,?,?,?,?)",
		s.Env.Project, hashRoot(s.Env.ProjectRoot()), m.MemType, m.Title, m.Body, m.Confidence,
		why, h, Now())
	if err == nil {
		return res.LastInsertId()
	}
	if _, err2 := g.Exec("UPDATE lessons SET body=?, confidence=?, why=?, published_at=? "+
		"WHERE hash=?", m.Body, m.Confidence, why, Now(), h); err2 != nil {
		return 0, err
	}
	var lid int64
	if err := g.QueryRow("SELECT id FROM lessons WHERE hash=?", h).Scan(&lid); err != nil {
		return 0, err
	}
	if _, err := g.Exec("DELETE FROM lesson_fts WHERE rowid=?", lid); err != nil {
		return 0, err
	}
	return lid, nil
}

// retractLesson takes a published lesson back out of the spine. A lesson is a copy that
// lives outside the project that wrote it, so turning a project private cannot reach it;
// `private` points here for that reason.
//
// Retracting deletes the copy. It cannot unpublish what another project already read, and
// says so rather than implying it can.
func (s *Store) retractLesson(w io.Writer, a PublishArgs) error {
	g := s.globalConnect(true)
	if g == nil {
		return fmt.Errorf("the global brain is not writable")
	}
	defer s.closeGlobal(g)
	// Only your own: a lesson belongs to the project that published it, same rule as
	// every by-id command.
	var owner, title string
	err := g.QueryRow("SELECT project, title FROM lessons WHERE id=?", a.ID).Scan(&owner, &title)
	if err != nil {
		return fmt.Errorf("no published lesson #%d. os-brain global --recall \"<words>\" "+
			"lists them with their ids", a.ID)
	}
	if owner != s.Env.Project {
		return fmt.Errorf("lesson #%d was published by %s, not by %s. Only the project that "+
			"published a lesson can retract it", a.ID, Repr(owner), Repr(s.Env.Project))
	}
	if _, err := g.Exec("DELETE FROM lessons WHERE id=?", a.ID); err != nil {
		return err
	}
	return Out(w, a.JSON, fmt.Sprintf("retracted lesson #%d: %s\n"+
		"  The copy is gone from the shared index. It cannot unread what another project "+
		"already read, and the memory itself is untouched in this project.", a.ID, title),
		map[string]interface{}{"retracted": a.ID, "title": title})
}
