package dump

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/chka/os-brain/internal/measure"
	"github.com/chka/os-brain/internal/pyfmt"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/textguard"
	"github.com/chka/os-brain/internal/timefmt"
)

// `import` reads a JSON dump back into the store. A dump is a file somebody handed over,
// so it is treated as untrusted input from start to finish (see import_rows.go): the
// whole dump is validated before the first row is written, and every rule an ordinary
// write follows applies to its rows too.

type ImportArgs struct {
	File        string
	IMeanIt     bool
	DryRun      bool
	Events      bool
	AllProjects bool
	AllowSecret bool
	// Force lets a dump bring guardrails and retract a pinned memory or a guardrail. Both
	// bind every later session, so somebody who has read the dry run says so.
	Force bool
	JSON  bool
}

// importRow is one memory of a dump as it will be written, and what it lands on.
type importRow struct {
	im              *ImportedMemory
	hash            string
	old, next       int64 // the dump's own id for this row and for its successor
	hasOld, hasNext bool
	// dupOf is the earlier row of this dump holding the same identity, or -1.
	dupOf int
	// existing is the memory already in the store under this identity, 0 for none.
	existing                      int64
	existingPinned, existingGuard bool
	existingLive                  bool
}

// importLink is a supersession to record, as indexes into importPlan.rows: self is
// superseded by next.
type importLink struct{ self, next int }

// importPlan is everything an import has read and decided before it writes anything.
type importPlan struct {
	a              ImportArgs
	self           string
	now            int64
	notImported    map[string]int // tables the dump carries that import leaves alone
	foreignByLabel map[string]int // rows per other project's label
	dumpEvents     int            // events in the dump, imported or not

	rows                             []*importRow
	foreign, pinsDropped, downgraded int
	events, tasks, measures, limits  []map[string]interface{}

	links              []importLink
	retract, protected []*importRow // existing live memories the dump supersedes
	dangling           int
	guards             []string // titles of new guardrails

	added   []string // titles of the memories that will be written
	present int      // memories already in the store, or repeated within the dump
}

// importResult is what writing the plan did.
type importResult struct {
	relinked, events, measures, measuresInStore int
}

func (s *Store) Import(w io.Writer, a ImportArgs) error {
	dump, err := readDump(a.File)
	if err != nil {
		return err
	}
	// Every key is checked against this store's own columns before anything is read for
	// meaning.
	notImported, err := s.checkDumpShape(s.DB, dump)
	if err != nil {
		return err
	}
	p := &importPlan{a: a, self: s.Env.Project, now: timefmt.Now(), notImported: notImported,
		dumpEvents: len(jsonList(dump["events"]))}
	p.foreignByLabel = foreignLabels(dump, p.self, a.Events)
	if err := s.checkForeignLabels(p); err != nil {
		return err
	}
	// Validated in full BEFORE anything is written: an unknown type on row 40 must not
	// leave 39 rows imported.
	if err := s.collectMemories(p, dump); err != nil {
		return err
	}
	if err := p.collectActivity(dump); err != nil {
		return err
	}
	if err := p.collectMeasures(dump); err != nil {
		return err
	}
	p.linkSupersessions()
	p.findNewGuardrails()
	if !a.DryRun && !a.Force {
		if err := p.refuseUnreviewed(); err != nil {
			return err
		}
	}
	p.countAdded()
	if a.DryRun {
		return p.printDryRun(w)
	}
	res, err := s.applyImport(p)
	if err != nil {
		return err
	}
	return p.printResult(w, res)
}

// readDump reads and parses the file, refusing a dump from a newer schema than this
// binary understands.
func readDump(file string) (map[string]interface{}, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		// Name the path the caller typed, not the syscall that failed it.
		if pe, ok := err.(*os.PathError); ok {
			return nil, fmt.Errorf("%s: %v", file, pe.Err)
		}
		return nil, err
	}
	var dump map[string]interface{}
	if err := json.Unmarshal(data, &dump); err != nil {
		return nil, fmt.Errorf("%s is not readable JSON: %w", file, err)
	}
	if v := JSONInt(dump["schema_version"]); int(v) > store.SchemaVersion {
		return nil, fmt.Errorf("this dump is schema v%d; this brain understands up to v%d. "+
			"Upgrade before importing", v, store.SchemaVersion)
	}
	return dump, nil
}

