package dump

import (
	"database/sql"
	"sort"

	"github.com/chka/os-brain/internal/fsguard"
	"github.com/chka/os-brain/internal/store"
)

// ProjectTables reports every table in this database carrying a project column, read from
// the schema rather than written down. A hardcoded list is a list somebody forgets to
// extend, and here the cost of forgetting is rows left behind by a command whose whole
// claim is that nothing is left behind.
func (s *Store) ProjectTables() ([]string, error) {
	names, err := s.TableNames()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, t := range names {
		cols, err := s.ColumnsOf(t)
		if err != nil {
			return nil, err
		}
		if cols["project"] {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out, nil
}

// DumpProject writes this project's rows from every project-scoped table. Deliberately not
// Backup: this dump protects a delete that only ever touches this project's rows, so it
// holds exactly those and nothing of the spine or of any other project.
func (s *Store) DumpProject(out fsguard.Dump) (map[string]int, error) {
	tables, err := s.ProjectTables()
	if err != nil {
		return nil, err
	}
	dump := s.newDump()
	counts := map[string]int{}
	for _, t := range tables {
		rows, err := s.rowsOrdered("SELECT * FROM "+t+" WHERE project=?", s.Env.Project)
		if err != nil {
			return nil, err
		}
		dump[t] = rows
		counts[t] = len(rows)
	}
	if err := writeDumpFile(&out, dump, tables); err != nil {
		return nil, err
	}
	return counts, nil
}

func SumCounts(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// PurgeProject deletes this project's rows from the named tables in one transaction. It
// drops the search-index entry of every memory it removes first: the index is keyed by
// memory id and shared with every other project in the file, so an entry left behind
// matches a memory that no longer exists, a corrupt search result in somebody else's brain.
// each, when not nil, runs per memory before the delete, for a caller that wants history
// or tombstones written.
func (s *Store) PurgeProject(tables []string, each func(*sql.Tx, *store.Memory) error) (map[string]int, error) {
	rows, err := s.QueryMemories("SELECT "+store.MemoryCols+", 0.0 AS bm FROM memories m "+
		"WHERE project=? ORDER BY id", s.Env.Project)
	if err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, m := range rows {
		if each != nil {
			if err := each(tx, m); err != nil {
				return nil, err
			}
		}
		if err := s.FTSDel(tx, m.ID); err != nil {
			return nil, err
		}
	}
	deleted := map[string]int{}
	for _, t := range tables {
		res, err := tx.Exec("DELETE FROM "+t+" WHERE project=?", s.Env.Project)
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		deleted[t] = int(n)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return deleted, nil
}
