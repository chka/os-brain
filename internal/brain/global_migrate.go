package brain

import (
	"database/sql"
	"strings"
)

// spineMigration is one step that brings a spine below version `below` forward.
type spineMigration struct {
	below int
	step  func(g sqlRunner) error
}

// spineMigrations run in this order, which is the order they were written in and NOT
// version order. Two orderings matter: rewording the retired axioms must run before
// marking the seeded ones (the old wording is not a current default until it has been
// rewritten), and binding project names reads root_id, which hashing the project roots
// renames into existence.
var spineMigrations = []spineMigration{
	{11, addDirectMessageKinds},
	{9, hashProjectRoots},
	{8, addProjectPrivateFlag},
	{7, rewordRetiredAxioms},
	{5, addAxiomProvenance},
	{12, bindProjectNamesAndHashLessonRoots},
	{13, addEditorColumns},
	{14, clearBroadcastReadState},
	{15, recordDefaultsAlreadyOffered},
}

// migrateGlobal brings an older spine forward. Every step is additive, same rule as the
// project store: this file is every project's axioms, standards and undelivered notes, so
// nothing in it is ever dropped to make a migration simpler.
func migrateGlobal(g sqlRunner, have int) error {
	for _, m := range spineMigrations {
		if have < m.below {
			if err := m.step(g); err != nil {
				return err
			}
		}
	}
	return nil
}

type spineColumn struct{ name, decl string }

// addMissingColumns adds each column the table does not have yet. With skipMissingTable
// set, a table that does not exist is left alone: globalSchemaDDL runs before the
// migrations and creates such a table complete, so altering it would only fail with
// "no such table". Without it, a missing table is an error, as it always was.
func addMissingColumns(g sqlRunner, table string, cols []spineColumn, skipMissingTable bool) error {
	have, err := dbColumns(g, table)
	if err != nil {
		return err
	}
	if skipMissingTable && len(have) == 0 {
		return nil
	}
	for _, c := range cols {
		if have[c.name] {
			continue
		}
		if _, err := g.Exec("ALTER TABLE " + table + " ADD COLUMN " + c.name + " " + c.decl); err != nil {
			return err
		}
	}
	return nil
}

// addDirectMessageKinds (v11): a direct message gets a kind (text or an instruction) and
// a session gets a do-not-disturb flag. Every message already stored reads back as 'text',
// which is what it was.
func addDirectMessageKinds(g sqlRunner) error {
	if err := addMissingColumns(g, "live_sessions", []spineColumn{
		{"muted_at", "INTEGER"},
	}, true); err != nil {
		return err
	}
	return addMissingColumns(g, "dms", []spineColumn{
		{"kind", "TEXT NOT NULL DEFAULT 'text'"},
		{"accepted_at", "INTEGER"},
		{"rejected_at", "INTEGER"},
	}, true)
}

// hashProjectRoots (v9) takes the filesystem out of the index. `brief` registers the
// project on every run, and before this it wrote the project's absolute path into a file
// every other project on the machine reads: fine for one developer's own projects, not for
// one machine holding three clients' work, and nobody was told.
//
// The column is renamed rather than dropped and each value is replaced by a hash of the
// same path, so identity survives and location does not. Paths already written are
// overwritten here on purpose: leaving them would protect only projects registered after
// the upgrade.
func hashProjectRoots(g sqlRunner) error {
	cols, err := dbColumns(g, "projects")
	if err != nil {
		return err
	}
	if cols["root"] && !cols["root_id"] {
		if _, err := g.Exec("ALTER TABLE projects RENAME COLUMN root TO root_id"); err != nil {
			return err
		}
	}
	rows, err := g.Query("SELECT root_id FROM projects")
	if err != nil {
		return err
	}
	olds, err := scanStrings(rows)
	if err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, old := range olds {
		if old == "" || isRootHash(old) {
			continue
		}
		if _, err := g.Exec("UPDATE projects SET root_id=? WHERE root_id=?",
			hashRoot(old), old); err != nil {
			return err
		}
	}
	return nil
}

