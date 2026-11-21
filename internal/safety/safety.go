package safety

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chka/os-brain/internal/fsguard"
	"github.com/chka/os-brain/internal/pyfmt"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/timefmt"
)

// Safety copies: automatic, verified copies of the whole store, made at session open.
//
// Before these existed every copy was manual: `backup` and `export` when somebody typed
// them, plus the dumps prune, share and uninstall write to protect themselves. A brain
// nobody remembered to back up had nothing behind it but a weeks-old file.
//
// A copy here is a DATABASE, not a JSON dump: `VACUUM INTO` is one statement, consistent
// under WAL without stopping writers, and restoring it is copying a file back rather than
// importing rows into whatever is there now. The JSON dump keeps its own job.

const (
	// SafetyInterval is how stale the newest copy may be before `--auto` makes another.
	// A day: short enough that a laptop lost tonight costs one session, long enough that
	// a developer opening six sessions in a morning gets one copy.
	SafetyInterval = 24 * time.Hour
	// SafetyKeep is how many copies survive rotation. Two rules in rotate bound it.
	SafetyKeep = 14
	// SafetyShrinkAlarm is how far the live store may fall below the best copy before the
	// brief says so. Not a threshold for ACTION (nothing is ever restored automatically),
	// only for saying out loud that the store got smaller.
	SafetyShrinkAlarm = 0.80
)

// SafetyCopy is one file on disk plus what verifying it found.
type SafetyCopy struct {
	Path     string `json:"path"`
	Taken    int64  `json:"taken"`
	Bytes    int64  `json:"bytes"`
	Memories int    `json:"memories"`
	Schema   int    `json:"schema"`
	OK       bool   `json:"ok"`
	Problem  string `json:"problem,omitempty"`
}

// safetyDir is per STORE, not per project: a shared file holds many projects and one copy
// of it protects all of them, while a project with its own file is a different store and
// must not land in the same directory. It sits outside every project because a copy in a
// working tree is one `git add -A` away from being published, and that has happened.
func (s *Store) safetyDir() string {
	sum := sha256.Sum256([]byte(s.Env.DBPath))
	return filepath.Join(filepath.Dir(s.Env.GlobalDB), "backups", hex.EncodeToString(sum[:8]))
}

// inspectCopy describes the copy at path, dated by its modification time, and opens it to
// say whether it is any good.
func inspectCopy(path string, info os.FileInfo) SafetyCopy {
	c := SafetyCopy{Path: path, Bytes: info.Size(), Taken: info.ModTime().Unix()}
	c.Memories, c.Schema, c.Problem = peekCopy(path)
	c.OK = c.Problem == ""
	return c
}

// listCopies reads the copy directory, newest first, opening each copy to verify it.
func (s *Store) listCopies() ([]SafetyCopy, error) {
	dir := s.safetyDir()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []SafetyCopy
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, inspectCopy(filepath.Join(dir, e.Name()), info))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Taken > out[j].Taken })
	return out, nil
}

// peekCopy opens a copy read-only and asks the three questions that make it a copy rather
// than a file: does it pass integrity_check, what schema is it, and how much is in it.
func peekCopy(path string) (memories, schema int, problem string) {
	db, err := sql.Open("sqlite", store.SQLiteURI(path, "mode=ro"))
	if err != nil {
		return 0, 0, err.Error()
	}
	defer db.Close()
	var check string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&check); err != nil {
		return 0, 0, err.Error()
	}
	if check != "ok" {
		return 0, 0, "integrity_check: " + check
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM memories").Scan(&memories); err != nil {
		return 0, 0, err.Error()
	}
	var v string
	_ = db.QueryRow("SELECT v FROM meta WHERE k='schema_version'").Scan(&v)
	schema, _ = strconv.Atoi(v)
	return memories, schema, ""
}

