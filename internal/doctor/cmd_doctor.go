package doctor

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chka/os-brain/internal/dump"
	"github.com/chka/os-brain/internal/fsguard"
	"github.com/chka/os-brain/internal/measure"
	"github.com/chka/os-brain/internal/memory"
	"github.com/chka/os-brain/internal/pyfmt"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/textguard"
)

type DoctorArgs struct {
	Fix  bool
	JSON bool
}

// problem is something wrong with the store. kind names the repair --fix applies, or is
// empty when only a person can say what the data should have been.
type problem struct {
	msg  string
	kind string // reindex | rehash | unlink | confidence | ""
}

// doctorReport collects what the checks find, in the order they run. Problems are faults;
// notes are things worth knowing that are not wrong as such.
type doctorReport struct {
	problems []problem
	notes    []string
	cycles   [][]int64 // supersession cycles, kept for --fix
	nlive    int       // this project's live memories, also used by the notes
}

func (r *doctorReport) problem(kind, format string, args ...interface{}) {
	r.problems = append(r.problems, problem{fmt.Sprintf(format, args...), kind})
}

func (r *doctorReport) note(format string, args ...interface{}) {
	r.notes = append(r.notes, fmt.Sprintf(format, args...))
}

// Doctor detects a brain that has quietly gone inconsistent, and with --fix repairs what
// it can. The structural checks are deliberately store-wide rather than per-project: index
// drift, dangling links and integrity are properties of the file, and a problem in another
// project's rows is still a problem with the database this project depends on. The
// repairs, though, touch only this project's rows.
func (s *Store) Doctor(w io.Writer, a DoctorArgs) error {
	r := &doctorReport{}
	nmem := s.countOf("SELECT COUNT(*) FROM memories")
	s.checkSearchIndex(r)
	s.checkIdentityHashes(r)
	s.checkSupersession(r)
	s.checkOwnHashes(r)
	s.checkInfiniteReadings(r)
	s.checkConfidence(r)
	s.checkIntegrity(r)
	s.noteUndeliveredNotes(r)
	s.noteUnrecordedSupersessions(r)
	s.noteProvenance(r)
	s.checkMeasureBands(r)
	s.noteFileHealth(r)

	var fixNotes []string
	if a.Fix && len(r.problems) > 0 {
		var err error
		if fixNotes, err = s.repair(r); err != nil {
			return err
		}
	}
	return s.printDoctor(w, a, r, nmem, fixNotes)
}

// countOf runs a COUNT query; a failed query counts as zero, so one broken table cannot
// stop the rest of the checks from reporting.
func (s *Store) countOf(q string, args ...interface{}) int {
	var n int
	_ = s.DB.QueryRow(q, args...).Scan(&n)
	return n
}

// checkSearchIndex compares the memories with the search index in both directions, named
// separately. Comparing the two counts alone is one number hiding two faults: a memory
// missing from the index and an orphan index row cancel each other out exactly, and the
// check goes quiet while a memory nobody can find sits in the store. That is the failure
// this whole command exists to catch, so it asks the question that cannot cancel.
func (s *Store) checkSearchIndex(r *doctorReport) {
	if missing := s.countOf("SELECT COUNT(*) FROM memories WHERE id NOT IN " +
		"(SELECT rowid FROM mem_fts)"); missing > 0 {
		r.problem("reindex", "%d memories are not in the search "+
			"index: recall cannot find them at all", missing)
	}
	if orphan := s.countOf("SELECT COUNT(*) FROM mem_fts WHERE rowid NOT IN " +
		"(SELECT id FROM memories)"); orphan > 0 {
		r.problem("reindex", "%d index rows point at deleted "+
			"memories", orphan)
	}
}

func (s *Store) checkIdentityHashes(r *doctorReport) {
	if nullh := s.idList("SELECT id FROM memories WHERE hash IS NULL"); len(nullh) > 0 {
		r.problem("rehash", "%d memories have no identity hash "+
			"and can never be updated in place: %v", len(nullh), clipIDs(nullh, 10))
	}
}

// checkSupersession finds links to memories that no longer exist, and cycles. The links
// must form a DAG: a cycle would hang the walk that forwards a retracted fact to its
// current version.
func (s *Store) checkSupersession(r *doctorReport) {
	dangling := s.idList("SELECT id FROM memories WHERE superseded_by IS NOT NULL " +
		"AND superseded_by NOT IN (SELECT id FROM memories)")
	if len(dangling) > 0 {
		r.problem("unlink", "%d memories are superseded by a "+
			"memory that no longer exists, so they never surface: %v",
			len(dangling), clipIDs(dangling, 10))
	}
	next := map[int64]int64{}
	if rows, err := s.DB.Query("SELECT id, superseded_by FROM memories WHERE " +
		"superseded_by IS NOT NULL"); err == nil {
		for rows.Next() {
			var id, nxt int64
			if err := rows.Scan(&id, &nxt); err == nil {
				next[id] = nxt
			}
		}
		rows.Close()
	}
	r.cycles = supersessionCycles(next)
	for _, c := range r.cycles {
		r.problem("unlink", "supersession cycle %v", c)
	}
}

