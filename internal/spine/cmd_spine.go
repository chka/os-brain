package spine

import (
	"database/sql"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/chka/os-brain/internal/pyfmt"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/textguard"
	"github.com/chka/os-brain/internal/timefmt"
)

// `os-brain axiom` and `os-brain standard`: the house rules in the spine. Axioms bind every
// project; standards are the defaults a new project starts from. Both are read as rules by
// every session on the machine, so every change passes confirmSpine, a person's yes at a
// terminal.

// confirmSpine is the person gate every spine write passes: axioms and standards are read
// as house rules by every project, so changing one is not an agent's call.
func confirmSpine(confirm func(string) (bool, error), cmd, summary string) error {
	if confirm == nil {
		return fmt.Errorf("changing the %ss every project reads is a person's decision. Ask the "+
			"person to run `os-brain %s ...` themselves, in a terminal", cmd, cmd)
	}
	ok, err := confirm(summary)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("nothing changed")
	}
	return nil
}

// spineAuthor is how a spine write names who made it.
func (s *Store) spineAuthor() string {
	by := s.Env.Project
	if sid := s.Env.SessionID(); sid != "" {
		by += ", session " + store.ShortSession(sid)
	}
	return by
}

// --- axiom -----------------------------------------------------------------------

type AxiomArgs struct {
	Sub  string // add | rm | list | edit
	Text string
	Why  string
	ID   int64
	JSON bool
	// Confirm shows a person the change and reports their answer. Only the CLI sets it, at a
	// terminal: an axiom binds every project on this machine, so a session (or a note that
	// talked one into it) must not be able to write the rules every other session obeys.
	Confirm func(summary string) (bool, error)
}

func (s *Store) Axiom(w io.Writer, a AxiomArgs) error {
	switch a.Sub {
	case "add":
		return s.axiomAdd(w, a)
	case "edit":
		return s.axiomEdit(w, a)
	case "rm":
		return s.axiomRemove(w, a)
	}
	return s.axiomList(w, a)
}

func (s *Store) axiomAdd(w io.Writer, a AxiomArgs) error {
	g := s.GlobalConnect(true)
	if g == nil {
		return fmt.Errorf("the global brain is not writable, so axioms cannot be edited here")
	}
	defer s.CloseGlobal(g)
	// An axiom is the widest write in the system: one row, read live by every project on
	// the machine. It once accepted an empty string, and a session fumbling the argument
	// order put a blank axiom into the shared spine while the confirmation said it now
	// "applies to every project from their next session".
	if strings.TrimSpace(a.Text) == "" {
		return fmt.Errorf("an axiom needs text. It is one row read live by every " +
			"project on this machine, so an empty one is a rule nobody can follow " +
			"and everybody is shown.\n" +
			"Usage: os-brain axiom add \"<the rule>\" --why \"<what it prevents>\"")
	}
	if err := textguard.CheckLines("the axiom", a.Text, "--why", a.Why); err != nil {
		return err
	}
	if err := textguard.GuardTextNoOverride(a.Text, a.Why); err != nil {
		return err
	}
	if err := confirmSpine(a.Confirm, "axiom", fmt.Sprintf("Add an axiom that binds every "+
		"project on this machine, as %s:\n  %s\n  why: %s", s.spineAuthor(), a.Text, a.Why)); err != nil {
		return err
	}
	if err := s.insertAxiom(g, a.Text, a.Why); err != nil {
		return err
	}
	rows := s.axiomRows(g)
	return store.Out(w, a.JSON, fmt.Sprintf("axiom recorded as %s; it applies to every project "+
		"from their next session, and each one's brief will say it changed\n(%d axioms now)",
		pyfmt.Repr(s.Env.Project), len(rows)),
		map[string]interface{}{"axioms": rows})
}