// checkForeignLabels guards rows labelled as another project's. Writing under another
// project's label was once the one place a cross-project write had a lower bar than
// everywhere else, and such rows read afterwards as that project's own work. Without
// --all-projects they are skipped. With it, a private project is never written into,
// whatever the flags, and anything else needs --i-mean-it.
func (s *Store) checkForeignLabels(p *importPlan) error {
	if !p.a.AllProjects || len(p.foreignByLabel) == 0 {
		return nil
	}
	for _, name := range store.SortedStrings(p.foreignByLabel) {
		if s.ImportPrivate(name) {
			return fmt.Errorf("this dump writes into %s, which is a private project (or "+
				"the index cannot say it is not). Import never writes into a private "+
				"project: that project imports its own rows", pyfmt.Repr(name))
		}
	}
	if p.a.IMeanIt || p.a.DryRun {
		return nil
	}
	n := 0
	for _, c := range p.foreignByLabel {
		n += c
	}
	return fmt.Errorf("%d rows of this dump are labelled as another project's: %s.\n"+
		"--all-projects writes them under that label, where they read as that "+
		"project's own work.\nIf this is a restore of a whole store you own, add "+
		"--i-mean-it. If it is not, drop --all-projects and they will be skipped",
		n, labelList(p.foreignByLabel))
}

// owns says which project a row is filed under, and whether this import writes it.
func (p *importPlan) owns(r map[string]interface{}) (string, bool) {
	owner := JSONStr(r["project"], p.self)
	return owner, owner == p.self || p.a.AllProjects
}

// collectMemories sanitises every memory row this import writes and looks up what each
// one lands on: an earlier row of the same dump, or a memory already in the store.
func (s *Store) collectMemories(p *importPlan, dump map[string]interface{}) error {
	byHash := map[string]int{}
	for i, m := range jsonList(dump["memories"]) {
		owner, ok := p.owns(m)
		if !ok {
			p.foreign++
			continue
		}
		if err := GuardRow(p.a.AllowSecret, "memories", i, m); err != nil {
			return err
		}
		im, err := SanitiseMemory(m, owner, "import", p.now)
		if err != nil {
			return fmt.Errorf("row %d of this dump: %v", i+1, err)
		}
		r := &importRow{im: im, hash: store.ContentHash(owner, im.scope, im.title), dupOf: -1}
		if raw, ok := m["id"]; ok && raw != nil {
			r.old, r.hasOld = JSONInt(raw), true
		}
		if raw, ok := m["superseded_by"]; ok && raw != nil && JSONInt(raw) != 0 {
			r.next, r.hasNext = JSONInt(raw), true
		}
		if j, seen := byHash[r.hash]; seen {
			r.dupOf = j
		} else {
			byHash[r.hash] = len(p.rows)
			if err := s.findExisting(r); err != nil {
				return err
			}
			if r.existing == 0 {
				if im.pinDropped {
					p.pinsDropped++
				}
				if im.downgraded {
					p.downgraded++
				}
			}
		}
		p.rows = append(p.rows, r)
	}
	return nil
}

// findExisting fills in the memory already stored under the row's identity, if any.
func (s *Store) findExisting(r *importRow) error {
	var pinned int
	var typ string
	var sup sql.NullInt64
	err := s.DB.QueryRow("SELECT id, pinned, mem_type, superseded_by FROM memories "+
		"WHERE hash=?", r.hash).Scan(&r.existing, &pinned, &typ, &sup)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	r.existingPinned, r.existingGuard = pinned != 0, store.Contains(store.GuardrailTypes, typ)
	r.existingLive = !sup.Valid
	return nil
}