// safetyDue answers whether --auto should do anything: the newest copy is old enough, or
// the store has been written since it was taken. A brain nobody wrote to does not need a
// second identical copy.
func (s *Store) safetyDue(copies []SafetyCopy) bool {
	if len(copies) == 0 {
		return true
	}
	newest := copies[0]
	if timefmt.Now()-newest.Taken >= int64(SafetyInterval/time.Second) {
		return true
	}
	info, err := os.Stat(s.Env.DBPath)
	if err != nil {
		return false
	}
	return info.ModTime().Unix() > newest.Taken
}

// freeCopyPath picks a file name for a new copy. The name has second resolution and
// VACUUM INTO refuses to overwrite, so two copies in the same second would collide. That
// is not theoretical: `--restore` copies the current store immediately, in the same second
// as any copy made just before it, and the restore then refused itself. A numeric suffix
// separates them.
func freeCopyPath(dir string) string {
	stamp := time.Now().Format("20060102-150405")
	path := filepath.Join(dir, "brain-"+stamp+".db")
	for n := 2; n < 100; n++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		path = filepath.Join(dir, fmt.Sprintf("brain-%s-%d.db", stamp, n))
	}
	return path
}

// MakeSafetyCopy writes one copy and verifies it before returning. Verification is part of
// making it, not a separate ritual somebody schedules and forgets: a copy nobody has
// opened is a belief, not a backup.
func (s *Store) MakeSafetyCopy() (SafetyCopy, error) {
	dir := s.safetyDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return SafetyCopy{}, err
	}
	path := freeCopyPath(dir)
	// Created empty and owner-only first: VACUUM INTO writes into an empty file, but
	// creates a missing one with the process umask, which can leave it world-readable.
	if err := fsguard.PrecreatePrivate(path); err != nil {
		return SafetyCopy{}, fmt.Errorf("could not write a safety copy to %s: %w", path, err)
	}
	if _, err := s.DB.Exec("VACUUM INTO ?", path); err != nil {
		os.Remove(path)
		return SafetyCopy{}, fmt.Errorf("could not write a safety copy to %s: %w", path, err)
	}

	c := SafetyCopy{Path: path, Taken: timefmt.Now()}
	if info, err := os.Stat(path); err == nil {
		c.Bytes = info.Size()
	}
	c.Memories, c.Schema, c.Problem = peekCopy(path)
	c.OK = c.Problem == ""

	// A copy that opens but holds a different number of memories than the store it was
	// taken from did not copy the store.
	live := s.liveMemoryCount()
	if c.OK && c.Memories != live {
		c.OK = false
		c.Problem = fmt.Sprintf("copy holds %d memories, the store has %d", c.Memories, live)
	}
	return c, nil
}

// liveMemoryCount is every memory row in the store, all projects, superseded included:
// the same thing peekCopy counts in a copy, so the two can be compared.
func (s *Store) liveMemoryCount() int {
	var n int
	_ = s.DB.QueryRow("SELECT COUNT(*) FROM memories").Scan(&n)
	return n
}

// rotate deletes the oldest copies beyond SafetyKeep, and is the part of this feature most
// able to cause the loss it exists to prevent. Plain time-based rotation has a famous
// failure: a store is emptied by accident, fourteen ordinary sessions follow, and rotation
// faithfully replaces every good copy with a copy of the damage.
//
// So two rules bound it. The copy holding the MOST memories is kept beyond the window: if
// the store shrank, the evidence of what it held outlives the schedule. A copy that failed
// verification is also kept: it is evidence about the source, and tidying it away throws
// out the only sign of trouble.
func (s *Store) rotate(copies []SafetyCopy) []string {
	if len(copies) <= SafetyKeep {
		return nil
	}
	best := 0
	for i, c := range copies {
		if c.OK && c.Memories > copies[best].Memories {
			best = i
		}
	}
	var removed []string
	for i := SafetyKeep; i < len(copies); i++ {
		if i == best || !copies[i].OK {
			continue
		}
		if err := os.Remove(copies[i].Path); err == nil {
			removed = append(removed, copies[i].Path)
		}
	}
	return removed
}

