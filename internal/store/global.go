package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/chka/os-brain/internal/diag"
	"github.com/chka/os-brain/internal/fsguard"
)

// The spine is the one SQLite file every project on this machine shares. It holds the
// axioms, the house standards, the project index, notes between projects, published
// lessons and the release register. A project's own memories never go into it.
//
// This file opens the spine and keeps its schema current. The tables are described in
// globalSchemaDDL, the migrations live in global_migrate.go and the default axioms in
// global_axioms.go.

// GlobalSchemaVersion is the spine's shape, separate from SchemaVersion. globalConnect
// refuses a spine newer than this and falls back to the built-in axioms, rather than
// reading a schema it does not understand.
const GlobalSchemaVersion = 15

// spineUnavailable is the warning for every way the spine can fail to open. Opening it is
// never fatal, but it is never silent either: falling back to the built-in axioms without
// saying so is how a brief comes out looking complete when it is not.
const spineUnavailable = "global brain unavailable (%v); axioms fall back to the built-in set"

// spineWriterParams: the spine is the one file every project touches. One developer with
// four projects and a few sessions each is a dozen processes on this database, where a
// project store only sees that project's own sessions. So it takes the immediate write
// lock (a deferred BEGIN would have to upgrade from reader to writer and can fail with
// SQLITE_BUSY at once) and waits up to ten seconds for a busy lock.
const spineWriterParams = "_pragma=busy_timeout(10000)&_txlock=immediate"

// HashRoot identifies a project directory without storing where it is. The index needs
// identity (is this row mine, is that name a live project elsewhere) and never the path
// itself. A path in the spine would put one client's directory layout in a file that every
// other client's sessions on the same machine open.
//
// Symlinks are resolved first so two spellings of one directory hash the same, which is
// what Realpath() already does everywhere identity is compared.
func HashRoot(path string) string {
	if path == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(fsguard.Realpath(path)))
	return hex.EncodeToString(sum[:])
}

func (s *Store) GlobalEnabled() bool {
	return s.Env.Config.Global == nil || *s.Env.Config.Global
}

// GlobalConnect opens the spine, creating it when create is set. It returns nil whenever
// the spine is missing, unreadable or opted out of: a project must keep working without
// it, just without the index, the notes and the custom axioms.
func (s *Store) GlobalConnect(create bool) *sql.DB {
	if !s.GlobalEnabled() {
		return nil
	}
	if !s.spineParentReady(create) {
		return nil
	}
	if _, err := os.Stat(s.Env.GlobalDB); os.IsNotExist(err) && !create {
		return nil
	}
	// The project store may be the spine file itself, and a second handle would then
	// deadlock against our own open write transaction.
	if s.Env.Shared {
		if err := s.ensureGlobalSchema(s.DB); err != nil {
			return nil
		}
		return s.DB
	}
	if err := fsguard.PrecreatePrivate(s.Env.GlobalDB); err != nil {
		diag.Warn(spineUnavailable, err)
		return nil
	}
	g, err := openSpineWriter(s.Env.GlobalDB)
	if err != nil {
		diag.Warn(spineUnavailable, err)
		return nil
	}
	if err := RetryBusy(func() error { return s.ensureGlobalSchema(g) }); err != nil {
		diag.Warn(spineUnavailable, err)
		g.Close()
		return nil
	}
	_ = os.Chmod(s.Env.GlobalDB, 0o600)
	return g
}

// spineParentReady reports whether the spine's directory exists, making it (owner-only)
// when the caller is allowed to create the spine.
func (s *Store) spineParentReady(create bool) bool {
	parent := filepath.Dir(s.Env.GlobalDB)
	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		return true
	}
	if !create {
		return false
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		diag.Warn(spineUnavailable, err)
		return false
	}
	return true
}

// openSpineWriter opens the spine file with a single connection, so every statement on
// the handle shares one SQLite lock.
func openSpineWriter(path string) (*sql.DB, error) {
	g, err := sql.Open("sqlite", SQLiteURI(path, spineWriterParams))
	if err != nil {
		return nil, err
	}
	g.SetMaxOpenConns(1)
	return g, nil
}

// CloseGlobal closes a handle from globalConnect, unless it is the project store itself.
func (s *Store) CloseGlobal(g *sql.DB) {
	if g != nil && g != s.DB {
		g.Close()
	}
}

// WritableSpine is globalConnect(true) for callers that need an error to return.
func (s *Store) WritableSpine() (*sql.DB, error) {
	g := s.GlobalConnect(true)
	if g == nil {
		return nil, fmt.Errorf("the shared store is not writable")
	}
	return g, nil
}