// pick is the rows of one table this import writes, each passed through the secret scan.
func (p *importPlan) pick(dump map[string]interface{}, table string) ([]map[string]interface{}, error) {
	var out []map[string]interface{}
	for i, r := range jsonList(dump[table]) {
		if _, ok := p.owns(r); !ok {
			continue
		}
		if err := GuardRow(p.a.AllowSecret, table, i, r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// collectActivity reads events and tasks, only with --events. Text that lands in a brief
// must be single-line, as it must on an ordinary write.
func (p *importPlan) collectActivity(dump map[string]interface{}) error {
	if !p.a.Events {
		return nil
	}
	var err error
	if p.events, err = p.pick(dump, "events"); err != nil {
		return err
	}
	if p.tasks, err = p.pick(dump, "tasks"); err != nil {
		return err
	}
	for i, e := range p.events {
		if err := textguard.CheckLines("the summary", JSONStr(e["summary"], ""), "the kind",
			JSONStr(e["kind"], "")); err != nil {
			return fmt.Errorf("event %d of this dump: %v", i+1, err)
		}
	}
	for i, t := range p.tasks {
		if err := textguard.CheckLine("the title", JSONStr(t["title"], "")); err != nil {
			return fmt.Errorf("task %d of this dump: %v", i+1, err)
		}
	}
	return nil
}

// collectMeasures reads measure readings and bands, refusing a value the measure command
// itself would refuse.
func (p *importPlan) collectMeasures(dump map[string]interface{}) error {
	var err error
	if p.measures, err = p.pick(dump, "measures"); err != nil {
		return err
	}
	for i, m := range p.measures {
		v := jsonFloat(m["value"])
		if err := measure.CheckMeasureValue(fmt.Sprintf("measure %d of this dump", i+1), &v); err != nil {
			return err
		}
		if err := textguard.CheckLines("the label", JSONStr(m["label"], ""), "the unit",
			JSONStr(m["unit"], "")); err != nil {
			return fmt.Errorf("measure %d of this dump: %v", i+1, err)
		}
	}
	p.limits, err = p.pick(dump, "measure_limits")
	return err
}

// linkSupersessions remaps the dump's supersession links onto its rows: export ids do not
// survive autoincrement. A link whose superseded row is ALREADY in the store retracts a
// live memory somebody else wrote, so it is recorded for the report, and a pinned memory
// or a guardrail is retracted only on --force. Import once had no such check, and a forged
// "confirmed, pinned, updated 2100" row retracted a pinned guardrail with the dry run
// saying only "would add 1".
func (p *importPlan) linkSupersessions() {
	oldIdx := map[int64]int{}
	for i, r := range p.rows {
		if r.hasOld {
			oldIdx[r.old] = i
		}
	}
	root := func(i int) int {
		for p.rows[i].dupOf >= 0 {
			i = p.rows[i].dupOf
		}
		return i
	}
	retracting := map[int]bool{}
	for i, r := range p.rows {
		if !r.hasNext || !r.hasOld {
			continue
		}
		ni, ok := oldIdx[r.next]
		if !ok {
			p.dangling++
			continue
		}
		si, ni := root(i), root(ni)
		if si == ni {
			continue
		}
		if p.rows[si].im.owner != p.rows[ni].im.owner {
			// A supersession never crosses projects: one project's row cannot retract
			// another's.
			p.dangling++
			continue
		}
		if t := p.rows[si]; t.existing != 0 {
			if !t.existingLive {
				continue
			}
			if !retracting[si] {
				retracting[si] = true
				p.retract = append(p.retract, t)
				if t.existingPinned || t.existingGuard {
					p.protected = append(p.protected, t)
				}
			}
		}
		p.links = append(p.links, importLink{si, ni})
	}
}

// findNewGuardrails lists the guardrails the dump would add. A guardrail binds every
// session of the project it lands in, so a file cannot bring one in unread.
func (p *importPlan) findNewGuardrails() {
	for _, r := range p.rows {
		if r.dupOf < 0 && r.existing == 0 && store.Contains(store.GuardrailTypes, r.im.typ) {
			p.guards = append(p.guards, r.im.title)
		}
	}
}

// refuseUnreviewed stops an import that retires a rule or brings a new one, unless the
// person passes --force after reading the dry run.
func (p *importPlan) refuseUnreviewed() error {
	if len(p.protected) > 0 {
		var list []string
		for _, r := range p.protected {
			list = append(list, "  - "+describeExisting(r))
		}
		return fmt.Errorf("this dump retracts %d pinned memories or guardrails already "+
			"in this brain:\n%s\nA file somebody handed over does not get to retire a "+
			"rule. Read the dry run, and pass --force only if these really are replaced",
			len(p.protected), strings.Join(list, "\n"))
	}
	if len(p.guards) > 0 {
		return fmt.Errorf("this dump brings %d guardrails (policy or constraint), and a "+
			"guardrail binds every session of the project it lands in: %s.\nRead them "+
			"with --dry-run, and pass --force if they are rules this project keeps",
			len(p.guards), pyfmt.Repr(textguard.Clip(p.guards[0], 70)))
	}
	return nil
}

// describeExisting names the stored memory a row lands on, tagged when it is protected.
func describeExisting(r *importRow) string {
	tag := ""
	if r.existingPinned {
		tag += " [pinned]"
	}
	if r.existingGuard {
		tag += " [guardrail]"
	}
	return fmt.Sprintf("#%d %s%s", r.existing, textguard.OneLine(textguard.Clip(r.im.title, 70)), tag)
}

func (p *importPlan) countAdded() {
	for _, r := range p.rows {
		if r.dupOf >= 0 || r.existing != 0 {
			p.present++
		} else {
			p.added = append(p.added, r.im.title)
		}
	}
}

// reportLines appends what the dry run and the result both say about the dump: what it
// retracts, the guardrails it brings, what it claimed and did not get, and what it left
// alone.
func (p *importPlan) reportLines(lines []string) []string {
	if len(p.retract) > 0 {
		lines = append(lines, fmt.Sprintf("  retracts %d memories already in this brain "+
			"(a row in the dump supersedes them):", len(p.retract)))
		for _, r := range p.retract {
			lines = append(lines, "    - "+describeExisting(r))
		}
	}
	if len(p.guards) > 0 {
		lines = append(lines, fmt.Sprintf("  brings %d guardrails (needs --force):", len(p.guards)))
		for i, g := range p.guards {
			if i == 10 {
				break
			}
			lines = append(lines, "    ! "+textguard.OneLine(textguard.Clip(g, 70)))
		}
	}
	if p.pinsDropped > 0 {
		lines = append(lines, fmt.Sprintf("  %d arrive unpinned: a dump cannot pin "+
			"(os-brain update <id> --pin, one at a time, after reading them)", p.pinsDropped))
	}
	if p.downgraded > 0 {
		lines = append(lines, fmt.Sprintf("  %d claimed 'confirmed' with nothing checkable "+
			"and arrive as 'reported'", p.downgraded))
	}
	if p.a.AllProjects && len(p.foreignByLabel) > 0 {
		lines = append(lines, "  writes under other projects' labels: "+
			labelList(p.foreignByLabel))
	}
	var left []string
	for _, k := range store.SortedStrings(p.notImported) {
		if p.notImported[k] > 0 {
			left = append(left, fmt.Sprintf("%d %s", p.notImported[k], k))
		}
	}
	if len(left) > 0 {
		lines = append(lines, "  not imported: "+strings.Join(left, ", ")+
			" (import writes memories, events, tasks and measures; the spine, which every "+
			"project shares, never comes from a dump)")
	}
	return lines
}

func (p *importPlan) printDryRun(w io.Writer) error {
	lines := []string{fmt.Sprintf("would add %d memories, skip %d already present",
		len(p.added), p.present)}
	if p.foreign > 0 {
		lines = append(lines, fmt.Sprintf("  and skip %d belonging to another project "+
			"(--all-projects to restore a whole store)", p.foreign))
	}
	for i, t := range p.added {
		if i == 20 {
			break
		}
		lines = append(lines, "  + "+textguard.OneLine(t))
	}
	if p.a.Events {
		lines = append(lines, fmt.Sprintf("would append %d events", len(p.events)))
	}
	lines = p.reportLines(lines)
	var retractIDs []int64
	for _, r := range p.retract {
		retractIDs = append(retractIDs, r.existing)
	}
	added := p.added
	if added == nil {
		added = []string{}
	}
	return store.Out(w, p.a.JSON, strings.Join(lines, "\n"),
		map[string]interface{}{"would_add": len(added), "would_skip": p.present,
			"would_foreign": p.foreign, "titles": added, "applied": false,
			"would_retract": retractIDs, "guardrails": len(p.guards),
			"pins_dropped": p.pinsDropped, "not_imported": p.notImported})
}

// applyImport writes the plan in one transaction: memories, then their supersession
// links, then activity and measures. Anything that fails rolls all of it back.
func (s *Store) applyImport(p *importPlan) (importResult, error) {
	var res importResult
	tx, err := s.DB.Begin()
	if err != nil {
		return res, err
	}
	defer tx.Rollback()

	ids, err := s.insertImportedMemories(tx, p)
	if err != nil {
		return res, err
	}
	if res.relinked, err = writeImportedLinks(tx, p, ids); err != nil {
		return res, err
	}
	if res.events, err = p.insertActivity(tx); err != nil {
		return res, err
	}
	if res.measures, res.measuresInStore, err = p.insertMeasures(tx); err != nil {
		return res, err
	}
	return res, tx.Commit()
}

// insertImportedMemories writes each new memory and returns, per row of the plan, the id
// it now has in the store (0 for a repeat within the dump). Every imported row carries
// trust 'imported' and starts unpinned.
func (s *Store) insertImportedMemories(tx *sql.Tx, p *importPlan) ([]int64, error) {
	ids := make([]int64, len(p.rows))
	for i, r := range p.rows {
		if r.dupOf >= 0 {
			continue
		}
		if r.existing != 0 {
			ids[i] = r.existing
			continue
		}
		var err error
		if ids[i], err = s.InsertImportedMemory(tx, r.im, r.hash); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// InsertImportedMemory writes one sanitised memory under the identity hash, indexes it for
// search and returns its id. Import and absorb both store rows from somebody else's brain
// through it, so whichever command brought a row in, it arrives unpinned with trust
// 'imported'.
func (s *Store) InsertImportedMemory(tx *sql.Tx, im *ImportedMemory, hash string) (int64, error) {
	res, err := tx.Exec("INSERT INTO memories(project,scope,mem_type,title,body,source,"+
		"source_date,source_commit,confidence,importance,pinned,use_count,last_used,"+
		"verified_at,session,trust,hash,created_at,updated_at) "+
		"VALUES(?,?,?,?,?,?,?,?,?,?,0,?,?,?,?,'imported',?,?,?)",
		im.owner, im.scope, im.typ, im.title, im.body, im.source, im.sourceDate,
		im.sourceCommit, im.confidence, im.importance, im.useCount, im.lastUsed,
		im.verifiedAt, im.session, hash, im.createdAt, im.updatedAt)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := s.FTSPut(tx, id, im.title, im.body); err != nil {
		return 0, err
	}
	return id, nil
}

func writeImportedLinks(tx *sql.Tx, p *importPlan, ids []int64) (int, error) {
	relinked := 0
	for _, l := range p.links {
		selfID, nextID := ids[l.self], ids[l.next]
		if p.rows[l.self].existing != 0 {
			// The same hand-over remember --supersedes does: the retracted row takes an
			// identity of its own and is unpinned, so it neither holds the title nor leads
			// the brief.
			if _, err := tx.Exec("UPDATE memories SET superseded_by=?, pinned=0, hash=? "+
				"WHERE id=?", nextID, store.SupersededHash(selfID, p.rows[l.self].hash), selfID); err != nil {
				return 0, err
			}
		} else if _, err := tx.Exec("UPDATE memories SET superseded_by=? WHERE id=?",
			nextID, selfID); err != nil {
			return 0, err
		}
		relinked++
	}
	return relinked, nil
}

// insertActivity appends events and open tasks, and returns how many rows it wrote.
// Tasks come with the events: both are activity rather than memory, both have no identity
// to merge on, and both are what --events was named for before tasks existed. Open ones
// only: a closed task is history the log already holds.
func (p *importPlan) insertActivity(tx *sql.Tx) (int, error) {
	n, now := 0, p.now
	for _, e := range p.events {
		owner, _ := p.owns(e)
		if _, err := tx.Exec("INSERT INTO events(project,ts,scope,kind,actor,summary,ref,session) "+
			"VALUES(?,?,?,?,?,?,?,?)", owner, ClampTS(JSONIntOr(e["ts"], now), now),
			JSONStr(e["scope"], "global"), JSONStr(e["kind"], "note"),
			JSONStr(e["actor"], "import"), JSONStr(e["summary"], ""),
			JSONStr(e["ref"], ""), JSONStr(e["session"], "")); err != nil {
			return 0, err
		}
		n++
	}
	for _, t := range p.tasks {
		if st := JSONStr(t["state"], "open"); st != "open" {
			continue
		}
		owner, _ := p.owns(t)
		if _, err := tx.Exec("INSERT INTO tasks(project,title,body,owner,scope,state,"+
			"position,sched_kind,sched_n,sched_unit,sched_at,sched_days,sched_says,tz,"+
			"due_at,notify,created_at,updated_at,session) "+
			"VALUES(?,?,?,?,?,'open',?,?,?,?,?,?,?,?,?,?,?,?,?)",
			owner, JSONStr(t["title"], ""), JSONStr(t["body"], ""),
			JSONStr(t["owner"], "agent"), JSONStr(t["scope"], "global"),
			jsonFloatOr(t["position"], 0), JSONStr(t["sched_kind"], ""),
			JSONIntOr(t["sched_n"], 0), JSONStr(t["sched_unit"], ""),
			JSONStr(t["sched_at"], ""), JSONStr(t["sched_days"], ""),
			JSONStr(t["sched_says"], ""), JSONStr(t["tz"], ""),
			jsonNullInt(t["due_at"]), JSONIntOr(t["notify"], 0),
			ClampTS(JSONIntOr(t["created_at"], now), now),
			ClampTS(JSONIntOr(t["updated_at"], now), now),
			JSONStr(t["session"], "")); err != nil {
			return 0, err
		}
		n++
	}
	return n, nil
}

// insertMeasures restores readings and bands, and returns how many readings it wrote and
// how many this project already had.
//
// Measure points carry no identity: two readings of the same key on the same day are a
// legitimate series, not a duplicate. So there is no safe merge, and importing into a
// project that already has readings would silently double the series it is meant to
// restore. They are restored only into an empty one, and the result says so when not.
func (p *importPlan) insertMeasures(tx *sql.Tx) (int, int, error) {
	var have int
	if err := tx.QueryRow("SELECT COUNT(*) FROM measures WHERE project=?",
		p.self).Scan(&have); err != nil {
		return 0, 0, err
	}
	if len(p.measures) == 0 || have > 0 {
		return 0, have, nil
	}
	n, now := 0, p.now
	for _, m := range p.measures {
		owner, _ := p.owns(m)
		if _, err := tx.Exec("INSERT INTO measures(project,scope,key,label,value,unit,"+
			"taken_on,taken_at,source,confidence,ref,note,created_at) "+
			"VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)",
			owner, JSONStr(m["scope"], "global"),
			JSONStr(m["key"], ""), JSONStr(m["label"], ""), jsonFloat(m["value"]),
			JSONStr(m["unit"], ""), JSONStr(m["taken_on"], ""),
			ClampTS(JSONIntOr(m["taken_at"], now), now), JSONStr(m["source"], "import"),
			store.NormConfidence(JSONStr(m["confidence"], "")), JSONStr(m["ref"], ""),
			JSONStr(m["note"], ""), ClampTS(JSONIntOr(m["created_at"], now), now)); err != nil {
			return 0, 0, err
		}
		n++
	}
	for _, lim := range p.limits {
		owner, _ := p.owns(lim)
		for _, b := range []string{"low", "high"} {
			if v, ok := lim[b].(float64); ok {
				if err := measure.CheckMeasureValue("a band's "+b, &v); err != nil {
					return 0, 0, err
				}
			}
		}
		if _, err := tx.Exec("INSERT INTO measure_limits(project,key,low,high,better,"+
			"unit,why,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(project,key) DO NOTHING",
			owner, JSONStr(lim["key"], ""), jsonNullFloat(lim["low"]),
			jsonNullFloat(lim["high"]), JSONStr(lim["better"], ""),
			JSONStr(lim["unit"], ""), JSONStr(lim["why"], ""),
			ClampTS(JSONIntOr(lim["updated_at"], now), now)); err != nil {
			return 0, 0, err
		}
	}
	return n, have, nil
}

func (p *importPlan) printResult(w io.Writer, res importResult) error {
	human := fmt.Sprintf("imported %d memories (%d already present), %d supersession links "+
		"restored, %d events", len(p.added), p.present, res.relinked, res.events)
	if p.foreign > 0 {
		human += fmt.Sprintf("\n  skipped %d memories belonging to other projects "+
			"(--all-projects to restore a whole store)", p.foreign)
	}
	foreignImported := 0
	for _, r := range p.rows {
		if r.dupOf < 0 && r.existing == 0 && r.im.owner != p.self {
			foreignImported++
		}
	}
	if foreignImported > 0 {
		// Without this, rows arriving under a label this project does not read as vanish
		// from every project-scoped view: stats, recall and doctor's live count all skip
		// them, with nothing anywhere saying why. absorb says the same.
		human += fmt.Sprintf("\n  %d are filed under another project, so THIS project will "+
			"not see them: it reads as %s. os-brain relabel <label> claims them.",
			foreignImported, pyfmt.Repr(p.self))
	}
	if p.dumpEvents > 0 && !p.a.Events {
		// Silence here is data loss during a restore, which is the case that matters.
		human += fmt.Sprintf("\n  %d events in this dump were NOT imported. Re-run with "+
			"--events if you are restoring a backup.", p.dumpEvents)
	}
	if res.measures > 0 {
		human += fmt.Sprintf("\n  restored %d measure points", res.measures)
	} else if len(p.measures) > 0 {
		human += fmt.Sprintf("\n  %d measure points were NOT imported: this project already "+
			"has %d. Readings have no identity, so importing would duplicate the series "+
			"rather than merge it.", len(p.measures), res.measuresInStore)
	}
	if p.dangling > 0 {
		human += fmt.Sprintf("\n  warning: %d supersession links point outside this dump", p.dangling)
	}
	if extra := p.reportLines(nil); len(extra) > 0 {
		human += "\n" + strings.Join(extra, "\n")
	}
	return store.Out(w, p.a.JSON, human, map[string]interface{}{
		"added": len(p.added), "skipped": p.present, "relinked": res.relinked,
		"dangling": p.dangling, "events": res.events, "foreign": p.foreign,
		"measures": res.measures, "measures_skipped": len(p.measures) - res.measures,
		"retracted": len(p.retract), "pins_dropped": p.pinsDropped,
		"not_imported": p.notImported})
}