// insertAxiom writes a new axiom attributed to this project and session. Adding text that
// already exists rewords its rationale instead, which is an edit: recorded as one, never
// as authorship. When even that fails, the insert's error is the one returned.
func (s *Store) insertAxiom(g *sql.DB, text, why string) error {
	ts := timefmt.Now()
	_, err := g.Exec("INSERT INTO axioms(text,rationale,origin_project,origin_session,"+
		"seeded,created_at,updated_at) VALUES(?,?,?,?,0,?,?)",
		text, why, s.Env.Project, s.Env.SessionID(), ts, ts)
	if err == nil {
		return nil
	}
	if _, err2 := g.Exec("UPDATE axioms SET rationale=?, edited_project=?, "+
		"edited_session=?, edited_at=?, updated_at=? WHERE text=?",
		why, s.Env.Project, s.Env.SessionID(), ts, ts, text); err2 != nil {
		return err
	}
	return nil
}

// axiomEdit rewords an axiom in place. Without it the only routes were `add` with the same
// text, which stamps the editor over the author, or raw SQL against the spine. Editing keeps
// the author, records the editor beside it, and says what it changed.
func (s *Store) axiomEdit(w io.Writer, a AxiomArgs) error {
	g := s.GlobalConnect(true)
	if g == nil {
		return fmt.Errorf("the global brain is not writable, so axioms cannot be edited here")
	}
	defer s.CloseGlobal(g)
	if a.ID == 0 {
		return fmt.Errorf("which axiom? `os-brain axiom edit --id <n> [new text] --why <new rationale>`\n" +
			"`os-brain axiom list` shows the ids")
	}
	var text, why string
	var seeded int
	if err := g.QueryRow("SELECT text, rationale, seeded FROM axioms WHERE id=?", a.ID).
		Scan(&text, &why, &seeded); err == sql.ErrNoRows {
		return fmt.Errorf("no axiom #%d", a.ID)
	} else if err != nil {
		return err
	}
	if a.Text == "" && a.Why == "" {
		return fmt.Errorf("nothing to change on axiom #%d. Pass new text as an argument, "+
			"a new rationale as --why, or both:\n  %s", a.ID, text)
	}
	if err := textguard.CheckLines("the axiom", a.Text, "--why", a.Why); err != nil {
		return err
	}
	if err := textguard.GuardTextNoOverride(a.Text, a.Why); err != nil {
		return err
	}
	newText, newWhy := text, why
	if a.Text != "" {
		newText = a.Text
	}
	if a.Why != "" {
		newWhy = a.Why
	}
	if newText == text && newWhy == why {
		return store.Out(w, a.JSON, fmt.Sprintf("axiom #%d already reads that way; nothing changed", a.ID),
			map[string]interface{}{"id": a.ID, "changed": false})
	}
	// A core row tracks the binary only while its text is byte-for-byte what shipped:
	// reworded, later releases can no longer update it and it no longer says what shipped,
	// so it stops being core. The rationale carries no such contract.
	reworded := a.Text != "" && a.Text != text
	losesCore := seeded == 1 && reworded
	core := ""
	if losesCore {
		core = " It is one of the core set that ships with the brain, and reworded it stops being core."
	}
	if err := confirmSpine(a.Confirm, "axiom", fmt.Sprintf("Reword axiom #%d, which binds "+
		"every project on this machine, as %s:%s\n  was: %s\n  now: %s\n  why: %s", a.ID,
		s.spineAuthor(), core, text, newText, newWhy)); err != nil {
		return err
	}
	if losesCore {
		fmt.Fprintf(w, "  note: axiom #%d shipped with the binary; reworded here it is no "+
			"longer core, and later releases no longer update it\n", a.ID)
	}
	ts := timefmt.Now()
	if _, err := g.Exec("UPDATE axioms SET text=?, rationale=?, updated_at=?, "+
		"edited_project=?, edited_session=?, edited_at=?, "+
		"seeded=CASE WHEN ? THEN 0 ELSE seeded END WHERE id=?",
		newText, newWhy, ts, s.Env.Project, s.Env.SessionID(), ts, reworded, a.ID); err != nil {
		return err
	}
	lines := []string{fmt.Sprintf("axiom #%d updated; every project sees it from its next brief", a.ID)}
	if newText != text {
		lines = append(lines, "  was:  "+text, "  now:  "+newText)
	}
	if newWhy != why {
		lines = append(lines, "  why:  "+newWhy)
	}
	return store.Out(w, a.JSON, strings.Join(lines, "\n"), map[string]interface{}{
		"id": a.ID, "changed": true, "text": newText, "rationale": newWhy})
}