// pushCopy hands a verified copy to a command the PROJECT declared, because a copy on this
// laptop does not survive this laptop, and the binary does no networking by design.
//
// It is gated exactly like a wakeup probe, and for the same reason: brain.json is
// committed, so the command arrives with a clone, and an ungated one would run at session
// open on the machine of whoever cloned it. An unapproved push is printed in full and
// skipped. A failing push is reported: one that silently stopped working is worse than
// none, because it is believed.
func (s *Store) pushCopy(w io.Writer, c SafetyCopy) {
	raw := strings.TrimSpace(s.Env.Config.Backups.Push)
	if raw == "" || !c.OK {
		return
	}
	cmd := strings.ReplaceAll(raw, "{file}", c.Path)
	sum := sha256.Sum256([]byte("backup-push\x00" + raw))
	if !s.Trusted()[hex.EncodeToString(sum[:])] {
		fmt.Fprintf(w, "backup push NOT approved on this machine, so it did not run:\n"+
			"  %s\napprove it with: os-brain wakeup --trust\n", raw)
		return
	}
	out, err := exec.Command("sh", "-c", cmd).CombinedOutput()
	if err != nil {
		fmt.Fprintf(w, "backup push FAILED (%v): %s\n", err, strings.TrimSpace(string(out)))
		return
	}
	fmt.Fprintf(w, "pushed off this machine: %s\n", raw)
}

// SafetyShrink reports that the live store holds materially less than its best copy. It is
// deliberately a REPORT and never an action: a project may legitimately shrink, since
// `forget --all` is a command people run on purpose. Returning the copy lets the caller
// name the file somebody would restore from.
func (s *Store) SafetyShrink() (live int, best *SafetyCopy) {
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM memories").Scan(&live); err != nil {
		return 0, nil
	}
	copies, err := s.listCopies()
	if err != nil {
		return live, nil
	}
	for i := range copies {
		if !copies[i].OK {
			continue
		}
		if best == nil || copies[i].Memories > best.Memories {
			best = &copies[i]
		}
	}
	if best == nil || best.Memories == 0 {
		return live, nil
	}
	if float64(live) >= float64(best.Memories)*SafetyShrinkAlarm {
		return live, nil
	}
	return live, best
}

// The command modes below hang off `backup` rather than a new top-level command, because
// "make a copy of the brain" is what `backup` is called and every extra command is more
// surface to learn.

// SafetyAuto is what the session-open hook runs. Silent when it does nothing, one line
// when it acts: a message on every session start gets muted, and takes the one that
// mattered with it.
func (s *Store) SafetyAuto(w io.Writer) error {
	copies, err := s.listCopies()
	if err != nil {
		return err
	}
	if !s.safetyDue(copies) {
		return nil
	}
	c, err := s.MakeSafetyCopy()
	if err != nil {
		// Never fatal. A brain that refuses to open a session because it could not write
		// a copy has turned a safety net into an outage.
		fmt.Fprintf(w, "safety copy FAILED: %v\n", err)
		return nil
	}
	if !c.OK {
		fmt.Fprintf(w, "safety copy written but DID NOT VERIFY: %s\n  %s\n", c.Path, c.Problem)
		return nil
	}
	fmt.Fprintf(w, "safety copy: %s (%d memories, %s)\n", filepath.Base(c.Path), c.Memories,
		humanBytes(c.Bytes))
	again, _ := s.listCopies()
	if gone := s.rotate(again); len(gone) > 0 {
		fmt.Fprintf(w, "  rotated out %d older %s\n", len(gone), copiesWord(len(gone)))
	}
	s.pushCopy(w, c)
	return nil
}

