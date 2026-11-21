package install

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chka/os-brain/internal/dump"
	"github.com/chka/os-brain/internal/fsguard"
	"github.com/chka/os-brain/internal/pyfmt"
	"github.com/chka/os-brain/internal/store"
)

// UninstallArgs are the flags of `os-brain uninstall`, which removes what init added and
// nothing else.
//
// It is a command because removing a brain by hand goes wrong: a session improvising one
// deleted the whole .claude/settings.json rather than the hooks the brain put there, and
// edited the shared store with raw SQL. Removing a brain is a destructive operation across
// several files and a database other projects may share, which is exactly the kind of thing
// that should be a command with a dry run.
//
//   - It is a DRY RUN unless --apply, like prune. The report names every file it would
//     touch and every line it would remove.
//   - It NEVER deletes the store unless --purge is also given, and even then it dumps
//     first. Everything else here is recreatable from the binary in one command; the
//     database is the only copy of what the project learned.
type UninstallArgs struct {
	Dir    string
	Apply  bool
	Purge  bool
	Backup string
	JSON   bool
	// The dump's refusals, the same three every other dump takes.
	Force        bool
	AllowOutside bool
	AllowTracked bool
}

// removal is one line of the uninstall plan.
type removal struct {
	What string `json:"what"`
	Path string `json:"path,omitempty"`
	Note string `json:"note,omitempty"`
}

// initWrittenFiles are the files init writes into the brain directory that uninstall deletes
// and init can recreate. brain.json is deleted too, but is listed on its own because it is
// the project's identity rather than a generated file.
var initWrittenFiles = []string{"HOWTO.md", "BEST-PRACTICES.md", autoexecScript}