// checkOwnHashes finds live memories whose identity hash is not their project, scope and
// title (or the title with its id, the form a retracted row takes). remember cannot find
// such a memory to refine, so the next write of the same fact stores a second copy. An
// earlier relabel left these behind. Only this project's rows: --fix rewrites nothing
// else, and listing another project's ids would name its rows.
func (s *Store) checkOwnHashes(r *doctorReport) {
	var bad []int64
	if rows, err := s.DB.Query("SELECT id, scope, title, hash FROM memories WHERE project=? "+
		"AND superseded_by IS NULL AND hash IS NOT NULL ORDER BY id", s.Env.Project); err == nil {
		for rows.Next() {
			var id int64
			var scope, title, h string
			if rows.Scan(&id, &scope, &title, &h) != nil {
				continue
			}
			if h != store.ContentHash(s.Env.Project, scope, title) &&
				h != store.ContentHash(s.Env.Project, scope, fmt.Sprintf("%s #%d", title, id)) {
				bad = append(bad, id)
			}
		}
		rows.Close()
	}
	if len(bad) > 0 {
		r.problem("rehash", "%d memories carry an identity hash "+
			"that is not their own project, scope and title, so remember cannot find them to "+
			"refine and stores a second copy instead: %v", len(bad), clipIDs(bad, 10))
	}
}

// checkInfiniteReadings finds non-finite measure values. One cannot be drawn, and before
// the write path refused them one crashed every brief. The renderers skip them now; only
// their owner can say what they were, so --fix leaves them.
func (s *Store) checkInfiniteReadings(r *doctorReport) {
	var found []string
	if rows, err := s.DB.Query("SELECT id, key FROM measures WHERE project=? AND "+
		"(value > 1.7976931348623157e308 OR value < -1.7976931348623157e308) ORDER BY id",
		s.Env.Project); err == nil {
		for rows.Next() {
			var id int64
			var k string
			if rows.Scan(&id, &k) == nil {
				found = append(found, fmt.Sprintf("#%d %s", id, k))
			}
		}
		rows.Close()
	}
	if len(found) > 0 {
		r.problem("", "%d measure readings are infinite and "+
			"are left out of every trend: %s\n    --fix cannot know what they should have "+
			"been: os-brain measure forget <id>, then add the real reading",
			len(found), strings.Join(clipStrings(found, 8), ", "))
	}
}

func (s *Store) checkConfidence(r *doctorReport) {
	known := sortedKeysOfConfidence()
	if bad := s.countOf("SELECT COUNT(*) FROM memories WHERE confidence NOT IN ("+
		store.Placeholders(len(known))+")", store.ToIfaces(known)...); bad > 0 {
		r.problem("confidence", "%d memories carry an unknown "+
			"confidence value", bad)
	}
}

func (s *Store) checkIntegrity(r *doctorReport) {
	var ic string
	if err := s.DB.QueryRow("PRAGMA integrity_check").Scan(&ic); err != nil {
		r.problem("", "sqlite integrity_check failed: %v", err)
	} else if ic != "ok" {
		r.problem("", "%s", "sqlite integrity_check: "+ic)
	}
}

// noteUndeliveredNotes finds notes sent from here that reached nobody. `note` refuses an
// unknown recipient now; these were parked under the old rule and reported to their
// sender as delivered. Reported, never deleted: the text may be the only copy of what
// somebody meant to say.
func (s *Store) noteUndeliveredNotes(r *doctorReport) {
	g := s.GlobalConnect(false)
	if g == nil {
		return
	}
	defer s.CloseGlobal(g)
	if !s.TableExistsIn(g, "notes") || !s.TableExistsIn(g, "projects") {
		return
	}
	lost, err := g.Query("SELECT id, to_project FROM notes WHERE from_project=? "+
		"AND acked_at IS NULL AND to_project <> ? AND to_project NOT IN "+
		"(SELECT name FROM projects)", s.Env.Project, s.Env.Project)
	if err != nil {
		return
	}
	defer lost.Close()
	for lost.Next() {
		var id int64
		var to string
		if lost.Scan(&id, &to) == nil {
			r.note("note #%d, sent from here, is "+
				"addressed to %s, which is not a project in this brain: nobody "+
				"has read it. Send it again to the right name", id, pyfmt.Repr(textguard.Clip(to, 60)))
		}
	}
}