// SafetyList prints what exists. The verdict column is the point: a directory of files is
// not a backup story until somebody has opened them.
func (s *Store) SafetyList(w io.Writer, asJSON bool) error {
	copies, err := s.listCopies()
	if err != nil {
		return err
	}
	if asJSON {
		return store.Out(w, true, "", map[string]interface{}{
			"dir": s.safetyDir(), "count": len(copies), "copies": copies,
		})
	}
	if len(copies) == 0 {
		fmt.Fprintf(w, "no safety copies yet for this store.\n  they land in %s\n"+
			"  one is made at session open, at most every %d hours\n",
			s.safetyDir(), int(SafetyInterval.Hours()))
		return nil
	}
	fmt.Fprintf(w, "%d safety %s in %s\n", len(copies),
		copiesWord(len(copies)), s.safetyDir())
	for _, c := range copies {
		verdict := "ok"
		if !c.OK {
			verdict = "FAILED: " + c.Problem
		}
		fmt.Fprintf(w, "  %s  %-7s %5d memories  schema v%d  %s\n",
			time.Unix(c.Taken, 0).Format("2006-01-02 15:04"), humanBytes(c.Bytes),
			c.Memories, c.Schema, verdict)
	}
	return nil
}

// SafetyVerify opens copies and proves they read. With no argument it checks all of them,
// because "are my backups any good" is a question asked about the set.
func (s *Store) SafetyVerify(w io.Writer, path string, asJSON bool) error {
	copies, err := s.copiesToVerify(path)
	if err != nil {
		return err
	}
	bad := 0
	for _, c := range copies {
		if !c.OK {
			bad++
		}
	}
	if asJSON {
		return store.Out(w, true, "", map[string]interface{}{
			"checked": len(copies), "failed": bad, "copies": copies,
		})
	}
	if len(copies) == 0 {
		fmt.Fprintln(w, "nothing to verify: no safety copies for this store yet")
		return nil
	}
	for _, c := range copies {
		if c.OK {
			fmt.Fprintf(w, "ok      %s  %d memories\n", filepath.Base(c.Path), c.Memories)
		} else {
			fmt.Fprintf(w, "FAILED  %s  %s\n", filepath.Base(c.Path), c.Problem)
		}
	}
	fmt.Fprintf(w, "%d checked, %d failed\n", len(copies), bad)
	return nil
}

// copiesToVerify is the one named copy, or every copy of this store when none is named.
func (s *Store) copiesToVerify(path string) ([]SafetyCopy, error) {
	if path == "" {
		return s.listCopies()
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("no such copy: %s", path)
	}
	return []SafetyCopy{inspectCopy(path, info)}, nil
}

// SafetyRestore is the drill. It is a dry run unless --apply, like prune, uninstall and
// forget --all, and it prints the comparison BEFORE the day it is needed: the answer to
// "is this copy any good" should not first be sought during the emergency.
func (s *Store) SafetyRestore(w io.Writer, path string, apply, asJSON bool) error {
	if path == "" {
		return fmt.Errorf("restore needs the copy to restore from.\n" +
			"  os-brain backup --list        what there is\n" +
			"  os-brain backup --restore <path>")
	}
	// The file is read once, into a private file beside the store, and every check below
	// runs on THAT copy. Verifying one path and then copying another let a crafted name
	// (`x.db?y`) be checked as one file and installed as a different one.
	staged, dropped, err := s.stageRestore(path)
	if err != nil {
		return err
	}
	installed := false
	defer func() {
		if !installed {
			os.Remove(staged)
		}
	}()
	mem, schema, problem := peekCopy(staged)
	if problem != "" {
		return fmt.Errorf("that copy does not verify, so it is not going anywhere near "+
			"the live store:\n  %s\n  %s", path, problem)
	}
	if schema > store.SchemaVersion {
		return fmt.Errorf("that copy is schema v%d and this client is v%d. Upgrade the "+
			"client before restoring: an older binary writing into a newer store is the "+
			"corruption nobody notices", schema, store.SchemaVersion)
	}
	live := s.liveMemoryCount()
	summary := func(applied bool) map[string]interface{} {
		return map[string]interface{}{
			"from": path, "applied": applied, "dropped": dropped,
			"copy": map[string]interface{}{"memories": mem, "schema": schema},
			"live": map[string]interface{}{"memories": live},
		}
	}

	if asJSON && !apply {
		return store.Out(w, true, "", summary(false))
	}
	if !asJSON {
		printRestorePlan(w, path, mem, schema, live, dropped)
	}
	if !apply {
		fmt.Fprintln(w, "\nThis was a dry run and nothing was written. Add --apply to do it.")
		return nil
	}
	// The current store is copied first, always. Nothing here overwrites anything without
	// leaving a copy of what it replaced, the same rule uninstall and prune keep.
	pre, err := s.MakeSafetyCopy()
	if err != nil {
		return fmt.Errorf("refusing to restore: could not copy the CURRENT store first "+
			"(%w). The store you have is the one you would lose", err)
	}
	if !asJSON {
		fmt.Fprintf(w, "\ncurrent store copied first: %s\n", pre.Path)
	}
	if err := s.swapIn(staged); err != nil {
		return err
	}
	installed = true
	if asJSON {
		out := summary(true)
		out["previous"] = pre.Path
		return store.Out(w, true, "", out)
	}
	fmt.Fprintf(w, "restored %d memories into %s\n", mem, s.Env.DBPath)
	fmt.Fprintln(w, "Run `os-brain doctor` before trusting it.")
	return nil
}

