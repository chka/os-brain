package brain

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// Store is one open brain.db and the environment (project, paths, config) it was opened in.
type Store struct {
	DB  *sql.DB
	Env Env
}

// Memory is one row of the memories table. Nullable columns are pointers so "never
// verified" stays distinguishable from "verified at the epoch".
type Memory struct {
	ID           int64   `json:"id"`
	Scope        string  `json:"scope"`
	MemType      string  `json:"mem_type"`
	Title        string  `json:"title"`
	Body         string  `json:"body"`
	Source       string  `json:"source"`
	Importance   int     `json:"importance"`
	Pinned       int     `json:"pinned"`
	UseCount     int     `json:"use_count"`
	LastUsed     *int64  `json:"last_used"`
	Project      string  `json:"project"`
	SupersededBy *int64  `json:"superseded_by"`
	HappenedOn   string  `json:"happened_on"`
	SnoozeUntil  *int64  `json:"snooze_until"`
	ReviewOn     *int64  `json:"review_on"`
	Confidence   string  `json:"confidence"`
	SourceDate   string  `json:"source_date"`
	VerifiedAt   *int64  `json:"verified_at"`
	SourceCommit string  `json:"source_commit"`
	Trust        string  `json:"trust"`
	ValidFrom    string  `json:"valid_from"`
	ValidTo      string  `json:"valid_to"`
	Session      string  `json:"session"`
	Hash         *string `json:"hash"`
	CreatedAt    int64   `json:"created_at"`
	UpdatedAt    int64   `json:"updated_at"`

	// The fields below are set by recall and never stored.

	// BM is the engine's raw relevance. It is emitted (as "bm") only on recall rows, which
	// InRecall marks: recall's JSON has always carried a bm key and get's never has, and
	// consumers of each already parse exactly that shape.
	BM       float64    `json:"-"`
	Cov      int        `json:"-"` // query terms this row holds; see CoverageFloor
	Via      []ViaRef   `json:"matched_via,omitempty"`
	InRecall bool       `json:"-"`
	Score    float64    `json:"score,omitempty"`
	Matched  bool       `json:"matched,omitempty"`
	Loose    bool       `json:"loose,omitempty"`
	Tier     string     `json:"tier,omitempty"`
	Fresh    *Freshness `json:"freshness,omitempty"`
	Conflict *Conflict  `json:"conflict,omitempty"`
}

// memoryAlias exists only so MarshalJSON can reuse the struct tags without recursing.
type memoryAlias Memory

// MarshalJSON adds two keys the struct tags cannot express.
//
// The type travels under both "mem_type" and "type". This store has always said mem_type
// and ns-brain's team server has always said type, so a consumer written against one read
// nothing from the other. Emitting both keeps one wire contract whichever brain answers.
func (m Memory) MarshalJSON() ([]byte, error) {
	base, err := json.Marshal(memoryAlias(m))
	if err != nil {
		return nil, err
	}
	extra := `,"type":` + string(jsonOrNull(m.MemType))
	if m.InRecall {
		extra += `,"bm":` + string(jsonOrNull(m.BM))
	}
	return append(append(base[:len(base)-1], []byte(extra)...), '}'), nil
}

func jsonOrNull(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("null")
	}
	return b
}

// memoryCols is the column list scanMemory expects, in its order.
const memoryCols = `id, scope, mem_type, title, body, source, importance, pinned,
	use_count, last_used, project, superseded_by, happened_on, snooze_until, review_on,
	confidence, source_date, verified_at, source_commit, trust, valid_from, valid_to,
	session, hash, created_at, updated_at`

// scanMemory reads one row selected with memoryCols, plus a trailing bm column when withBM
// is set (recall's queries select one).
func scanMemory(s interface {
	Scan(...interface{}) error
}, withBM bool) (*Memory, error) {
	var m Memory
	dest := []interface{}{&m.ID, &m.Scope, &m.MemType, &m.Title, &m.Body, &m.Source,
		&m.Importance, &m.Pinned, &m.UseCount, &m.LastUsed, &m.Project, &m.SupersededBy,
		&m.HappenedOn, &m.SnoozeUntil, &m.ReviewOn, &m.Confidence, &m.SourceDate,
		&m.VerifiedAt, &m.SourceCommit, &m.Trust, &m.ValidFrom, &m.ValidTo, &m.Session,
		&m.Hash, &m.CreatedAt, &m.UpdatedAt}
	if withBM {
		dest = append(dest, &m.BM)
	}
	if err := s.Scan(dest...); err != nil {
		return nil, err
	}
	return &m, nil
}

// --- opening a store -------------------------------------------------------------

