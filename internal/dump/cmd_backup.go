package dump

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/chka/os-brain/internal/fsguard"
	"github.com/chka/os-brain/internal/safety"
	"github.com/chka/os-brain/internal/store"
)

type BackupArgs struct {
	Force        bool
	Out          string
	AllowOutside bool
	AllowTracked bool
	JSON         bool
	// The safety-copy modes (safety.go). They live on `backup` rather than on a command of
	// their own because "make a copy of the brain" is what this command is called.
	Auto    bool
	List    bool
	Verify  bool
	Restore string
	Apply   bool
	// WholeStore dumps every project in a shared store, not just this one. Confirm is the
	// person's typed yes at a terminal, set only by the CLI; without it WholeStore refuses.
	WholeStore bool
	Confirm    func(summary string) (bool, error)
}

// Backup writes a JSON dump of this project's part of the store, or runs one of the
// safety-copy modes.
//
// Tables are enumerated from the schema rather than a hardcoded list. A hand-written list
// is a list somebody forgets to extend: a new table once shipped and was silently absent
// from every backup taken that day, in the one command people are told to run before
// anything destructive.
func (s *Store) Backup(w io.Writer, a BackupArgs) error {
	switch {
	case a.Auto:
		return safety.New(s.Store).SafetyAuto(w)
	case a.List:
		return safety.New(s.Store).SafetyList(w, a.JSON)
	case a.Verify:
		return safety.New(s.Store).SafetyVerify(w, a.Out, a.JSON)
	case a.Restore != "":
		return safety.New(s.Store).SafetyRestore(w, a.Restore, a.Apply, a.JSON)
	}
	out := a.Out
	if out == "" {
		out = filepath.Join(s.Env.Here, "brain-backup-"+time.Now().Format("20060102-150405")+".json")
	}
	// Refuse to overwrite a file this tool did not generate, as export and snapshot do.
	// Backup once did not, so `backup --out notes.json` destroyed notes.json without a
	// word. --force overrides it, and has to exist for the refusal to be honest.
	dumpOut := fsguard.Dump{Path: out, Root: s.Env.ProjectRoot(), Marker: BackupMarker, Force: a.Force,
		AllowOutside: a.AllowOutside, AllowTracked: a.AllowTracked}
	if err := dumpOut.Check(); err != nil {
		return err
	}
	shared := s.Env.DBPath == s.Env.GlobalDB
	if a.WholeStore {
		if err := confirmWholeStore(a, shared, s.Env.DBPath, out); err != nil {
			return err
		}
	}
	dump, order, counts, err := s.backupTables(shared, a.WholeStore)
	if err != nil {
		return err
	}
	if err := writeDumpFile(&dumpOut, dump, order); err != nil {
		return err
	}
	var parts []string
	for _, k := range order {
		if counts[k] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", k, counts[k]))
		}
	}
	obj := map[string]interface{}{"out": out}
	for k, v := range counts {
		obj[k] = v
	}
	return store.Out(w, a.JSON, fmt.Sprintf("backed up to %s (mode 600)\n  %s", out,
		strings.Join(parts, " · ")), obj)
}

// confirmWholeStore guards --whole-store: it copies every project in a shared store,
// private ones included, into this directory in cleartext, so only a person at a terminal
// can ask for it, and they are asked again.
func confirmWholeStore(a BackupArgs, shared bool, dbPath, out string) error {
	if !shared {
		return fmt.Errorf("--whole-store is for a shared store; this project's database is " +
			"its own, and a plain backup already holds all of it")
	}
	if a.Confirm == nil {
		return fmt.Errorf("--whole-store writes every project's memories, notes and messages " +
			"into this directory in cleartext: a person runs it, at a terminal")
	}
	ok, err := a.Confirm(fmt.Sprintf("This writes EVERY project in %s, private ones "+
		"included, to %s in cleartext.", dbPath, out))
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("not backed up: nothing written")
	}
	return nil
}

// backupTables reads what belongs in the backup, table by table, and returns the dump, the
// order its tables were read in and each table's row count. The order is the schema's,
// with mem_history written under the key `history` in mem_history's place.
func (s *Store) backupTables(shared, whole bool) (map[string]interface{}, []string, map[string]int, error) {
	tables, err := s.TableNames()
	if err != nil {
		return nil, nil, nil, err
	}
	dump := s.newDump()
	counts := map[string]int{}
	var order, uncovered []string
	for _, t := range tables {
		where, args, keep, err := s.backupFilter(t, shared, whole)
		if err == nil && !keep {
			continue
		}
		var rows []map[string]interface{}
		if err == nil {
			rows, err = s.tableRows(t, where, args)
		}
		if err != nil {
			uncovered = append(uncovered, fmt.Sprintf("%s (%v)", t, err))
			continue
		}
		key := t
		if t == "mem_history" {
			key = "history"
		}
		dump[key] = rows
		counts[key] = len(rows)
		order = append(order, key)
	}
	if len(uncovered) > 0 {
		// Fail closed: a partial backup that reports success is worse than no backup.
		return nil, nil, nil, fmt.Errorf("refusing to write a partial backup; these tables "+
			"could not be read: %s", strings.Join(uncovered, ", "))
	}
	return dump, order, counts, nil
}

// backupFilter is what of one table belongs in this project's backup. In a project's own
// database that is everything carrying its label and every table without one, which a
// restore needs. In a SHARED store the tables without a project column are the spine, and
// taking them whole once copied other projects' notes and direct messages, the index's
// private flags and every lesson's search index into this project's directory. There it is
// this project's own rows of each, the spine's two shared-by-design tables (axioms and
// standards), and nothing from a table it cannot attribute.
func (s *Store) backupFilter(table string, shared, whole bool) (string, []interface{}, bool, error) {
	if whole {
		return "", nil, true, nil
	}
	cols, err := s.ColumnsOf(table)
	if err != nil {
		return "", nil, false, err
	}
	if cols["project"] {
		return "project=?", []interface{}{s.Env.Project}, true, nil
	}
	if !shared {
		return "", nil, true, nil
	}
	switch {
	case cols["to_project"] && cols["from_project"]:
		return "to_project=? OR from_project=?", []interface{}{s.Env.Project, s.Env.Project}, true, nil
	case table == "projects":
		return "root_id=?", []interface{}{store.HashRoot(s.Env.ProjectRoot())}, true, nil
	case table == "project_ids":
		return "name=?", []interface{}{s.Env.Project}, true, nil
	case table == "axioms" || table == "standards":
		return "", nil, true, nil
	}
	return "", nil, false, nil
}