// axiomRemove deletes an axiom. A core one needs no extra flag: the person answering yes at
// the terminal is the confirmation, and the prompt says the axiom is core.
func (s *Store) axiomRemove(w io.Writer, a AxiomArgs) error {
	g := s.GlobalConnect(true)
	if g == nil {
		return fmt.Errorf("the global brain is not writable")
	}
	defer s.CloseGlobal(g)
	var text string
	var seeded int
	if err := g.QueryRow("SELECT text, seeded FROM axioms WHERE id=?", a.ID).
		Scan(&text, &seeded); err == sql.ErrNoRows {
		return fmt.Errorf("no axiom #%d", a.ID)
	} else if err != nil {
		return err
	}
	core := ""
	if seeded == 1 {
		core = " It is one of the core set that ships with the brain."
	}
	if err := confirmSpine(a.Confirm, "axiom", fmt.Sprintf("Remove axiom #%d from every "+
		"project on this machine:%s\n  %s", a.ID, core, text)); err != nil {
		return err
	}
	if seeded == 1 {
		fmt.Fprintf(w, "  note: axiom #%d was part of the core set that ships with the binary; "+
			"it no longer binds any project here\n", a.ID)
	}
	if _, err := g.Exec("DELETE FROM axioms WHERE id=?", a.ID); err != nil {
		return err
	}
	return store.Out(w, a.JSON, fmt.Sprintf("removed axiom #%d: %s", a.ID, textguard.OneLine(text)),
		map[string]interface{}{"removed": a.ID})
}

func (s *Store) axiomList(w io.Writer, a AxiomArgs) error {
	rows := s.Axioms()
	builtin := true
	for _, r := range rows {
		if r.ID != 0 {
			builtin = false
			break
		}
	}
	bar := strings.Repeat("=", 72)
	lines := []string{bar, "AXIOMS — these hold in every project", bar}
	for _, r := range rows {
		lines = append(lines, axiomListLines(r)...)
	}
	src := s.Env.GlobalDB
	if builtin {
		src = "built into this client (no global brain yet)"
	}
	lines = append(lines, "", "source: "+src,
		"reword one with `os-brain axiom edit --id <n>`; every project reads them live, "+
			"nothing is copied anywhere")
	return store.Out(w, a.JSON, strings.Join(lines, "\n"),
		map[string]interface{}{"axioms": rows, "builtin": builtin, "db": s.Env.GlobalDB})
}

// axiomListLines is one axiom with its rationale and origin. Origin is shown, never
// weighed: an axiom is not more binding because of who wrote it, but a reader deciding
// whether one is right needs to know whether it came with the binary or from a session in
// some other project last Tuesday.
func axiomListLines(r Axiom) []string {
	tag := " -"
	if r.ID != 0 {
		tag = "#" + strconv.FormatInt(r.ID, 10)
	}
	lines := []string{fmt.Sprintf("  %s %s", tag, textguard.OneLine(r.Text))}
	if r.Rationale != "" {
		lines = append(lines, "      why: "+textguard.OneLine(r.Rationale))
	}
	switch {
	case r.Seeded:
		lines = append(lines, "      core: ships with the brain")
	case r.Origin != "":
		who := "      from: " + textguard.OneLine(r.Origin)
		if r.Session != "" {
			who += ", session " + store.ShortSession(r.Session)
		}
		lines = append(lines, who+", "+timefmt.Day(r.CreatedAt))
	}
	if r.EditedBy != "" {
		who := "      edited by: " + textguard.OneLine(r.EditedBy)
		if r.EditedSession != "" {
			who += ", session " + store.ShortSession(r.EditedSession)
		}
		lines = append(lines, who+", "+timefmt.Day(r.EditedAt))
	}
	return lines
}