// Open connects, builds the schema if the file is new, and migrates an older one
// forward. It never migrates downward and never runs destructive DDL: an install's
// brain.db is the only copy of that project's memory.
func Open(env Env) (*Store, error) { return open(env) }

// dsnFor is the connection string for a brain file.
//
// _txlock=immediate is load-bearing, not a tuning knob. Every write path in this codebase
// reads before it writes (own(), the near-duplicate scan, the hash lookup), so a deferred
// BEGIN starts the transaction as a reader and has to upgrade on the first write. SQLite
// answers that upgrade with SQLITE_BUSY_SNAPSHOT immediately and deliberately does not
// apply busy_timeout to it, because the reader's snapshot is already stale and waiting
// cannot fix it. Measured before this existed: 20 concurrent `remember` calls from separate
// processes, 12 lost, each failing in 31ms against a 30-second timeout that was never
// consulted. Taking the write lock up front is what makes busy_timeout mean anything.
func dsnFor(path string) string {
	return SQLiteURI(path, "_pragma=busy_timeout(30000)&_txlock=immediate")
}

// SQLiteURI is a path as a SQLite file: URI. SQLite reads the name as a URI, so a `?` began
// the query and a `#` a fragment: a project in c#x kept its brain in a file called c,
// shared with every c#..., and restore verified x.db while copying the literal x.db?y.
// Percent-encoding the three characters URI parsing gives meaning to is all a path needs;
// SQLite decodes them back.
func SQLiteURI(path, query string) string {
	path = strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23").Replace(path)
	if query == "" {
		return "file:" + path
	}
	return "file:" + path + "?" + query
}