func printRestorePlan(w io.Writer, path string, mem, schema, live int, dropped []string) {
	fmt.Fprintf(w, "restore from %s\n", path)
	fmt.Fprintf(w, "  copy   %5d memories  schema v%d\n", mem, schema)
	fmt.Fprintf(w, "  live   %5d memories\n", live)
	if d := mem - live; d != 0 {
		fmt.Fprintf(w, "  change %+d memories\n", d)
	}
	for _, d := range dropped {
		fmt.Fprintf(w, "  drops  %s: not part of a brain's schema, so it does not come back\n", d)
	}
}

// stageRestore copies the file to a private temporary beside the store and makes it a
// brain and nothing else. Restore once swapped in any SQLite file as the whole store, and
// the store's own DDL is CREATE ... IF NOT EXISTS, so a trigger planted in the file
// survived every later open and could copy each new memory, of every project, somewhere
// its author could read it. sanitiseStaged closes that.
func (s *Store) stageRestore(path string) (string, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	staged, err := fsguard.WriteTemp(filepath.Dir(s.Env.DBPath), filepath.Base(s.Env.DBPath)+".restore",
		data, 0o600)
	if err != nil {
		return "", nil, err
	}
	dropped, err := s.sanitiseStaged(staged)
	if err != nil {
		os.Remove(staged)
		return "", nil, fmt.Errorf("that copy does not verify, so it is not going anywhere "+
			"near the live store:\n  %s\n  %w", path, err)
	}
	return staged, dropped, nil
}

// schemaObject is one row of sqlite_master.
type schemaObject struct{ typ, name string }

// sanitiseStaged compares the staged file's schema with the live store's. A table, or a
// column of a table, the live brain does not have refuses the copy: there is no safe way to
// guess what it is for. A trigger, a view, or an index the brain does not have is code or
// structure somebody added, so it is dropped from the staged file and reported.
func (s *Store) sanitiseStaged(staged string) ([]string, error) {
	liveTables, liveIndexes, err := s.liveSchemaNames()
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", store.SQLiteURI(staged, ""))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	objs, err := stagedSchemaObjects(db)
	if err != nil {
		return nil, err
	}
	var dropped []string
	for _, o := range objs {
		switch o.typ {
		case "table":
			if !liveTables[o.name] {
				return nil, fmt.Errorf("it holds a table %s that is not part of a brain", pyfmt.Repr(o.name))
			}
			if err := s.checkStagedColumns(db, o.name); err != nil {
				return nil, err
			}
		case "index":
			if liveIndexes[o.name] {
				continue
			}
			fallthrough
		case "trigger", "view":
			if _, err := db.Exec("DROP " + strings.ToUpper(o.typ) + " " + store.QuoteIdent(o.name)); err != nil {
				return nil, err
			}
			dropped = append(dropped, o.typ+" "+pyfmt.Repr(o.name))
		default:
			return nil, fmt.Errorf("it holds a %s %s that is not part of a brain", o.typ, pyfmt.Repr(o.name))
		}
	}
	return dropped, nil
}