// --- standard --------------------------------------------------------------------

type Standard struct {
	ID        int64  `json:"id"`
	Category  string `json:"category"`
	Text      string `json:"text"`
	Rationale string `json:"rationale"`
	Origin    string `json:"origin"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	EditedBy  string `json:"edited_by"`
	EditedAt  int64  `json:"edited_at"`
}

func (st Standard) Provenance() string {
	return spineProvenance(st.Origin, st.CreatedAt, st.EditedBy, st.EditedAt)
}

// Standards reads every house standard, grouped by category.
func (s *Store) Standards() []Standard {
	g := s.GlobalConnect(false)
	if g == nil {
		return nil
	}
	defer s.CloseGlobal(g)
	rows, err := g.Query("SELECT id, category, text, rationale, origin, created_at, updated_at, " +
		"edited_by, edited_at FROM standards ORDER BY category, id")
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []Standard{}
	for rows.Next() {
		var st Standard
		if err := rows.Scan(&st.ID, &st.Category, &st.Text, &st.Rationale, &st.Origin,
			&st.CreatedAt, &st.UpdatedAt, &st.EditedBy, &st.EditedAt); err != nil {
			return out
		}
		out = append(out, st)
	}
	return out
}

type StandardArgs struct {
	Sub      string // add | rm | list
	Text     string
	Why      string
	Category string
	FromID   int64
	ID       int64
	JSON     bool
	// Confirm is the person gate AxiomArgs.Confirm describes: a standard is printed in every
	// new project's brief as the way things are done here.
	Confirm func(summary string) (bool, error)
}

// Standard records house defaults: the stack, the conventions, the way things are done.
// A new project starts from these instead of re-deciding them, and says so when it
// deviates. Unlike axioms they are overridable locally.
func (s *Store) Standard(w io.Writer, a StandardArgs) error {
	if a.Category == "" {
		a.Category = "general"
	}
	switch a.Sub {
	case "add":
		return s.standardAdd(w, a)
	case "rm":
		return s.standardRemove(w, a)
	}
	return s.standardList(w, a)
}

func (s *Store) standardAdd(w io.Writer, a StandardArgs) error {
	g := s.GlobalConnect(true)
	if g == nil {
		return fmt.Errorf("the global brain is not writable, so standards cannot be edited")
	}
	defer s.CloseGlobal(g)
	text, why := a.Text, a.Why
	if a.FromID > 0 {
		var err error
		if text, why, err = s.liftStandard(a.FromID, text, why); err != nil {
			return err
		}
	}
	if text == "" {
		return fmt.Errorf("give the standard as text, or lift one from a memory with --from")
	}
	if err := textguard.CheckLines("the standard", text, "--category", a.Category); err != nil {
		return err
	}
	if err := textguard.GuardTextNoOverride(text, why); err != nil {
		return err
	}
	if err := confirmSpine(a.Confirm, "standard", fmt.Sprintf("Record a house standard "+
		"under %s that every new project starts from, as %s:\n  %s\n  why: %s",
		pyfmt.Repr(a.Category), s.spineAuthor(), text, why)); err != nil {
		return err
	}
	ts := timefmt.Now()
	_, err := g.Exec("INSERT INTO standards(category,text,rationale,origin,created_at,"+
		"updated_at) VALUES(?,?,?,?,?,?)", a.Category, text, why, s.Env.Project, ts, ts)
	if err != nil {
		// The text exists already: this is an edit of its category and rationale.
		if _, err2 := g.Exec("UPDATE standards SET category=?, rationale=?, updated_at=?, "+
			"edited_by=?, edited_at=? WHERE text=?", a.Category, why, ts, s.spineAuthor(), ts,
			text); err2 != nil {
			return err
		}
	}
	return store.Out(w, a.JSON, fmt.Sprintf("standard recorded under %s; new projects start "+
		"from it", pyfmt.Repr(a.Category)), map[string]interface{}{"text": text, "category": a.Category})
}

// liftStandard fills a standard's text and rationale from a memory, where not given. A
// standard is printed in every new project's brief, so it is lifted only from this
// project's own live memory, and never out of a private project.
func (s *Store) liftStandard(id int64, text, why string) (string, string, error) {
	m, err := s.Own(id, "standard")
	if err != nil {
		return "", "", err
	}
	if m.SupersededBy != nil {
		return "", "", fmt.Errorf("#%d was retracted (superseded by #%d); lift the current "+
			"version instead", m.ID, *m.SupersededBy)
	}
	if s.Env.Private {
		return "", "", fmt.Errorf("%s is private, and a standard is printed in other projects' "+
			"briefs: write the standard's text yourself rather than lifting a memory",
			pyfmt.Repr(s.Env.Project))
	}
	if text == "" {
		text = m.Title
	}
	if why == "" {
		why = m.Body
	}
	return text, why, nil
}

func (s *Store) standardRemove(w io.Writer, a StandardArgs) error {
	g := s.GlobalConnect(true)
	if g == nil {
		return fmt.Errorf("the global brain is not writable")
	}
	defer s.CloseGlobal(g)
	var text string
	if err := g.QueryRow("SELECT text FROM standards WHERE id=?", a.ID).Scan(&text); err == sql.ErrNoRows {
		return fmt.Errorf("no standard #%d", a.ID)
	} else if err != nil {
		return err
	}
	if err := confirmSpine(a.Confirm, "standard", fmt.Sprintf("Remove house standard #%d "+
		"from every project on this machine:\n  %s", a.ID, text)); err != nil {
		return err
	}
	if _, err := g.Exec("DELETE FROM standards WHERE id=?", a.ID); err != nil {
		return err
	}
	return store.Out(w, a.JSON, fmt.Sprintf("removed standard #%d: %s", a.ID, textguard.OneLine(text)),
		map[string]interface{}{"removed": a.ID})
}

// standardList filters on the category only. `list` is the default verb rather than one of
// the parsed verbs, so it arrives here as Text; filtering on Text once made
// `standard list --category stack` match nothing.
func (s *Store) standardList(w io.Writer, a StandardArgs) error {
	all := s.Standards()
	rows := all
	if a.Category != "general" {
		rows = nil
		for _, r := range all {
			if r.Category == a.Category {
				rows = append(rows, r)
			}
		}
	}
	bar := strings.Repeat("=", 72)
	lines := []string{bar, "STANDARDS — defaults every project starts from", bar}
	if len(rows) == 0 {
		lines = append(lines, "  (none yet)",
			"  Record the house defaults once and every future project inherits them:",
			"    os-brain standard add \"Go for new infrastructure tools\" --category stack "+
				"--why \"one static binary, no runtime to install\"",
			"    os-brain standard add --from <memory-id> --category security")
	}
	cat := ""
	for _, r := range rows {
		if r.Category != cat {
			cat = r.Category
			lines = append(lines, "", "  ["+textguard.OneLine(cat)+"]")
		}
		lines = append(lines, fmt.Sprintf("    #%d %s%s", r.ID, textguard.OneLine(r.Text), r.Provenance()))
		if r.Rationale != "" {
			lines = append(lines, "        why: "+textguard.OneLine(r.Rationale))
		}
	}
	if len(rows) > 0 {
		lines = append(lines, "",
			"These are defaults, not axioms: deviate when this project needs to,",
			"and store a local preference saying which one and why.")
	}
	return store.Out(w, a.JSON, strings.Join(lines, "\n"), map[string]interface{}{"standards": rows})
}