// noteUnrecordedSupersessions finds supersessions said in prose and never recorded as a
// link (see SupersedeClaims for why that matters).
func (s *Store) noteUnrecordedSupersessions(r *doctorReport) {
	rows, err := s.DB.Query("SELECT id, title, body FROM memories WHERE project=? AND "+
		"superseded_by IS NULL", s.Env.Project)
	if err != nil {
		return
	}
	type claim struct{ row, target int64 }
	var claims []claim
	for rows.Next() {
		var id int64
		var title, body string
		if rows.Scan(&id, &title, &body) == nil {
			for _, t := range memory.SupersedeClaims(title, body) {
				claims = append(claims, claim{id, t})
			}
		}
	}
	rows.Close()
	for _, cl := range claims {
		state := memory.ClaimMissing
		var sup sql.NullInt64
		if err := s.DB.QueryRow("SELECT superseded_by FROM memories WHERE id=? AND project=?",
			cl.target, s.Env.Project).Scan(&sup); err == nil {
			state = memory.ClaimLive
			if sup.Valid {
				state = memory.ClaimRetracted
			}
		}
		if n := memory.UnrecordedClaimNote(cl.row, cl.target, state); n != "" {
			r.notes = append(r.notes, n)
		}
	}
}

// noteProvenance looks at how this project's live memories are pinned, dated and sourced.
func (s *Store) noteProvenance(r *doctorReport) {
	npin := s.countOf("SELECT COUNT(*) FROM memories WHERE project=? AND pinned=1 AND "+
		"superseded_by IS NULL", s.Env.Project)
	r.nlive = s.countOf("SELECT COUNT(*) FROM memories WHERE project=? AND superseded_by IS NULL",
		s.Env.Project)
	nlive := r.nlive
	// Past about a fifth pinned, "read first" stops meaning anything.
	if nlive > 0 && float64(npin)/float64(nlive) > 0.2 && npin > 8 {
		r.note("%d of %d memories are pinned (%d%%): past about "+
			"a fifth, 'read first' stops meaning anything (os-brain review says which)",
			npin, nlive, int(100*float64(npin)/float64(nlive)+0.5))
	}
	// An undated 'confirmed' cannot be aged, so review --stale-days will never surface it
	// however load-bearing it is.
	if undated := s.countOf("SELECT COUNT(*) FROM memories WHERE project=? AND "+
		"confidence='confirmed' AND source_date='' AND verified_at IS NULL AND "+
		"superseded_by IS NULL", s.Env.Project); undated > 0 {
		r.note("%d confirmed memories carry no source_date and "+
			"have never been verified: nothing can age them, so review --stale-days will "+
			"not surface them (os-brain update <id> --source-date, or verify <id>)", undated)
	}
	nosource := s.countOf("SELECT COUNT(*) FROM memories WHERE project=? AND source='session' "+
		"AND superseded_by IS NULL", s.Env.Project)
	if nlive > 0 && float64(nosource)/float64(nlive) > 0.5 && nosource > 5 {
		r.note("%d of %d memories still carry the default source "+
			"'session', which records that provenance was never set", nosource, nlive)
	}
}

// checkMeasureBands looks at this project's measures: bands with nothing to check, series
// that mix units, and readings outside their band.
func (s *Store) checkMeasureBands(r *doctorReport) {
	// A band declared for a key that has no readings is a check that will never run, and
	// it looks exactly like a check that keeps passing.
	orphans := s.stringList("SELECT key FROM measure_limits WHERE project=? AND key NOT IN "+
		"(SELECT DISTINCT key FROM measures WHERE project=?)", s.Env.Project, s.Env.Project)
	if len(orphans) > 0 {
		r.note("bands declared for keys with no readings: %s "+
			"(nothing is being checked)", strings.Join(clipStrings(orphans, 8), ", "))
	}

	mixed := s.stringList("SELECT key, COUNT(DISTINCT unit) u FROM measures WHERE "+
		"project=? AND unit != '' GROUP BY key HAVING u > 1", s.Env.Project)
	if len(mixed) > 0 {
		// Naming the command matters: --fix cannot repair this one, because choosing which
		// unit is right needs to know what was measured. Reporting a fault with no route
		// out is what sends people to raw SQL.
		r.problem("", "%d measure series mix units, so "+
			"the trend is arithmetic on unlike numbers: %s\n    "+
			"--fix cannot choose for you: os-brain measure series <key> to see them, then "+
			"os-brain measure relabel <id> --unit <right> (renames, never converts)",
			len(mixed), strings.Join(clipStrings(mixed, 8), ", "))
	}

	var breached []string
	for _, sm := range measure.New(s.Store).MeasureOverview("") {
		if sm.Breach != "" {
			breached = append(breached, fmt.Sprintf("%s %s %s", sm.Key, measure.Fm(sm.Latest), sm.Breach))
		}
	}
	if len(breached) > 0 {
		plural := ""
		if len(breached) > 1 {
			plural = "s"
		}
		r.note("%d measure%s outside the declared band: %s",
			len(breached), plural, strings.Join(clipStrings(breached, 8), ", "))
	}
}