// ensureGlobalSchema creates whatever is missing, migrates an older spine forward and
// makes sure the default axioms are present. It runs on every open.
func (s *Store) ensureGlobalSchema(g *sql.DB) error {
	if err := createSpineTables(g); err != nil {
		return err
	}
	have := spineVersionIn(g)
	if have > GlobalSchemaVersion {
		return s.checkNewerSpine(g, have)
	}
	if have < GlobalSchemaVersion {
		if err := migrateSpineInOneTransaction(g, have); err != nil {
			return err
		}
	}
	if need, known := recordedMinClient(g, "gmeta"); !known || need != MinCompatibleGlobalSchema {
		if err := stampCompat(g, "gmeta", MinCompatibleGlobalSchema); err != nil {
			return err
		}
	}
	if err := seedDefaultAxioms(g); err != nil {
		return err
	}
	if err := RetireDefaultAxioms(g); err != nil {
		return err
	}
	return OfferDefaultAxioms(g)
}

func createSpineTables(g *sql.DB) error {
	for _, stmt := range SplitStatements(GlobalSchemaDDL) {
		if _, err := g.Exec(stmt); err != nil {
			return err
		}
	}
	if _, err := g.Exec(globalFTSDDL); err != nil {
		return err
	}
	_, err := g.Exec(ProjectIDsDDL)
	return err
}

// spineVersionIn is the schema version the spine records, or 0 when it records none.
func spineVersionIn(g queryer) int {
	var raw string
	if err := g.QueryRow("SELECT v FROM gmeta WHERE k='schema_version'").Scan(&raw); err != nil {
		return 0
	}
	v, _ := strconv.Atoi(raw)
	return v
}

// checkNewerSpine decides whether this binary may use a spine written by a newer one. It
// may when the spine says, through its compatibility register (compat.go), that clients
// of this version are still welcome. The spine is the file every project opens, so the
// strict rule bites hardest here: refusing every newer spine meant upgrading one project
// silently cut every other one off from its axioms, and the built-in fallback looks
// identical to the real thing.
func (s *Store) checkNewerSpine(g *sql.DB, have int) error {
	need, known := recordedMinClient(g, "gmeta")
	if known && GlobalSchemaVersion >= need {
		return nil
	}
	diag.Warn("the shared spine is schema v%d; this brain understands v%d. Upgrade %s "+
		"before relying on axioms, standards or notes from it:\n"+
		"  install the new release from https://github.com/chka/os-brain/releases",
		have, GlobalSchemaVersion, s.Env.Here)
	return fmt.Errorf("spine schema v%d is newer than v%d", have, GlobalSchemaVersion)
}

// migrateSpineInOneTransaction runs every pending migration and stamps the new version
// together. A half-migrated spine fails on every later open, and the spine holds every
// project's axioms, index and name bindings.
func migrateSpineInOneTransaction(g *sql.DB, have int) error {
	tx, err := g.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := MigrateGlobal(tx, have); err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT OR REPLACE INTO gmeta(k,v) VALUES('schema_version',?)",
		strconv.Itoa(GlobalSchemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

// SpineSchemaVersion reads the spine's schema version read-only, without going through
// globalConnect, whose guard would refuse a newer spine and so hide the very number the
// caller wants to report.
func (s *Store) SpineSchemaVersion() int {
	if _, err := os.Stat(s.Env.GlobalDB); err != nil {
		return 0
	}
	g, err := sql.Open("sqlite", SQLiteURI(s.Env.GlobalDB, "mode=ro"))
	if err != nil {
		return 0
	}
	defer g.Close()
	return spineVersionIn(g)
}

// GMeta reads one key of the spine's own key/value table, or "" when there is none.
func (s *Store) GMeta(key string) string {
	g := s.GlobalConnect(false)
	if g == nil {
		return ""
	}
	defer s.CloseGlobal(g)
	var v string
	if err := g.QueryRow("SELECT v FROM gmeta WHERE k=?", key).Scan(&v); err != nil {
		return ""
	}
	return v
}

// sqlRunner is a spine handle or a transaction on one.
type sqlRunner interface {
	Exec(string, ...interface{}) (sql.Result, error)
	Query(string, ...interface{}) (*sql.Rows, error)
	QueryRow(string, ...interface{}) *sql.Row
}

// dbColumns is the set of column names a table has, empty when the table does not exist.
func dbColumns(g Querier, table string) (map[string]bool, error) {
	rows, err := g.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// SplitStatements splits a block of DDL into its non-empty statements, because the driver
// runs one statement per Exec.
func SplitStatements(ddl string) []string {
	var out []string
	for _, stmt := range splitOnSemicolon(ddl) {
		if strings.TrimSpace(stmt) != "" {
			out = append(out, stmt)
		}
	}
	return out
}

func (s *Store) TableExistsIn(g *sql.DB, name string) bool {
	var one int
	return g.QueryRow("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?",
		name).Scan(&one) == nil
}
