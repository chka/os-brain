package store

import (
	"database/sql"
	"fmt"
	"io"
	"strings"

	"github.com/chka/os-brain/internal/pyfmt"
	"github.com/chka/os-brain/internal/timefmt"
)

// A project in the shared store is addressed by name: every filter is `project = ?`. If the
// name were simply whatever a directory declared (brain.json, OS_BRAIN_PROJECT or the
// directory's basename), a cloned repository committing {"project":"victim","shared":true},
// or a second checkout that happens to be called `app`, could read, write, drain and
// un-private another project. The binding below is what makes a name belong to one
// directory.
//
// It is held in its own table rather than in the project index because the index is the
// wrong shape for it: its rows age out after six months, a project can opt out of it, and it
// is keyed by directory, so it holds as many rows for one name as there are directories
// claiming it. root_id is the same opaque hash of the directory the index uses (the store
// never holds paths); private lives here too, so it is keyed to the identity rather than to
// whichever row with that name a reader happens to find.
const ProjectIDsDDL = `CREATE TABLE IF NOT EXISTS project_ids (
    name TEXT PRIMARY KEY,
    root_id TEXT NOT NULL,
    private INTEGER NOT NULL DEFAULT 0,
    bound_at INTEGER NOT NULL
)`

// ErrBoundElsewhere is returned by RegisterProject for a name another directory holds.
type ErrBoundElsewhere struct{ name string }

func (e ErrBoundElsewhere) Error() string {
	return fmt.Sprintf("%s is bound to another directory in the shared index; this one is "+
		"not registered under it. Give this project its own name (\"project\" in brain.json)",
		pyfmt.Repr(e.name))
}

// isNoSuchTable is true for the error a query gets on a store that predates a table. Each
// caller decides what a missing table means for it.
func isNoSuchTable(err error) bool {
	return strings.Contains(err.Error(), "no such table")
}

// checkIdentity runs when the shared store is opened. A directory may act as project P only
// if P is bound to it; the first directory to open an unbound name gets it (trust on first
// use), unless the index already shows that name living somewhere else, which is the moved
// or cloned case and needs a person.
func (s *Store) checkIdentity() error {
	if s.Env.DBPath != s.Env.GlobalDB || s.Env.DBPath == ":memory:" {
		return nil
	}
	if _, err := s.DB.Exec(ProjectIDsDDL); err != nil {
		return err
	}
	name := s.Env.Project
	if s.Env.Named {
		if s.NamePrivate(s.DB, name) {
			return fmt.Errorf("%s is private: it is reachable only from its own directory, "+
				"not by naming it to this server", pyfmt.Repr(name))
		}
		return nil
	}
	if s.Env.Claim {
		return nil
	}
	mine := HashRoot(s.Env.ProjectRoot())
	var bound string
	var boundAt int64
	var boundPriv bool
	err := s.DB.QueryRow("SELECT root_id, bound_at, private FROM project_ids WHERE name=?", name).
		Scan(&bound, &boundAt, &boundPriv)
	if err == nil {
		return s.keepBinding(name, mine, bound, boundAt, boundPriv)
	}
	if err != sql.ErrNoRows {
		return err
	}
	return s.bindOnFirstOpen(name, mine)
}

// keepBinding handles a name that is already bound: refuse it from any other directory, and
// from its own keep the private flag in step with brain.json. brain.json is the declaration
// and this is the directory it belongs to, so the binding follows it even for a project that
// keeps out of the index, because the binding is what a server asked for this name by an MCP
// caller consults.
func (s *Store) keepBinding(name, mine, bound string, boundAt int64, boundPriv bool) error {
	if bound != mine {
		return s.boundElsewhere(name, bound, boundAt)
	}
	if boundPriv == s.Env.Private {
		return nil
	}
	_, err := s.DB.Exec("UPDATE project_ids SET private=? WHERE name=?",
		BoolToInt(s.Env.Private), name)
	return err
}

// bindOnFirstOpen binds an unbound name to this directory, unless the index shows the name
// only in other directories.
func (s *Store) bindOnFirstOpen(name, mine string) error {
	here, elsewhere, priv, err := s.indexRowsFor(name, mine)
	if err != nil {
		return err
	}
	if here == 0 && elsewhere > 0 {
		return s.boundElsewhere(name, "", 0)
	}
	if s.Env.Private {
		priv = 1
	}
	if _, err := s.DB.Exec("INSERT OR IGNORE INTO project_ids(name, root_id, private, bound_at) "+
		"VALUES(?,?,?,?)", name, mine, priv, timefmt.Now()); err != nil {
		return err
	}
	// Two first opens can race; whoever inserted first holds the name.
	var bound string
	var boundAt int64
	if err := s.DB.QueryRow("SELECT root_id, bound_at FROM project_ids WHERE name=?", name).
		Scan(&bound, &boundAt); err != nil {
		return err
	}
	if bound != mine {
		return s.boundElsewhere(name, bound, boundAt)
	}
	return nil
}