func Uninstall(w io.Writer, s *store.Store, root string, a UninstallArgs) error {
	if a.Dir == "" {
		a.Dir = "brain"
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	here := filepath.Join(root, a.Dir)
	if err := checkUninstallTarget(s, root, here, a.Purge); err != nil {
		return err
	}
	var memCount int
	_ = s.DB.QueryRow("SELECT COUNT(*) FROM memories WHERE project=?", s.Env.Project).Scan(&memCount)

	plan, err := uninstallPlan(s, root, here, a, memCount)
	if err != nil {
		return err
	}
	if !a.Apply {
		return dryRunReport(w, s, root, a, plan)
	}

	dumpPath, err := backupBeforeUninstall(s, root, here, a)
	if err != nil {
		return err
	}
	if inIndex(s.Env) {
		if g := s.GlobalConnect(false); g != nil {
			_, _ = g.Exec("DELETE FROM projects WHERE name=?", s.Env.Project)
		}
	}
	// The shared purge runs against the live connection, before it is closed and before
	// anything on disk moves: on a shared store there is no file to delete, and the rows
	// are the whole of what --purge means.
	var purgedRows map[string]int
	if a.Purge && s.Env.Shared {
		tables, err := dump.New(s).ProjectTables()
		if err != nil {
			return err
		}
		if purgedRows, err = dump.New(s).PurgeProject(tables, nil); err != nil {
			return err
		}
	}
	s.Close()

	done, err := removeInstalledFiles(root, here, a.Dir)
	if err != nil {
		return err
	}
	done = append(done, removeStore(s.Env, here, a.Purge, memCount, purgedRows)...)

	lines := []string{fmt.Sprintf("uninstalled %s", pyfmt.Repr(s.Env.Project))}
	for _, d := range done {
		lines = append(lines, "  - "+d)
	}
	lines = append(lines, "", "backup: "+dumpPath)
	if !a.Purge {
		lines = append(lines, "",
			"The memories are still there. To bring the brain back: os-brain init")
	}
	return store.Out(w, a.JSON, strings.Join(lines, "\n"),
		map[string]interface{}{"uninstalled": true, "project": s.Env.Project,
			"backup": dumpPath, "done": done, "purged": a.Purge})
}

// inIndex reports whether the project is in the shared index, which is the default.
func inIndex(env store.Env) bool {
	return env.Config.Global == nil || *env.Config.Global
}

// checkUninstallTarget refuses before anything is planned when the directory is not a
// brain, is not the brain the store was opened for, or would make a delete land somewhere
// else than intended.
func checkUninstallTarget(s *store.Store, root, here string, purge bool) error {
	if _, err := os.Stat(filepath.Join(here, "brain.json")); err != nil {
		return fmt.Errorf("no brain at %s: nothing to uninstall.\n"+
			"Point --dir at the brain directory if it is somewhere else.", here)
	}
	// The files come from root and the store from s: they must be one brain, or the backup
	// is of one project and the delete of another.
	if fsguard.Realpath(here) != fsguard.Realpath(s.Env.Here) {
		return fmt.Errorf("%s is not the brain this command opened (%s). Nothing was changed.",
			here, s.Env.Here)
	}
	// A private store is deleted by path, so the path deleted must be the store backed up.
	if purge && !s.Env.Shared &&
		fsguard.Realpath(s.Env.DBPath) != fsguard.Realpath(filepath.Join(here, "brain.db")) {
		return fmt.Errorf("--purge deletes %s, but this brain's store is %s (OS_BRAIN_DB?). "+
			"Nothing was changed: unset OS_BRAIN_DB, or remove that file yourself.",
			filepath.Join(here, "brain.db"), s.Env.DBPath)
	}
	// A brain directory that is a link would have every delete below land in its target.
	return fsguard.CheckNoLinks(root, filepath.Join(here, "brain.json"))
}

// uninstallPlan lists everything uninstall will do, database first, and refuses up front
// (dry run included) when a file it would edit is a link, rather than half way through.
func uninstallPlan(s *store.Store, root, here string, a UninstallArgs, memCount int) ([]removal, error) {
	plan := []removal{storePlanLine(s.Env, a.Purge, memCount)}
	add := func(what, path, note string) { plan = append(plan, removal{what, path, note}) }

	for _, f := range initWrittenFiles {
		p := filepath.Join(here, f)
		if _, err := os.Stat(p); err == nil {
			add("delete", p, "written by init, recreatable")
		}
	}
	for _, f := range slashCommandFiles {
		p := filepath.Join(root, ".claude", "commands", f)
		if _, err := os.Stat(p); err == nil {
			add("delete", p, "brain slash command written by init, recreatable")
		}
	}
	// A plugin is a whole file rather than an entry in one, so it goes only when it is ours
	// AND this brain's: another project's brain can be wired into the same agent.
	for _, rel := range pluginPaths {
		if ownPlugin(root, rel, a.Dir) {
			add("delete", filepath.Join(root, rel), "agent plugin written by init")
		}
	}
	add("delete", filepath.Join(here, "brain.json"), "project identity")

	plan = append(plan, plannedHookRemovals(root, a.Dir)...)
	if line := plannedClaudeMD(root, a.Dir); line.What != "" {
		plan = append(plan, line)
	}
	if line := plannedGitignore(root, a.Dir); line.What != "" {
		plan = append(plan, line)
	}
	for _, r := range plan {
		if r.What == "remove hook" || r.What == "remove section" || r.What == "remove entries" {
			if err := fsguard.CheckNoLinks(root, r.Path); err != nil {
				return nil, err
			}
		}
	}
	if inIndex(s.Env) {
		add("drop this project's row", "the shared index",
			"other projects' rows are untouched")
	}
	return plan, nil
}

// storePlanLine is the plan's first line, because it is the only irreversible one and the
// reader should meet it before the housekeeping.
//
// Which database is not a detail. A private brain owns its file, so --purge deletes it. A
// shared one is a column in a file other projects are using: deleting that file would remove
// their memories, and deleting nothing would leave THIS project's rows in it forever,
// orphaned once the index row goes. So a shared purge deletes this project's rows and keeps
// the file.
func storePlanLine(env store.Env, purge bool, memCount int) removal {
	switch {
	case purge && env.Shared:
		return removal{"delete this project's rows", env.DBPath,
			fmt.Sprintf("%d memories — the shared database is kept, other projects untouched",
				memCount)}
	case purge:
		return removal{"delete the database", env.DBPath,
			fmt.Sprintf("%d memories — this is the only copy", memCount)}
	case env.Shared:
		return removal{"KEEP this project's rows", env.DBPath,
			fmt.Sprintf("%d memories left in the shared database; --purge deletes them", memCount)}
	default:
		return removal{"KEEP the database", env.DBPath,
			fmt.Sprintf("%d memories left in place; --purge deletes it", memCount)}
	}
}

func dryRunReport(w io.Writer, s *store.Store, root string, a UninstallArgs, plan []removal) error {
	lines := []string{fmt.Sprintf("DRY RUN — uninstall %s from %s", pyfmt.Repr(s.Env.Project), root), ""}
	for _, r := range plan {
		l := "  " + r.What
		if r.Path != "" {
			l += "  " + r.Path
		}
		if r.Note != "" {
			l += "\n      " + r.Note
		}
		lines = append(lines, l)
	}
	lines = append(lines, "",
		"Nothing has been changed. Add --apply to do it.")
	if !a.Purge && s.Env.Shared {
		lines = append(lines, "This project's memories are kept in the shared database. "+
			"Add --purge as well to delete them.")
	} else if !a.Purge {
		lines = append(lines, "The database is kept. Add --purge as well to delete it.")
	}
	lines = append(lines, "",
		"This does NOT restore a memory store the brain replaced at install time. If a",
		"MEMORY.md or an auto-memory directory was migrated in and deleted, put it back",
		"yourself before removing the brain, or its contents are gone with the database.")
	return store.Out(w, a.JSON, strings.Join(lines, "\n"),
		map[string]interface{}{"dry_run": true, "project": s.Env.Project, "plan": plan})
}

// backupBeforeUninstall dumps the project before anything changes, always, even when the
// database is being kept: the caller is about to remove the config and hooks that make it
// reachable. It returns where the dump ended up.
func backupBeforeUninstall(s *store.Store, root, here string, a UninstallArgs) (string, error) {
	dumpPath := a.Backup
	if dumpPath == "" {
		dumpPath = filepath.Join(here, "uninstall-backup-"+time.Now().Format("20060102-150405")+".json")
	}
	// A shared store gets this project's rows only: dumping the whole file would copy every
	// other project's memories into a directory that is about to be deleted.
	var err error
	if s.Env.Shared {
		_, err = dump.New(s).DumpProject(fsguard.Dump{Path: dumpPath, Root: root, Marker: dump.BackupMarker, Force: a.Force,
			AllowOutside: a.AllowOutside, AllowTracked: a.AllowTracked})
	} else {
		err = dump.New(s).Backup(io.Discard, dump.BackupArgs{Out: dumpPath, Force: a.Force,
			AllowOutside: a.AllowOutside, AllowTracked: a.AllowTracked})
	}
	if err != nil {
		return "", fmt.Errorf("refusing to uninstall: the backup failed (%w).\n"+
			"Nothing has been changed", err)
	}
	if a.Purge {
		// Out of the brain directory, so deleting it does not take the dump with it. Never
		// onto a file already there: a hard link fails where a rename would replace it, and
		// a dump left where it was keeps the directory from being removed.
		kept := filepath.Join(root, filepath.Base(dumpPath))
		if fsguard.CheckNoLinks(root, kept) == nil && os.Link(dumpPath, kept) == nil {
			os.Remove(dumpPath)
			dumpPath = kept
		}
	}
	return dumpPath, nil
}

// removeInstalledFiles takes init's wiring out of every agent config and deletes the files
// init wrote, and reports what it did. Only a failure to rewrite a hook file is an error.
func removeInstalledFiles(root, here, dir string) ([]string, error) {
	var done []string
	if err := applyHookRemovals(root, dir); err != nil {
		return nil, err
	}
	done = append(done, "session hooks removed from every agent config that had them")
	for _, rel := range pluginPaths {
		if ownPlugin(root, rel, dir) {
			if err := os.Remove(filepath.Join(root, rel)); err == nil {
				done = append(done, rel+" removed")
			}
		}
	}
	if err := stripClaudeMD(root, dir); err == nil {
		done = append(done, "CLAUDE.md section removed")
	}
	if err := stripGitignore(root, dir); err == nil {
		done = append(done, ".gitignore entries removed")
	}
	for _, f := range append(initWrittenFiles, "brain.json") {
		_ = os.Remove(filepath.Join(here, f))
	}
	return done, nil
}

// removeStore deletes the database file on a private --purge, reports the rows a shared
// --purge already deleted, and removes the brain directory when nothing is left in it.
func removeStore(env store.Env, here string, purge bool, memCount int, purgedRows map[string]int) []string {
	var done []string
	switch {
	case purge && env.Shared:
		done = append(done, fmt.Sprintf("%d memories deleted from the shared database "+
			"(%d rows in all); no other project touched", purgedRows["memories"],
			dump.SumCounts(purgedRows)))
		done = append(done, removeEmptyDir(here)...)
	case purge:
		for _, f := range []string{"brain.db", "brain.db-wal", "brain.db-shm"} {
			_ = os.Remove(filepath.Join(here, f))
		}
		done = append(done, fmt.Sprintf("database deleted (%d memories)", memCount))
		done = append(done, removeEmptyDir(here)...)
	case env.Shared:
		done = append(done, fmt.Sprintf("%d memories KEPT in the shared database at %s",
			memCount, env.DBPath))
	default:
		done = append(done, "database KEPT at "+env.DBPath)
	}
	return done
}

func removeEmptyDir(here string) []string {
	if entries, err := os.ReadDir(here); err == nil && len(entries) == 0 {
		_ = os.Remove(here)
		return []string{"empty brain directory removed"}
	}
	return nil
}