// noteFileHealth checks the database file itself: owner-only permissions, since it holds
// every memory in cleartext, and a write-ahead log that has grown without a checkpoint.
func (s *Store) noteFileHealth(r *doctorReport) {
	if info, err := os.Stat(s.Env.DBPath); err == nil {
		if mode := info.Mode().Perm(); mode != 0o600 {
			r.note("database mode is 0o%o, expected 0o600", mode)
		}
	}
	if info, err := os.Stat(s.Env.DBPath + "-wal"); err == nil && info.Size() > 8*1024*1024 {
		r.note("write-ahead log is %dKB (os-brain vacuum "+
			"checkpoints it)", info.Size()/1024)
	}
}

// repair applies the fix for each kind of problem found and returns what it has to say
// about it.
func (s *Store) repair(r *doctorReport) ([]string, error) {
	kinds := map[string]bool{}
	for _, p := range r.problems {
		if p.kind != "" {
			kinds[p.kind] = true
		}
	}
	if kinds["reindex"] {
		if err := s.Reindex(); err != nil {
			return nil, err
		}
	}
	var fixNotes []string
	// Every repair below rewrites memories, and the old links and hashes are not
	// recoverable from anything else. So this project is dumped first, the way prune does,
	// and only this project's rows are touched. A shared store's other projects are theirs
	// to fix: --fix once nulled every project's links with nothing kept.
	if kinds["rehash"] || kinds["unlink"] || kinds["confidence"] {
		backup := filepath.Join(s.Env.Here, "brain-doctor-"+dump.Stamp()+".json")
		if _, err := dump.New(s.Store).DumpProject(fsguard.Dump{Path: backup, Root: s.Env.ProjectRoot(),
			Marker: dump.BackupMarker}); err != nil {
			return nil, fmt.Errorf("not repaired: the backup taken first failed: %w", err)
		}
		fixNotes = append(fixNotes, "backed up this project to "+backup+" first")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if kinds["rehash"] {
		if err := s.Rehash(tx, s.Env.Project); err != nil {
			return nil, err
		}
	}
	if kinds["unlink"] {
		notes, err := s.unlinkBroken(tx, r.cycles)
		if err != nil {
			return nil, err
		}
		fixNotes = append(fixNotes, notes...)
	}
	if kinds["confidence"] {
		known := sortedKeysOfConfidence()
		args := append([]interface{}{store.DefaultConfidence, s.Env.Project}, store.ToIfaces(known)...)
		if _, err := tx.Exec("UPDATE memories SET confidence=? WHERE project=? AND "+
			"confidence NOT IN ("+store.Placeholders(len(known))+")", args...); err != nil {
			return nil, err
		}
	}
	return fixNotes, tx.Commit()
}

// unlinkBroken clears this project's links to memories that no longer exist, and breaks
// each cycle at one edge: the newest row's, which makes it the current version and leaves
// every other member and every chain into the cycle retracted. Clearing every link on
// every path once republished facts that had been retracted on purpose.
func (s *Store) unlinkBroken(tx *sql.Tx, cycles [][]int64) ([]string, error) {
	if _, err := tx.Exec("UPDATE memories SET superseded_by=NULL WHERE project=? AND "+
		"superseded_by NOT IN (SELECT id FROM memories)", s.Env.Project); err != nil {
		return nil, err
	}
	var notes []string
	for _, c := range cycles {
		newest := c[len(c)-1]
		res, err := tx.Exec("UPDATE memories SET superseded_by=NULL WHERE id=? AND "+
			"project=?", newest, s.Env.Project)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			notes = append(notes, fmt.Sprintf("cycle %v is another project's: "+
				"left for it to repair", c))
		} else {
			notes = append(notes, fmt.Sprintf("cycle %v: #%d is the current "+
				"version now", c, newest))
		}
	}
	return notes, nil
}