// indexRowsFor counts the project index rows carrying name in this directory (here) and in
// others (elsewhere), and whether any row here is private. A store without the index yet
// counts as no rows.
func (s *Store) indexRowsFor(name, mine string) (here, elsewhere, priv int, err error) {
	rows, qerr := s.DB.Query("SELECT root_id, private FROM projects WHERE name=?", name)
	if qerr != nil {
		if isNoSuchTable(qerr) {
			return 0, 0, 0, nil
		}
		return 0, 0, 0, qerr
	}
	defer rows.Close()
	for rows.Next() {
		var rid string
		var p int
		if rows.Scan(&rid, &p) != nil {
			continue
		}
		if rid == mine {
			here++
			priv |= p
		} else {
			elsewhere++
		}
	}
	return here, elsewhere, priv, nil
}

func (s *Store) boundElsewhere(name, bound string, at int64) error {
	where := "the shared index lists it in another directory"
	if bound != "" {
		where = fmt.Sprintf("it is bound to another directory (root id %s, since %s)",
			shortID(bound), timefmt.Day(at))
	}
	return fmt.Errorf("%s is another project in the shared store: %s, not to %s.\n"+
		"Refusing to open it from here. Two directories under one name read, write and delete "+
		"each other's memories.\n"+
		"  a different project that shares the name: give this one its own, \"project\" in %s/brain.json\n"+
		"  the same project, moved here: a person at a terminal runs `os-brain relabel --claim` here\n"+
		"(Only a hash of the directory is stored, never the path, so this cannot say where.)",
		pyfmt.Repr(name), where, s.Env.ProjectRoot(), s.Env.Here)
}

// NamePrivate is true when the name is declared private by its binding or by ANY index row
// carrying it. Either is enough: a stale or duplicate row saying otherwise must not win.
func (s *Store) NamePrivate(g *sql.DB, name string) bool {
	var n int
	if err := g.QueryRow("SELECT (SELECT COUNT(*) FROM project_ids WHERE name=? AND private=1)"+
		" + (SELECT COUNT(*) FROM projects WHERE name=? AND private=1)", name, name).
		Scan(&n); err != nil {
		// Unreadable is not "open".
		return !isNoSuchTable(err)
	}
	return n > 0
}

// PrivateNamesSQL selects every name some binding or index row declares private.
const PrivateNamesSQL = "SELECT name FROM project_ids WHERE private=1 " +
	"UNION SELECT name FROM projects WHERE private=1"

// bindRegistered keeps the binding in step with RegisterProject: an unbound name is bound
// here, a name bound here gets this project's private flag, a name bound elsewhere refuses.
func (s *Store) bindRegistered(g *sql.DB) error {
	if _, err := g.Exec(ProjectIDsDDL); err != nil {
		return err
	}
	mine := HashRoot(s.Env.ProjectRoot())
	if _, err := g.Exec("INSERT OR IGNORE INTO project_ids(name, root_id, private, bound_at) "+
		"VALUES(?,?,?,?)", s.Env.Project, mine, BoolToInt(s.Env.Private), timefmt.Now()); err != nil {
		return err
	}
	var bound string
	if err := g.QueryRow("SELECT root_id FROM project_ids WHERE name=?", s.Env.Project).
		Scan(&bound); err != nil {
		return err
	}
	if bound != mine {
		return ErrBoundElsewhere{s.Env.Project}
	}
	_, err := g.Exec("UPDATE project_ids SET private=? WHERE name=?", BoolToInt(s.Env.Private),
		s.Env.Project)
	return err
}