// isRootHash: 64 characters with no path separator or dot is a value hashRoot already
// produced. A rerun of a migration must not hash the hash, or identity would change.
func isRootHash(v string) bool {
	return len(v) == 64 && !strings.ContainsAny(v, "/\\.")
}

// scanStrings reads a one-column result into a slice and closes the rows.
func scanStrings(rows *sql.Rows) ([]string, error) {
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// addProjectPrivateFlag (v8): a project can declare itself private, which the projects
// that must honour it can only learn from the index.
func addProjectPrivateFlag(g sqlRunner) error {
	return addMissingColumns(g, "projects", []spineColumn{
		{"private", "INTEGER NOT NULL DEFAULT 0"},
	}, false)
}

// rewordRetiredAxioms (below v7) rewrites the seeded axioms whose shipped wording named one
// developer. Every install that ever created a spine has that wording, and an axiom is read
// live by every project, so leaving it would tell a stranger's agent to go and ask somebody
// it has never heard of. Rows are matched verbatim against the shipped strings: that is
// the text the binary wrote, not a guess about what somebody meant, and anything reworded
// locally is left alone.
func rewordRetiredAxioms(g sqlRunner) error {
	for _, r := range retiredAxioms {
		// A spine that already holds the new text keeps it; the UPDATE would collide with
		// the UNIQUE index and take the whole migration down with it.
		var clash int
		if g.QueryRow("SELECT 1 FROM axioms WHERE text=?", r[1]).Scan(&clash) == nil {
			continue
		}
		if _, err := g.Exec("UPDATE axioms SET text=?, updated_at=? WHERE text=?",
			r[1], Now(), r[0]); err != nil {
			return err
		}
	}
	for _, r := range retiredRationales {
		if _, err := g.Exec("UPDATE axioms SET rationale=?, updated_at=? WHERE rationale=?",
			r[1], Now(), r[0]); err != nil {
			return err
		}
	}
	return nil
}

// addAxiomProvenance (v5) records who wrote each axiom. Existing rows keep an empty origin
// and seeded=0, which is honest: nothing recorded who wrote them, and marking them all as
// seeded would let a custom axiom inherit the protection meant for the core set. The one
// exception is a row whose text is verbatim a default: that is not a guess about
// authorship, it is the same string the binary ships.
func addAxiomProvenance(g sqlRunner) error {
	if err := addMissingColumns(g, "axioms", []spineColumn{
		{"origin_project", "TEXT NOT NULL DEFAULT ''"},
		{"origin_session", "TEXT NOT NULL DEFAULT ''"},
		{"seeded", "INTEGER NOT NULL DEFAULT 0"},
	}, false); err != nil {
		return err
	}
	for _, ax := range DefaultAxioms {
		if _, err := g.Exec("UPDATE axioms SET seeded=1, origin_project='os-brain' "+
			"WHERE text=? AND seeded=0", ax[0]); err != nil {
			return err
		}
	}
	return nil
}

// bindProjectNamesAndHashLessonRoots (v12) binds each project name to one directory
// (project_ids, see projectid.go). The bindings are seeded from the index wherever that is
// unambiguous: a name with exactly one directory is bound to it, so the project already
// living there keeps opening and a clone appearing later is refused. A name the index holds
// under several directories is left unbound and goes to whichever opens first, or to
// `relabel --claim`.
//
// Published lessons stop carrying an absolute path in the same step, replaced by the hash
// the index uses, as v9 did for the index itself.
func bindProjectNamesAndHashLessonRoots(g sqlRunner) error {
	if _, err := g.Exec(projectIDsDDL); err != nil {
		return err
	}
	if pcols, err := dbColumns(g, "projects"); err != nil {
		return err
	} else if pcols["root_id"] {
		if _, err := g.Exec("INSERT OR IGNORE INTO project_ids(name, root_id, private, bound_at) " +
			"SELECT name, MIN(root_id), MAX(private), MAX(last_seen) FROM projects " +
			"GROUP BY name HAVING COUNT(DISTINCT root_id) = 1"); err != nil {
			return err
		}
	}
	lcols, err := dbColumns(g, "lessons")
	if err != nil {
		return err
	}
	if !lcols["project_root"] {
		return nil
	}
	rows, err := g.Query("SELECT DISTINCT project_root FROM lessons WHERE project_root <> ''")
	if err != nil {
		return err
	}
	olds, err := scanStrings(rows)
	if err != nil {
		return err
	}
	for _, old := range olds {
		if isRootHash(old) {
			continue
		}
		if _, err := g.Exec("UPDATE lessons SET project_root=? WHERE project_root=?",
			hashRoot(old), old); err != nil {
			return err
		}
	}
	return nil
}

// addEditorColumns (v13): a reworded axiom or standard kept its first author and nothing
// else, so a rule rewritten by any session read as the original author's, "core" included.
// The editor is now recorded beside the author, never over it.
func addEditorColumns(g sqlRunner) error {
	if err := addMissingColumns(g, "axioms", []spineColumn{
		{"edited_project", "TEXT NOT NULL DEFAULT ''"},
		{"edited_session", "TEXT NOT NULL DEFAULT ''"},
		{"edited_at", "INTEGER NOT NULL DEFAULT 0"},
	}, true); err != nil {
		return err
	}
	return addMissingColumns(g, "standards", []spineColumn{
		{"edited_by", "TEXT NOT NULL DEFAULT ''"},
		{"edited_at", "INTEGER NOT NULL DEFAULT 0"},
	}, true)
}

// clearBroadcastReadState (v14): a broadcast's read, ack and accept moved to note_reads,
// one row per recipient. What a broadcast row already carried was stamped by one recipient
// nobody recorded, and it hid the item from every other one, so it is cleared rather than
// guessed at: each recipient sees it unread again, and the note sweep stops deleting it
// for everybody a week after one project acked it.
func clearBroadcastReadState(g sqlRunner) error {
	cols, err := dbColumns(g, "notes")
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		return nil
	}
	set := "read_at=NULL, acked_at=NULL"
	if cols["accepted_as"] {
		set += ", accepted_as=NULL"
	}
	_, err = g.Exec("UPDATE notes SET " + set + " WHERE to_project='*'")
	return err
}

