package brain

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

type PruneArgs struct {
	OlderThan   int
	Scope       string
	Tier        string
	Superseded  bool
	Apply       bool
	Yes         bool
	IMeanIt     bool
	MaxDelete   int
	Backup      string
	AllProjects bool
	JSON        bool
	// The dump's refusals, the same three backup and export take.
	Force        bool
	AllowOutside bool
	AllowTracked bool
}

// Prune deletes decayed or superseded memories. It is a dry run by default, dumps the
// brain before deleting, refuses tiers A and B, rejects a same-day cutoff and caps how
// many rows one run may delete. Every one of those guards is here because the widest
// possible action is the wrong default for a delete.
func (s *Store) Prune(w io.Writer, a PruneArgs) error {
	if a.MaxDelete <= 0 {
		a.MaxDelete = 25
	}
	f, err := s.pruneFilter(a)
	if err != nil {
		return err
	}
	where, params, pw, pp := f.where, f.params, f.projectWhere, f.projectParams

	// The current version of a retracted fact is never pruned. Deleting it once nulled its
	// predecessor's link and put the retracted fact back into recall, which forget refuses
	// for exactly that reason. Counted, so the dry run says what it kept.
	heads, err := s.count("SELECT COUNT(*) FROM memories WHERE "+strings.Join(where, " AND ")+
		" AND "+chainHeadSQL("memories"), params...)
	if err != nil {
		return err
	}
	where = append(where, "NOT "+chainHeadSQL("m"))
	rows, err := s.queryMemories("SELECT "+memoryCols+", 0.0 AS bm FROM memories m WHERE "+
		strings.Join(where, " AND ")+" ORDER BY updated_at", params...)
	if err != nil {
		return err
	}
	var lines []string
	if heads > 0 {
		lines = append(lines, fmt.Sprintf("kept %d that supersede another memory: pruning one "+
			"would bring back the fact it retracted (os-brain forget <id> --force decides that "+
			"one at a time)", heads))
	}
	for _, r := range rows {
		lines = append(lines, fmt.Sprintf("#%d [%s/%s] %s  (updated %s, used %dx)",
			r.ID, OneLine(r.Scope), r.MemType, OneLine(r.Title), Day(r.UpdatedAt), r.UseCount))
	}
	// The cap. "Half the brain" is half of what this prune can reach, not of the whole
	// shared store, where a small project's every decayed memory was always under half.
	var total int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM memories WHERE "+pw, pp...).Scan(&total); err != nil {
		return err
	}
	tooMany := len(rows) > a.MaxDelete || (total >= 10 && len(rows) > total/2)
	if a.Apply && !a.Yes && len(rows) > 0 && tooMany {
		return fmt.Errorf("refusing to delete %d of %d memories in one go.\nExport first "+
			"(os-brain export --out brain-backup.json), narrow it with --scope/--older-than, "+
			"or pass --yes if this is really intended", len(rows), total)
	}

	if a.Apply && len(rows) > 0 {
		backup, err := s.pruneBackup(a, pw, pp)
		if err != nil {
			return err
		}
		lines = append(lines, fmt.Sprintf("backed up the whole brain to %s first", backup))
		if err := s.deletePruned(rows, a.OlderThan); err != nil {
			return err
		}
		plural := "ies"
		if len(rows) == 1 {
			plural = "y"
		}
		lines = append(lines, fmt.Sprintf("\npruned %d memor%s", len(rows), plural))
	} else if len(rows) > 0 {
		lines = append(lines, fmt.Sprintf("\n%d memories would be pruned. Export first "+
			"(os-brain export --out brain-backup.json), then re-run with --apply.", len(rows)))
	}
	human := "(nothing to prune)"
	if len(rows) > 0 {
		human = strings.Join(lines, "\n")
	} else if heads > 0 {
		human = human + "\n" + strings.Join(lines, "\n")
	}
	if rows == nil {
		rows = []*Memory{}
	}
	return Out(w, a.JSON, human, map[string]interface{}{
		"count": len(rows), "applied": a.Apply, "memories": rows, "kept_heads": heads})
}

// pruneSelection is what a prune may reach: the conditions on a candidate row, and the
// project filter on its own for counting what the prune can see.
type pruneSelection struct {
	where         []string
	params        []interface{}
	projectWhere  string
	projectParams []interface{}
}

