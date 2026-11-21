package store

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/chka/os-brain/internal/diag"
	"github.com/chka/os-brain/internal/timefmt"
)

// The project index: one row per project on this machine, so a session knows what else
// exists and whether someone has already solved its problem. A row carries the project's
// own description and a memory count, never a memory and never a path.

// Sibling is another project as the brief lists it.
type Sibling struct {
	Name       string `json:"name"`
	RootID     string `json:"root_id"`
	Memories   int    `json:"memories"`
	About      string `json:"about"`
	Stack      string `json:"stack"`
	ListedOnly bool   `json:"listed_only"`
	Synced     string `json:"synced"`
}

// OtherProjects is awareness without access: names and one-line specs, so a session
// knows what else exists. Scope names are never stored in the index at all: one project's
// scopes named a spouse and a disease, and a value redacted only at render time still
// sits in a shared file anyone can open.
func (s *Store) OtherProjects() []Sibling {
	g := s.GlobalConnect(false)
	if g == nil {
		return nil
	}
	defer s.CloseGlobal(g)
	// A private project is left out altogether: its name, spec and size are what it asked
	// to keep to itself.
	rows, err := g.Query("SELECT name, root_id, description, stack, visibility, memories, "+
		"last_seen FROM projects WHERE root_id <> ? AND private = 0 AND name NOT IN ("+
		PrivateNamesSQL+") ORDER BY last_seen DESC",
		HashRoot(s.Env.ProjectRoot()))
	if err != nil {
		return nil
	}
	defer rows.Close()
	now := timefmt.Now()
	out := []Sibling{}
	for rows.Next() {
		var sb Sibling
		var visibility string
		var lastSeen int64
		if err := rows.Scan(&sb.Name, &sb.RootID, &sb.About, &sb.Stack, &visibility,
			&sb.Memories, &lastSeen); err != nil {
			return out
		}
		// A 'listed' project withholds its spec, keeping only that it exists.
		if visibility == "listed" {
			sb.About, sb.Stack, sb.ListedOnly = "", "", true
		}
		sb.Synced = timefmt.HumanGap(now - lastSeen)
		out = append(out, sb)
	}
	return out
}

// RegisterProject writes this project's row in the shared index. It is the only write a
// project makes outside its own brain, and it carries no memory contents. The error matters
// to `private`, which must not report a declaration the index never received; brief ignores
// it, because a session must open without the index.
func (s *Store) RegisterProject() error {
	if !s.GlobalEnabled() || !s.IndexEnabled() {
		s.retractIndexRow()
		return nil
	}
	// A caller that named another project (over MCP) is not in that project's directory,
	// so the root this process sees is not the project's.
	if s.Env.Named {
		return nil
	}
	g := s.GlobalConnect(true)
	if g == nil {
		return fmt.Errorf("the shared index at %s could not be opened", s.Env.GlobalDB)
	}
	defer s.CloseGlobal(g)
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM memories WHERE project=? AND "+
		"superseded_by IS NULL", s.Env.Project).Scan(&n); err != nil {
		return err
	}
	s.PruneIndex(g)
	if err := s.bindRegistered(g); err != nil {
		return err
	}
	return s.upsertIndexRow(g, n)
}

// upsertIndexRow writes this project's row with its current spec and live memory count.
//
// Scope names are internal taxonomy and frequently sensitive, so the scopes column is
// written empty rather than filled and redacted later. does/provides/keywords are written
// and refreshed on conflict: leaving them out once meant `describe --does` reported success
// and never reached the index, so the one question the index exists to answer was asked
// against columns that stayed empty forever.
func (s *Store) upsertIndexRow(g *sql.DB, memories int) error {
	ctx := s.Env.Config.Context
	spec := s.Env.Config.Spec
	specUpdated, _ := strconv.ParseInt(spec["updated_at"], 10, 64)
	_, err := g.Exec("INSERT INTO projects(root_id,name,private,description,does,provides,keywords,"+
		"stack,status,visibility,scopes,memories,spec_updated_at,last_seen) "+
		"VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(root_id) DO UPDATE SET "+
		"name=excluded.name, private=excluded.private, description=excluded.description, "+
		"does=excluded.does, provides=excluded.provides, keywords=excluded.keywords, "+
		"stack=excluded.stack, status=excluded.status, visibility=excluded.visibility, "+
		"scopes=excluded.scopes, memories=excluded.memories, "+
		"spec_updated_at=excluded.spec_updated_at, last_seen=excluded.last_seen",
		HashRoot(s.Env.ProjectRoot()), s.Env.Project, BoolToInt(s.Env.Private), ctx["about"],
		spec["does"], spec["provides"], spec["keywords"], ctx["stack"], ctx["status"],
		s.Visibility(), "", memories, specUpdated, timefmt.Now())
	return err
}

// retractIndexRow removes this project's row from the shared index, and is what
// `"global": false` or `"index": false` means once the project has ALREADY registered.
// Opting out used to stop future writes and leave the existing row standing for the whole
// IndexUnseenDays window, so a project that had opted out still had its name, private flag
// and memory count in the spine. `publish --retract` closes the same gap for a lesson.
//
// It never creates the spine and never creates its schema: a project that wants no part of
// the shared layer must not bring one into existence on its way out. It deletes exactly one
// row, matched on this project's own root hash, and says so, because a retraction nobody
// sees is indistinguishable from the leak it fixed.
func (s *Store) retractIndexRow() {
	g := s.DB
	if !s.Env.Shared {
		if _, err := os.Stat(s.Env.GlobalDB); err != nil {
			return
		}
		db, err := openSpineWriter(s.Env.GlobalDB)
		if err != nil {
			return
		}
		defer db.Close()
		g = db
	}
	mine := HashRoot(s.Env.ProjectRoot())
	var n int
	if err := g.QueryRow("SELECT COUNT(*) FROM projects WHERE root_id=?", mine).Scan(&n); err != nil || n == 0 {
		return
	}
	if _, err := g.Exec("DELETE FROM projects WHERE root_id=?", mine); err != nil {
		return
	}
	diag.Warn("this project has opted out of the shared index; its row there has been removed")
}

// IndexUnseenDays is how long a project may go without registering before its row is
// dropped. Every install registers itself, throwaway ones included, so without pruning the
// index fills with projects that no longer exist and the awareness it provides becomes
// noise. A project that still exists opens a session eventually. Six months is deliberately
// long: a real project can go quiet for a season, and a dead row costs one line in a list.
const IndexUnseenDays = 180

// PruneIndex drops the rows not seen for IndexUnseenDays and returns how many went.
func (s *Store) PruneIndex(g *sql.DB) int {
	cutoff := timefmt.Now() - int64(IndexUnseenDays)*86400
	res, err := g.Exec("DELETE FROM projects WHERE last_seen < ?", cutoff)
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return int(n)
}

// IndexEnabled: a project can keep itself out of the shared index entirely with
// "index": false in brain.json. Default on, because an index nobody is in answers no
// questions, and the row carries no filesystem path.
func (s *Store) IndexEnabled() bool {
	if s.Env.Config.Index == nil {
		return true
	}
	return *s.Env.Config.Index
}

// Visibility is how much of this project's spec the index shares: "described" (all of it)
// or "listed" (only that the project exists).
func (s *Store) Visibility() string {
	if s.Env.Config.Spec != nil {
		if v := strings.ToLower(strings.TrimSpace(s.Env.Config.Spec["visibility"])); v == "listed" || v == "described" {
			return v
		}
	}
	return "described"
}