// recordDefaultsAlreadyOffered (v15): defaults are now offered by text (axioms_offered)
// rather than seeded once. Every default this spine could already have been given is
// recorded as offered, so none that an owner removed comes back. The four laws, new in this
// version, are not recorded, so offerDefaultAxioms gives them to the spine on this open.
func recordDefaultsAlreadyOffered(g sqlRunner) error {
	// The fresh DDL makes this table, but a spine built by a fixture or an older binary
	// reaches here without it.
	if _, err := g.Exec("CREATE TABLE IF NOT EXISTS axioms_offered (text TEXT PRIMARY KEY, " +
		"offered_at INTEGER NOT NULL)"); err != nil {
		return err
	}
	ts := Now()
	for _, text := range defaultsOfferedBeforeV15() {
		if _, err := g.Exec("INSERT OR IGNORE INTO axioms_offered(text,offered_at) VALUES(?,?)",
			text, ts); err != nil {
			return err
		}
	}
	return nil
}

// defaultsOfferedBeforeV15 is what a v14 spine could have been given: the defaults of that
// time, which are today's minus the four laws (DefaultAxioms[0]) and minus any reworded
// replacement, plus every text since retired. A replacement is left out so that
// retireDefaultAxioms or the next offer delivers it.
func defaultsOfferedBeforeV15() []string {
	replacement := map[string]bool{}
	for _, r := range RetiredDefaults {
		replacement[r[1]] = true
	}
	var was []string
	for _, ax := range DefaultAxioms[1:] {
		if !replacement[ax[0]] {
			was = append(was, ax[0])
		}
	}
	for _, r := range RetiredDefaults {
		was = append(was, r[0])
	}
	return was
}