// pruneFilter turns the flags into the WHERE clause of what may be pruned, refusing the
// dangerous combinations first. Pinned memories are never candidates.
func (s *Store) pruneFilter(a PruneArgs) (*pruneSelection, error) {
	if a.OlderThan < 1 {
		return nil, fmt.Errorf("--older-than must be at least 1 day: a same-day cutoff " +
			"deletes memories written during this session")
	}
	if t := strings.ToUpper(a.Tier); (t == "A" || t == "B") && !a.IMeanIt {
		// Name the tier that was actually asked for. This once printed the union of A and
		// B either way, so somebody checking whether they had the right tier was told
		// about types that are not in it.
		return nil, fmt.Errorf("tier %s never decays: %s. Pruning it deletes the "+
			"load-bearing part of the brain. Pass --i-mean-it if that is really the intent.",
			t, strings.Join(sortedKeys(tierTypes(t)), ", "))
	}
	if a.AllProjects && !a.IMeanIt {
		return nil, fmt.Errorf("--all-projects deletes from every project in the store. " +
			"Add --i-mean-it if that is really the intent.")
	}
	cutoff := Now() - int64(a.OlderThan)*86400
	pw, pp := s.ProjectFilter("", a.AllProjects)
	where := []string{pw, "pinned=0", "updated_at < ?"}
	params := append(append([]interface{}{}, pp...), cutoff)
	if a.Scope != "" {
		where = append(where, "scope = ?")
		params = append(params, a.Scope)
	}
	switch {
	case a.Superseded:
		where = append(where, "superseded_by IS NOT NULL")
	case a.Tier != "":
		types := tierTypes(strings.ToUpper(a.Tier))
		if types == nil {
			return nil, fmt.Errorf("--tier must be one of A B C D")
		}
		names := sortedKeys(types)
		where = append(where, "mem_type IN ("+placeholders(len(names))+")")
		params = append(params, toIfaces(names)...)
	default:
		return nil, fmt.Errorf("say what to prune: --tier D (decayed ephemera) or --superseded")
	}
	return &pruneSelection{where: where, params: params, projectWhere: pw, projectParams: pp}, nil
}

// pruneBackup dumps everything the prune can reach before anything is deleted, without
// being asked to, and returns where it went. It covers the same projects the delete
// reaches: with --all-projects the backup once left out the other projects' rows that the
// delete then removed.
func (s *Store) pruneBackup(a PruneArgs, pw string, pp []interface{}) (string, error) {
	backup := a.Backup
	if backup == "" {
		backup = filepath.Join(s.Env.Here,
			"brain-prune-"+time.Now().Format("20060102-150405")+".json")
	}
	out := Dump{Path: backup, Root: s.Env.ProjectRoot(), Marker: BackupMarker,
		Force: a.Force, AllowOutside: a.AllowOutside, AllowTracked: a.AllowTracked}
	if err := out.Check(); err != nil {
		return "", err
	}
	dump := s.newDump()
	tables := []string{"memories", "events", "leads"}
	for _, t := range tables {
		got, err := s.rowsOrdered("SELECT * FROM "+t+" WHERE "+pw+" ORDER BY id", pp...)
		if err != nil {
			return "", err
		}
		dump[t] = got
	}
	if err := writeDumpFile(&out, dump, tables); err != nil {
		return "", err
	}
	return backup, nil
}

// deletePruned archives, tombstones and deletes each row in one transaction. The
// tombstone stops a pruned fact being relearned as new; re-pointing the links that named a
// pruned row as their successor keeps every chain unbroken.
func (s *Store) deletePruned(rows []*Memory, olderThan int) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range rows {
		// Filed under the row's own project, so its owner is the one told it was pruned.
		owner := r.Project
		if owner == "" {
			owner = s.Env.Project
		}
		if err := s.archive(tx, r, "prune", fmt.Sprintf("tier %s, older than %dd",
			TierOf(r.MemType), olderThan)); err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT OR REPLACE INTO tombstones(hash,project,scope,title,"+
			"reason,forgotten_at) VALUES(?,?,?,?,?,?)",
			ContentHash(owner, r.Scope, r.Title), owner, r.Scope, r.Title,
			"pruned", Now()); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM memories WHERE id=?", r.ID); err != nil {
			return err
		}
		if err := s.ftsDel(tx, r.ID); err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE memories SET superseded_by=? WHERE superseded_by=?",
			r.SupersededBy, r.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// chainHeadSQL is true for a live memory that something else is superseded by: the current
// version of a corrected fact.
func chainHeadSQL(alias string) string {
	return "(" + alias + ".superseded_by IS NULL AND EXISTS (SELECT 1 FROM memories d " +
		"WHERE d.superseded_by = " + alias + ".id))"
}
