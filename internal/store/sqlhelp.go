package store

import (
	"database/sql"
	"sort"
	"strconv"
	"strings"
)

// Querier is whatever can run a query: the store's own handle or an open transaction.
// Reads issued inside a write transaction MUST use the transaction, because the store
// keeps one connection and the write holds it.
type Querier interface {
	Query(string, ...interface{}) (*sql.Rows, error)
}

func (s *Store) QueryMemories(q string, args ...interface{}) ([]*Memory, error) {
	return ScanMemories(s.DB, q, args...)
}

func ScanMemories(db Querier, q string, args ...interface{}) ([]*Memory, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Initialised, not nil: every one of these lists reaches JSON, and a nil slice
	// marshals to null where callers expect [].
	out := []*Memory{}
	for rows.Next() {
		m, err := ScanMemory(rows, true)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ByID is Readable with "not here" as nil rather than an error, for the internal walks
// (a supersession chain, a restore) that treat a missing row as damage to report.
func (s *Store) ByID(id int64, allProjects bool) (*Memory, error) {
	m, err := s.Readable(id, allProjects)
	if err != nil && err.Error() == NoMemory(id).Error() {
		return nil, nil
	}
	return m, err
}

func Placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func ToIfaces(ss []string) []interface{} {
	out := make([]interface{}, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func SortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func JoinSorted(m map[string]bool) string { return strings.Join(SortedKeys(m), ", ") }

func SortedStrings(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func SortedIfaceKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func Contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func BoolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func Round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

func Str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (s *Store) Count(q string, args ...interface{}) (int, error) {
	var n int
	err := s.DB.QueryRow(q, args...).Scan(&n)
	return n, err
}

// MetaStr reads a value from the store's meta table, or "" when it is not set.
func (s *Store) MetaStr(key string) string {
	var v string
	if err := s.DB.QueryRow("SELECT v FROM meta WHERE k=?", key).Scan(&v); err != nil {
		return ""
	}
	return v
}

// MetaInt reads a numeric meta value, or 0 when it is unset or unreadable.
func (s *Store) MetaInt(key string) int64 {
	// `env` builds a Store with no database (EnvBlockFor): the clock, the paths and git
	// are this machine's to answer, and there is no last-session cursor to read.
	if s.DB == nil {
		return 0
	}
	n, _ := strconv.ParseInt(s.MetaStr(key), 10, 64)
	return n
}

func QuoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func QuoteAll(cols []string) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = QuoteIdent(c)
	}
	return out
}

// TableNames is every table a backup covers: the schema's tables minus SQLite's own, the
// search index (rebuilt from memories) and BackupSkip.
func (s *Store) TableNames() ([]string, error) {
	rows, err := s.DB.Query("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		if strings.HasPrefix(n, "mem_fts") || strings.HasPrefix(n, "sqlite_") || BackupSkip[n] {
			continue
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ColumnsOf is the set of column names of a table in this store.
func (s *Store) ColumnsOf(table string) (map[string]bool, error) {
	rows, err := s.DB.Query("PRAGMA table_info(" + table + ")")
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

// BackupSkip are the tables a backup legitimately leaves out. Everything else in the
// schema is dumped, so a new table is covered the day it is added rather than the day
// somebody notices it was missing.
var BackupSkip = map[string]bool{"meta": true, "gmeta": true, "declined": true}

// TableColumns is the whitelist a row's keys are checked against: the columns this store
// actually has. Keys are compared, never spliced into SQL.
func TableColumns(db Querier, table string) (map[string]bool, error) {
	rows, err := db.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		cols[n] = true
	}
	return cols, rows.Err()
}
