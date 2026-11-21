package memory

import (
	"database/sql"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/chka/os-brain/internal/dump"
	"github.com/chka/os-brain/internal/fsguard"
	"github.com/chka/os-brain/internal/pyfmt"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/timefmt"
)

// `forget --all`: a project-scoped wipe. Everything this project stored, and nothing
// another project did.
//
// It exists because there was no route to it. `prune` deletes by decay tier and refuses A
// and B, and `uninstall --purge` deletes a FILE, which does the right thing for a project
// with its own brain and nothing at all for a shared one, where this project's rows sit in
// a database other projects are using. So "clean the memory out of this project" was a
// loop of prune invocations that still left the pinned rows behind.
//
// Every guard here is the usual rule for deletes applied to the widest one there is: a dry
// run unless --apply, --i-mean-it on top of that, a dump of this project's rows written
// first, and tombstones left behind so a wiped fact is not silently relearned as new.

// wipePlan is what a wipe would delete and what it would leave alone.
type wipePlan struct {
	tables  []string       // every project-scoped table
	counts  map[string]int // this project's rows in each
	targets []string       // the tables this run deletes from
	total   int            // rows in targets
	kept    []string       // "table N" for tables with rows that this run leaves alone
}

// planWipe counts this project's rows. Memories always go; the activity log, measures,
// leads and history go too only with --everything.
func (s *Store) planWipe(everything bool) (*wipePlan, error) {
	tables, err := dump.New(s.Store).ProjectTables()
	if err != nil {
		return nil, err
	}
	p := &wipePlan{tables: tables, counts: map[string]int{}, targets: []string{"memories"}}
	for _, t := range tables {
		var n int
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM "+t+" WHERE project=?",
			s.Env.Project).Scan(&n); err != nil {
			return nil, err
		}
		p.counts[t] = n
	}
	if everything {
		for _, t := range tables {
			if t != "memories" {
				p.targets = append(p.targets, t)
			}
		}
	}
	for _, t := range p.targets {
		p.total += p.counts[t]
	}
	for _, t := range tables {
		if !store.Contains(p.targets, t) && p.counts[t] > 0 {
			p.kept = append(p.kept, fmt.Sprintf("%s %d", t, p.counts[t]))
		}
	}
	return p, nil
}

// ForgetAll deletes every memory this project owns, every tier and pinned ones included,
// and with --everything the activity log, measures, leads and history too.
func (s *Store) ForgetAll(w io.Writer, a ForgetArgs) error {
	p, err := s.planWipe(a.Everything)
	if err != nil {
		return err
	}
	if !a.Apply {
		return s.printWipeDryRun(w, p, a.JSON)
	}
	if !a.IMeanIt {
		return fmt.Errorf("--all deletes every memory in %s, pinned and load-bearing ones "+
			"included (%d memories, %d rows). Add --i-mean-it if that is really the intent, "+
			"or run it without --apply to see the plan", pyfmt.Repr(s.Env.Project),
			p.counts["memories"], p.total)
	}

	backup := a.Backup
	if backup == "" {
		// Named to fall under the `brain/brain.db-*` line init writes into .gitignore. A
		// dump is every memory in cleartext, and the default path is the one nobody
		// thinks about, so the default is the one that must already be ignored.
		backup = filepath.Join(s.Env.Here,
			"brain.db-wipe-"+time.Now().Format("20060102-150405")+".json")
	}
	dumpOut := fsguard.Dump{Path: backup, Root: s.Env.ProjectRoot(), Marker: dump.BackupMarker, Force: a.Force,
		AllowOutside: a.AllowOutside, AllowTracked: a.AllowTracked}
	if err := dumpOut.Check(); err != nil {
		return err
	}
	if _, err := dump.New(s.Store).DumpProject(dumpOut); err != nil {
		return fmt.Errorf("refusing to wipe: the dump failed (%w).\nNothing has been changed", err)
	}

	reason := a.Reason
	if reason == "" {
		reason = "wiped"
	}
	deleted, err := dump.New(s.Store).PurgeProject(p.targets, func(tx *sql.Tx, m *store.Memory) error {
		if a.Everything {
			// Archiving into mem_history and then deleting mem_history is theatre.
			return nil
		}
		if err := s.Archive(tx, m, "wipe", reason); err != nil {
			return err
		}
		_, err := tx.Exec("INSERT OR REPLACE INTO tombstones(hash,project,scope,title,"+
			"reason,forgotten_at) VALUES(?,?,?,?,?,?)",
			store.TombstoneHash(s.Env.Project, m), s.Env.Project, m.Scope, m.Title,
			reason, timefmt.Now())
		return err
	})
	if err != nil {
		return err
	}

	out := []string{fmt.Sprintf("wiped %s", pyfmt.Repr(s.Env.Project))}
	for _, t := range p.targets {
		out = append(out, fmt.Sprintf("  %-16s %d deleted", t, deleted[t]))
	}
	out = append(out, "", "dump: "+backup)
	if !a.Everything && len(p.kept) > 0 {
		out = append(out, "kept: "+strings.Join(p.kept, " · "))
	}
	out = append(out, "",
		"The brain is still installed and the hooks still run; it is empty.",
		"os-brain import "+backup+" puts it back.")
	return store.Out(w, a.JSON, strings.Join(out, "\n"), map[string]interface{}{
		"wiped": true, "project": s.Env.Project, "deleted": deleted, "backup": backup})
}

func (s *Store) printWipeDryRun(w io.Writer, p *wipePlan, asJSON bool) error {
	out := []string{
		fmt.Sprintf("DRY RUN — wipe %s (%s)", pyfmt.Repr(s.Env.Project), s.Env.DBPath), "",
		"would delete:",
	}
	for _, t := range p.targets {
		out = append(out, fmt.Sprintf("  %-16s %d", t, p.counts[t]))
	}
	if len(p.kept) > 0 {
		out = append(out, "", "left alone: "+strings.Join(p.kept, " · "),
			"  --everything clears those too")
	}
	out = append(out, "",
		fmt.Sprintf("%d rows. Nothing has been changed. Add --apply --i-mean-it to do it.",
			p.total),
		"A dump of this project's rows is written first either way; no other project is touched.")
	return store.Out(w, asJSON, strings.Join(out, "\n"), map[string]interface{}{
		"dry_run": true, "project": s.Env.Project, "would_delete": p.total,
		"counts": p.counts, "targets": p.targets})
}