// liveSchemaNames is the live store's own tables and indexes: what a brain is allowed to
// contain.
func (s *Store) liveSchemaNames() (tables, indexes map[string]bool, err error) {
	tables, indexes = map[string]bool{}, map[string]bool{}
	rows, err := s.DB.Query("SELECT type, name FROM sqlite_master")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var typ, name string
		if err := rows.Scan(&typ, &name); err != nil {
			return nil, nil, err
		}
		switch typ {
		case "table":
			tables[name] = true
		case "index":
			indexes[name] = true
		}
	}
	return tables, indexes, nil
}

// stagedSchemaObjects lists the staged file's schema, leaving out SQLite's own internal
// objects. Read in full before anything is dropped, so the drops do not disturb the scan.
func stagedSchemaObjects(db *sql.DB) ([]schemaObject, error) {
	rows, err := db.Query("SELECT type, name FROM sqlite_master WHERE name NOT LIKE 'sqlite\\_%' ESCAPE '\\'")
	if err != nil {
		return nil, fmt.Errorf("it is not a SQLite database (%v)", err)
	}
	defer rows.Close()
	var objs []schemaObject
	for rows.Next() {
		var o schemaObject
		if err := rows.Scan(&o.typ, &o.name); err != nil {
			return nil, err
		}
		objs = append(objs, o)
	}
	return objs, nil
}

// checkStagedColumns refuses a staged table carrying a column the live table does not have.
func (s *Store) checkStagedColumns(db *sql.DB, table string) error {
	have, err := store.TableColumns(db, table)
	if err != nil {
		return err
	}
	want, err := store.TableColumns(s.DB, table)
	if err != nil {
		return err
	}
	for c := range have {
		if !want[c] {
			return fmt.Errorf("its table %s has a column %s that is not part of "+
				"a brain", pyfmt.Repr(table), pyfmt.Repr(c))
		}
	}
	return nil
}

// swapIn renames the staged file over the store, which is atomic: the path names the old
// file or the new one, never half of each. A rename cannot reach a process that already
// has the old file open, though, and its next write would land in the file being thrown
// away. So it refuses unless this is the only connection: leaving WAL mode is something
// SQLite allows only to the last connection to a database, which makes it the question
// "is anybody else here?" asked of the one component that knows. The exclusive lock taken
// after it holds off anyone arriving while the rename happens.
func (s *Store) swapIn(staged string) error {
	ctx := context.Background()
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var mode string
	if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode=DELETE").Scan(&mode); err != nil ||
		mode != "delete" {
		return fmt.Errorf("refusing to restore: another session has this store open (a hook, " +
			"an MCP server, a UI, another project on a shared store). The restore replaces the " +
			"file under it, and whatever it wrote next would land in the file being thrown " +
			"away. Close them and run it again")
	}
	if _, err := conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		_, _ = conn.ExecContext(ctx, "PRAGMA journal_mode=WAL")
		return fmt.Errorf("refusing to restore: could not lock the store (%v)", err)
	}
	dst := s.Env.DBPath
	if err := os.Rename(staged, dst); err != nil {
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
		_, _ = conn.ExecContext(ctx, "PRAGMA journal_mode=WAL")
		return fmt.Errorf("the restore did not happen, the store is as it was: %w", err)
	}
	_, _ = conn.ExecContext(ctx, "ROLLBACK")
	conn.Close()
	s.DB.Close()
	// The old file's journal and shared-memory files belong to the file just replaced;
	// left in place, SQLite would try to apply them to the restored one.
	for _, side := range []string{dst + "-wal", dst + "-shm", dst + "-journal"} {
		_ = os.Remove(side)
	}
	return nil
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0fkB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}

// copiesWord exists because Plural() appends "s", and the plural of copy is not copys.
func copiesWord(n int) string {
	if n == 1 {
		return "copy"
	}
	return "copies"
}