func (s *Store) printDoctor(w io.Writer, a DoctorArgs, r *doctorReport, nmem int, fixNotes []string) error {
	// Say what is being counted. 'N memories across every project in this store' next to
	// stats' 'memories=M' looks like a disagreement when it is two different denominators,
	// and 'every project' reads oddly for a store holding exactly one.
	scopeNote := fmt.Sprintf("%d rows including superseded", nmem)
	if s.countOf("SELECT COUNT(DISTINCT project) FROM memories") > 1 {
		scopeNote = fmt.Sprintf("%d rows total, every project in this store", nmem)
	}
	bar := strings.Repeat("=", 72)
	lines := []string{bar, strings.ToUpper(s.Env.Project) + " BRAIN — DOCTOR", bar,
		fmt.Sprintf("%d live memories for %s · %s · schema v%d · %s",
			r.nlive, s.Env.Project, scopeNote, store.SchemaVersion, s.Env.DBPath)}
	msgs := make([]string, 0, len(r.problems))
	for _, p := range r.problems {
		msgs = append(msgs, p.msg)
	}
	if len(msgs) > 0 {
		lines = append(lines, "", "## PROBLEMS")
		for _, m := range msgs {
			lines = append(lines, "  - "+m)
		}
		tail := "os-brain doctor --fix repairs the repairable ones in this project, after " +
			"backing it up"
		if a.Fix {
			tail = "repaired this project's (re-run to confirm)"
		}
		lines = append(lines, "", "  "+tail)
		for _, n := range fixNotes {
			lines = append(lines, "  "+n)
		}
	} else {
		lines = append(lines, "", "no problems found")
	}
	notes := r.notes
	if len(notes) > 0 {
		lines = append(lines, "", "## NOTES")
		for _, n := range notes {
			lines = append(lines, "  - "+n)
		}
	}
	if notes == nil {
		notes = []string{}
	}
	return store.Out(w, a.JSON, strings.Join(lines, "\n"), map[string]interface{}{
		"memories": nmem, "problems": msgs, "notes": notes,
		"fixed": a.Fix && len(r.problems) > 0})
}

// Vacuum checkpoints the write-ahead log into the database and compacts the file.
func (s *Store) Vacuum(w io.Writer, asJSON bool) error {
	if _, err := s.DB.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return err
	}
	if _, err := s.DB.Exec("VACUUM"); err != nil {
		return err
	}
	info, err := os.Stat(s.Env.DBPath)
	if err != nil {
		return err
	}
	return store.Out(w, asJSON, fmt.Sprintf("checkpointed and vacuumed; database is now %dKB",
		info.Size()/1024), map[string]interface{}{"bytes": info.Size()})
}

func (s *Store) idList(q string, args ...interface{}) []int64 {
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// stringList is the first column of every row a query returns, as text. A row whose
// columns do not scan is skipped; a query that fails returns nothing.
func (s *Store) stringList(q string, args ...interface{}) []string {
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil
	}
	var out []string
	for rows.Next() {
		var first string
		dest := []interface{}{&first}
		for i := 1; i < len(cols); i++ {
			dest = append(dest, new(interface{}))
		}
		if err := rows.Scan(dest...); err == nil {
			out = append(out, first)
		}
	}
	return out
}

func clipIDs(ids []int64, n int) []int64 {
	if len(ids) > n {
		return ids[:n]
	}
	return ids
}

func clipStrings(ss []string, n int) []string {
	if len(ss) > n {
		return ss[:n]
	}
	return ss
}

func sortedKeysOfConfidence() []string {
	out := make([]string, 0, len(store.Confidence))
	for k := range store.Confidence {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// supersessionCycles returns each cycle's members, ascending, and nothing for the chains
// that merely lead into one: those are retracted on purpose and stay that way.
func supersessionCycles(next map[int64]int64) [][]int64 {
	starts := make([]int64, 0, len(next))
	for id := range next {
		starts = append(starts, id)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	done := map[int64]bool{}
	var out [][]int64
	for _, start := range starts {
		pos := map[int64]int{}
		var path []int64
		cur := start
		for !done[cur] {
			if i, ok := pos[cur]; ok {
				c := append([]int64(nil), path[i:]...)
				sort.Slice(c, func(a, b int) bool { return c[a] < c[b] })
				out = append(out, c)
				break
			}
			nxt, ok := next[cur]
			if !ok {
				break
			}
			pos[cur] = len(path)
			path = append(path, cur)
			cur = nxt
		}
		for _, p := range path {
			done[p] = true
		}
	}
	return out
}