// Claim moves this project's binding to this directory: `relabel --claim`, after a move.
// confirm is the person's typed yes, supplied only by the CLI at a terminal, because a
// claim hands one directory's memories to another and an agent must not be able to do it.
func (s *Store) Claim(w io.Writer, confirm func(summary string) (bool, error), asJSON bool) error {
	if err := s.checkClaimAllowed(confirm); err != nil {
		return err
	}
	if _, err := s.DB.Exec(ProjectIDsDDL); err != nil {
		return err
	}
	name, mine := s.Env.Project, HashRoot(s.Env.ProjectRoot())
	var bound string
	var at int64
	err := s.DB.QueryRow("SELECT root_id, bound_at FROM project_ids WHERE name=?", name).
		Scan(&bound, &at)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil && bound == mine {
		return Out(w, asJSON, fmt.Sprintf("%s is already bound to this directory", pyfmt.Repr(name)),
			map[string]interface{}{"project": name, "changed": false})
	}
	var n int
	_ = s.DB.QueryRow("SELECT COUNT(*) FROM memories WHERE project=?", name).Scan(&n)
	ok, err := confirm(s.claimSummary(name, bound, at, n))
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("not claimed: nothing changed")
	}
	if err := s.moveBinding(name, mine); err != nil {
		return err
	}
	return Out(w, asJSON, fmt.Sprintf("%s is now bound to %s", pyfmt.Repr(name), s.Env.ProjectRoot()),
		map[string]interface{}{"project": name, "changed": true, "memories": n})
}

func (s *Store) checkClaimAllowed(confirm func(string) (bool, error)) error {
	if !s.Env.Claim || confirm == nil {
		return fmt.Errorf("moving a project's identity is a person's act: run `os-brain " +
			"relabel --claim` yourself, at a terminal")
	}
	if s.Env.DBPath != s.Env.GlobalDB {
		return fmt.Errorf("%s has its own database, so no other directory can claim its name; "+
			"there is nothing to move", pyfmt.Repr(s.Env.Project))
	}
	if s.Env.Named {
		return fmt.Errorf("a claim is made from the project's own directory, not by naming it")
	}
	return nil
}

// claimSummary is what the person confirms: who holds the name now, and what moves.
func (s *Store) claimSummary(name, bound string, at int64, memories int) string {
	was := "no directory yet"
	if bound != "" {
		was = fmt.Sprintf("another directory (root id %s, since %s)", shortID(bound), timefmt.Day(at))
	}
	return fmt.Sprintf("Project %s is bound to %s.\nClaiming it binds it to %s:\n"+
		"its %d memories, notes and approvals become this directory's, and the other one\n"+
		"can no longer open them. Do this only if %s is that project, moved here.",
		pyfmt.Repr(name), was, s.Env.ProjectRoot(), memories, s.Env.ProjectRoot())
}

// moveBinding rebinds name to this directory and drops the index rows other directories
// hold under it, in one transaction.
func (s *Store) moveBinding(name, mine string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var priv int
	_ = tx.QueryRow("SELECT private FROM project_ids WHERE name=?", name).Scan(&priv)
	if s.Env.Private {
		priv = 1
	}
	if _, err := tx.Exec("INSERT INTO project_ids(name, root_id, private, bound_at) VALUES(?,?,?,?) "+
		"ON CONFLICT(name) DO UPDATE SET root_id=excluded.root_id, private=excluded.private, "+
		"bound_at=excluded.bound_at", name, mine, priv, timefmt.Now()); err != nil {
		return err
	}
	// Index rows under this name from any other directory are now stale by definition.
	if _, err := tx.Exec("DELETE FROM projects WHERE name=? AND root_id<>?", name, mine); err != nil &&
		!isNoSuchTable(err) {
		return err
	}
	return tx.Commit()
}

func shortID(rootID string) string {
	if len(rootID) > 12 {
		return rootID[:12]
	}
	return rootID
}

// PrivateProject reports whether another project has declared itself private, for a
// roster shown to callers that are not that project (the MCP server's list of who it
// fronts). Unknown or unreadable counts as private.
func (s *Store) PrivateProject(name string) bool {
	g := s.GlobalConnect(false)
	if g == nil {
		return true
	}
	defer s.CloseGlobal(g)
	return s.NamePrivate(g, name)
}

// OwnsName guards what the spine addresses by project NAME, which every project reaches
// whether or not its memories are in the shared store: notes and their inbox, the shared
// mailbox, direct messages. A directory whose name is bound to another one reads and acks
// none of it, and sends nothing signed with that name: otherwise a clone could ack another
// project's inbox and send notes "from" it.
func (s *Store) OwnsName(g *sql.DB) error {
	if s.Env.Named {
		return nil
	}
	var bound string
	err := g.QueryRow("SELECT root_id FROM project_ids WHERE name=?", s.Env.Project).Scan(&bound)
	switch {
	case err == sql.ErrNoRows, err == nil && bound == HashRoot(s.Env.ProjectRoot()):
		return nil
	case err != nil && isNoSuchTable(err):
		return nil
	case err != nil:
		return err
	}
	return ErrBoundElsewhere{s.Env.Project}
}