func open(env Env) (*Store, error) {
	if env.ConfigErr != nil {
		return nil, env.ConfigErr
	}
	if err := os.MkdirAll(filepath.Dir(env.DBPath), 0o700); err != nil {
		return nil, err
	}
	_, statErr := os.Stat(env.DBPath)
	first := os.IsNotExist(statErr)

	if env.DBPath != ":memory:" {
		if err := precreatePrivate(env.DBPath); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", dsnFor(env.DBPath))
	if err != nil {
		return nil, err
	}
	// One connection: SQLite writes serialise anyway, and a pool turns "database is
	// locked" from a retryable wait into an error surfaced mid-transaction.
	db.SetMaxOpenConns(1)

	s := &Store{DB: db, Env: env}
	// First touch is exactly when several processes collide: one developer with four
	// projects and a few sessions each opens the same brand-new brain from a dozen
	// directions at once. PRAGMA journal_mode takes a brief exclusive lock and is documented
	// NOT to honour busy_timeout, and DDL under a concurrent schema build hits the same wall,
	// so the retry has to live here rather than being delegated to SQLite. Measured: without
	// it, ten processes opening one new brain lost a write two runs in three.
	if err := retryBusy(s.prepareSchema); err != nil {
		return nil, err
	}
	_ = os.Chmod(env.DBPath, 0o600)
	if err := retryBusy(s.checkIdentity); err != nil {
		db.Close()
		return nil, err
	}
	if first && s.claimCreation() {
		fmt.Fprintf(os.Stderr, "[brain] initialized new database at %s\n", env.DBPath)
	}
	return s, nil
}

// prepareSchema is everything a connection does before it is usable: WAL mode, the schema,
// the migration of an older file, then the indexes (which need the migrated columns).
func (s *Store) prepareSchema() error {
	if err := s.ensureWAL(); err != nil {
		return err
	}
	if err := s.ensureSchema(); err != nil {
		return err
	}
	if err := s.migrate(); err != nil {
		return err
	}
	for _, stmt := range indexes {
		if _, err := s.DB.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureWAL() error {
	var mode string
	if err := s.DB.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		return err
	}
	if strings.EqualFold(mode, "wal") {
		return nil
	}
	_, err := s.DB.Exec("PRAGMA journal_mode=WAL")
	return err
}

func (s *Store) ensureSchema() error {
	for _, stmt := range splitStatements(schemaDDL) {
		if _, err := s.DB.Exec(stmt); err != nil {
			return fmt.Errorf("schema: %w", err)
		}
	}
	_, err := s.DB.Exec(ftsDDL)
	return err
}

// claimCreation decides which process announces a new database. Every process in a startup
// race stats a missing file before opening it, so all of them think they created it.
// Measured: twelve sessions opening one new shared store printed nine "initialized new
// database" lines, and in a SessionStart hook that is nine junk lines in somebody's
// context. A write only one racer can win settles it. If the claim cannot be made at all,
// the process still announces, as it would have before the claim existed.
func (s *Store) claimCreation() bool {
	if _, err := s.DB.Exec("INSERT OR IGNORE INTO meta(k,v) VALUES('created_by',?)",
		creationToken()); err != nil {
		return true
	}
	var winner string
	if s.DB.QueryRow("SELECT v FROM meta WHERE k='created_by'").Scan(&winner) == nil &&
		winner != creationToken() {
		return false
	}
	return true
}

func (s *Store) Close() error { return s.DB.Close() }

// --- full-text index -------------------------------------------------------------
//
// The index holds accent-folded text (see ftsDDL for why it is written in code rather than
// by a trigger). Every write goes through these functions.

func (s *Store) ftsPut(tx *sql.Tx, id int64, title, body string) error {
	if err := s.ftsDel(tx, id); err != nil {
		return err
	}
	return ftsInsert(tx, id, title, body)
}

func (s *Store) ftsDel(tx *sql.Tx, id int64) error {
	_, err := tx.Exec("DELETE FROM mem_fts WHERE rowid=?", id)
	return err
}

func ftsInsert(tx *sql.Tx, id int64, title, body string) error {
	_, err := tx.Exec("INSERT INTO mem_fts(rowid, title, body) VALUES(?,?,?)",
		id, StripAccents(title), StripAccents(body))
	return err
}

// Reindex rebuilds the whole index from the memories table. Needed whenever the folding
// rule changes, because the stored tokens are folded at write time.
func (s *Store) Reindex() error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.reindexTx(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// reindexTx is Reindex inside a transaction the caller owns, so a migration can rebuild the
// index atomically with the rest of its steps.
func (s *Store) reindexTx(tx *sql.Tx) error {
	if _, err := tx.Exec("DELETE FROM mem_fts"); err != nil {
		return err
	}
	docs, err := allTitlesAndBodies(tx)
	if err != nil {
		return err
	}
	for _, d := range docs {
		if err := ftsInsert(tx, d.id, d.title, d.body); err != nil {
			return err
		}
	}
	return nil
}

type ftsDoc struct {
	id          int64
	title, body string
}

// allTitlesAndBodies reads every memory's text up front: the inserts that follow run on the
// same single connection, which cannot interleave them with an open result set.
func allTitlesAndBodies(tx *sql.Tx) ([]ftsDoc, error) {
	rows, err := tx.Query("SELECT id, title, body FROM memories")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var docs []ftsDoc
	for rows.Next() {
		var d ftsDoc
		if err := rows.Scan(&d.id, &d.title, &d.body); err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	return docs, nil
}

// --- ownership and filtering -----------------------------------------------------

// Own resolves an id that belongs to this project, for any action including a read. It is
// the one by-id accessor for memories: an id that does not exist, one belonging to another
// project and one belonging to a private project all get the same answer, so the error is
// not an oracle for what other projects hold. A refusal that named the owning project once
// let any caller enumerate every project label in the store, private ones included. The
// action argument is accepted for the call sites' readability and does not change the answer.
func (s *Store) Own(id int64, action string) (*Memory, error) {
	return s.Readable(id, false)
}

// NoMemory is the one answer for an id this caller may not see.
func NoMemory(id int64) error { return fmt.Errorf("no memory #%d in this project", id) }

// Readable resolves an id for reading. crossProject is the caller explicitly asking to read
// beyond this project (`get --all-projects`); even then only a project that is readable by
// the same rule as recall --all-projects, never a private one, never one that has not
// registered.
func (s *Store) Readable(id int64, crossProject bool) (*Memory, error) {
	row := s.DB.QueryRow("SELECT "+memoryCols+" FROM memories WHERE id=?", id)
	m, err := scanMemory(row, false)
	if err == sql.ErrNoRows {
		return nil, NoMemory(id)
	}
	if err != nil {
		return nil, err
	}
	if m.Project == "" || m.Project == s.Env.Project {
		return m, nil
	}
	if crossProject && contains(s.readableProjects(), m.Project) {
		return m, nil
	}
	return nil, NoMemory(id)
}

// ProjectFilter confines every read to this project unless something asks otherwise. One
// database holds every project, so this filter is what keeps them apart, and it has to be
// the default rather than something each call site remembers.
func (s *Store) ProjectFilter(alias string, allProjects bool) (string, []interface{}) {
	col := "project"
	if alias != "" {
		col = alias + ".project"
	}
	if !allProjects {
		return col + " = ?", []interface{}{s.Env.Project}
	}
	// --all-projects widens on purpose, but not into a project that asked to be left out.
	// Sharing a database and being readable by everything in it are different questions: a
	// flag with no per-project opt-out would let any project read any other's.
	//
	// It is an ALLOW-LIST, not a deny-list, and that distinction is the whole guard. A
	// deny-list asks "is this project marked private?", which is silently false for any
	// project that has not yet written its row - and a project only writes that row when
	// it runs brief or describe, so a store somebody just absorbed is unprotected exactly
	// while nobody has looked at it. Naming who may be read fails closed instead: an
	// unregistered project is not shown, which is the same rule as everywhere else here.
	// Your own rows are always yours, whatever anybody declared.
	readable := s.readableProjects()
	if len(readable) == 0 {
		return col + " = ?", []interface{}{s.Env.Project}
	}
	args := []interface{}{s.Env.Project}
	for _, p := range readable {
		args = append(args, p)
	}
	return "(" + col + " = ? OR " + col + " IN (" + placeholders(len(readable)) + "))", args
}

// readableProjects is every project in the shared index that has NOT declared itself
// private. A project cannot read another's brain.json, so the declaration has to live in
// the index; anything the index does not know about is left out rather than assumed open.
//
// Only the row from the directory the name is bound to counts, and a private declaration
// anywhere wins: otherwise a second directory registering the same name with private=0, or
// the row a project left behind before it moved, would put a private project back on the
// list.
func (s *Store) readableProjects() []string {
	g := s.globalConnect(false)
	if g == nil {
		return nil
	}
	defer s.closeGlobal(g)
	rows, err := g.Query("SELECT DISTINCT p.name FROM projects p JOIN project_ids b "+
		"ON b.name = p.name AND b.root_id = p.root_id WHERE p.private = 0 AND b.private = 0 "+
		"AND p.name <> ? AND p.name NOT IN ("+privateNamesSQL+")", s.Env.Project)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return out
		}
		out = append(out, n)
	}
	return out
}

// ScopeFilter: a scope sees its ancestors and global, never its children. A scope
// containing * or ? is treated as a glob instead.
func ScopeFilter(scope, alias string) (string, []interface{}) {
	col := alias + ".scope"
	if scope == "" || scope == "global" {
		return col + " = 'global'", nil
	}
	if strings.ContainsAny(scope, "*?") {
		return "(" + col + " GLOB ? OR " + col + " = 'global')", []interface{}{scope}
	}
	chain := ScopeAncestors(scope)
	return col + " IN (" + placeholders(len(chain)) + ")", toIfaces(chain)
}

// ScopeMatches is ScopeFilter for one row, for a caller holding rows rather than SQL.
func ScopeMatches(scope, rowScope string) bool {
	if scope == "" || scope == "global" {
		return rowScope == "global"
	}
	if strings.ContainsAny(scope, "*?") {
		return sqlGlob([]rune(scope), []rune(rowScope)) || rowScope == "global"
	}
	return contains(ScopeAncestors(scope), rowScope)
}

// sqlGlob is SQLite's GLOB for * and ?, the two a scope pattern uses: * matches any run,
// separators included, which path.Match does not do for a slash.
func sqlGlob(p, s []rune) bool {
	for len(p) > 0 {
		switch p[0] {
		case '*':
			for i := 0; i <= len(s); i++ {
				if sqlGlob(p[1:], s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if len(s) == 0 {
				return false
			}
		default:
			if len(s) == 0 || s[0] != p[0] {
				return false
			}
		}
		p, s = p[1:], s[1:]
	}
	return len(s) == 0
}

// tableExists reports whether this brain.db has the table; an older file may not.
func (s *Store) tableExists(name string) bool {
	var one int
	err := s.DB.QueryRow("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?",
		name).Scan(&one)
	return err == nil
}

// HappenedSQL is the date a memory is "about": what happened, falling back to when it
// was written down.
func HappenedSQL(alias string) string {
	return fmt.Sprintf("COALESCE(NULLIF(%s.happened_on, ''), date(%s.created_at, 'unixepoch', 'localtime'))", alias, alias)
}

// archive keeps the prior version of anything about to be destroyed.
func (s *Store) archive(tx *sql.Tx, m *Memory, action, reason string) error {
	project := m.Project
	if project == "" {
		project = s.Env.Project
	}
	// Placeholders are counted, not written out: a literal list silently went one short the
	// moment historyCols grew, and a miscounted INSERT fails only on the archive path, which
	// is exactly where the prior version of a memory is meant to be preserved.
	vals := []interface{}{project, m.ID, m.Scope, m.MemType, m.Title, m.Body, m.Source,
		m.SourceDate, m.SourceCommit, m.Confidence, m.Importance, m.Pinned, m.SupersededBy,
		action, reason, Now()}
	_, err := tx.Exec("INSERT INTO mem_history(project,mem_id,"+strings.Join(historyCols, ",")+
		",action,reason,archived_at) VALUES("+placeholders(len(vals))+")", vals...)
	return err
}
