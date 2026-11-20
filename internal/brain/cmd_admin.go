package brain

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// --- promote ---------------------------------------------------------------------

type PromoteArgs struct {
	ID   int64
	To   string
	JSON bool
}

// Promote refiles a memory into a wider scope (global by default).
func (s *Store) Promote(w io.Writer, a PromoteArgs) error {
	m, err := s.Own(a.ID, "refile")
	if err != nil {
		return err
	}
	dest := a.To
	if dest == "" {
		dest = "global"
	}
	if dest == m.Scope {
		return fmt.Errorf("#%d is already in %s", a.ID, dest)
	}
	// Same rule as remember and update: a destination scope that remember would refuse
	// must not be reachable by refiling into it.
	if err := CheckToken(dest, "scope"); err != nil {
		return err
	}
	s.checkDeclared(dest, "scope")
	// The scope is part of a memory's identity, so the hash moves with it, and a memory
	// already holding that identity in the destination is a merge, not a refile.
	h := ContentHash(s.Env.Project, dest, m.Title)
	var clash int64
	err = s.DB.QueryRow("SELECT id FROM memories WHERE hash=? AND id<>?", h, a.ID).Scan(&clash)
	if err == nil {
		return fmt.Errorf("#%d already holds that title in %s; merge them instead", clash, Repr(dest))
	} else if err != sql.ErrNoRows {
		return err
	}
	if _, err := s.DB.Exec("UPDATE memories SET scope=?, hash=?, updated_at=? WHERE id=?",
		dest, h, Now(), a.ID); err != nil {
		return err
	}
	return Out(w, a.JSON, fmt.Sprintf("promoted #%d from %s to %s", a.ID, m.Scope, dest),
		map[string]interface{}{"id": a.ID, "from": m.Scope, "to": dest})
}

// --- context ---------------------------------------------------------------------

type ContextArgs struct {
	Sub   string // set | rm | list
	Key   string
	Value string
	JSON  bool
}

// Context holds project facts every session should be told without having to ask: prod
// host, deploy command, dashboard URL. They ride along in the brief.
func (s *Store) Context(w io.Writer, a ContextArgs) error {
	cfg := s.Env.Config
	if cfg.Context == nil {
		cfg.Context = map[string]string{}
	}
	switch a.Sub {
	case "set":
		if err := CheckToken(a.Key, "context key"); err != nil {
			return err
		}
		// `context set` has no --allow-secret flag, so the refusal must not offer one.
		if err := GuardTextNoOverride(a.Value); err != nil {
			return err
		}
		cfg.Context[a.Key] = a.Value
		if err := SaveConfig(s.Env.Here, cfg); err != nil {
			return err
		}
		s.Env.Config = cfg
		return Out(w, a.JSON, fmt.Sprintf("context %s = %s", a.Key, a.Value),
			map[string]interface{}{"context": cfg.Context})
	case "rm":
		if _, ok := cfg.Context[a.Key]; !ok {
			return fmt.Errorf("no context key %s", Repr(a.Key))
		}
		delete(cfg.Context, a.Key)
		if err := SaveConfig(s.Env.Here, cfg); err != nil {
			return err
		}
		s.Env.Config = cfg
		return Out(w, a.JSON, fmt.Sprintf("removed context key %s", Repr(a.Key)),
			map[string]interface{}{"context": cfg.Context})
	}
	var lines []string
	for _, k := range sortedStringKeys(cfg.Context) {
		lines = append(lines, fmt.Sprintf("  %-12s %s", k, cfg.Context[k]))
	}
	human := "(no project context set; os-brain context set <key> <value>)"
	if len(lines) > 0 {
		human = strings.Join(lines, "\n")
	}
	return Out(w, a.JSON, human, map[string]interface{}{"context": cfg.Context})
}

func sortedStringKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- scope / kind registry -------------------------------------------------------

type RegistryArgs struct {
	Sub   string // add | rm | list
	Name  string
	Desc  string
	Kinds bool // false = scopes
	JSON  bool
}

// Registry declares, undeclares and lists the scopes (or kinds) this project uses, so a
// typo in a scope is warned about instead of becoming a new scope nobody recalls from.
func (s *Store) Registry(w io.Writer, a RegistryArgs) error {
	cfg := s.Env.Config
	label, key := "scope", "scopes"
	reg := cfg.Scopes
	if a.Kinds {
		label, key = "kind", "kinds"
		reg = cfg.Kinds
	}
	if reg == nil {
		reg = map[string]string{}
	}
	switch a.Sub {
	case "add":
		if t := trim(a.Name); t == "*" || t == "**" {
			return fmt.Errorf("%s matches every %s and would disable the check", Repr(a.Name), label)
		}
		// A pattern is checked as the name it would match: '*' itself is not a token
		// character, but a glob is a legitimate declaration.
		if err := CheckToken(strings.ReplaceAll(a.Name, "*", "x"), label); err != nil {
			return err
		}
		reg[a.Name] = a.Desc
	case "rm":
		if _, ok := reg[a.Name]; !ok {
			return fmt.Errorf("%s %s is not declared", label, Repr(a.Name))
		}
		delete(reg, a.Name)
	}
	if a.Sub == "add" || a.Sub == "rm" {
		if a.Kinds {
			cfg.Kinds = reg
		} else {
			cfg.Scopes = reg
		}
		if err := SaveConfig(s.Env.Here, cfg); err != nil {
			return err
		}
		s.Env.Config = cfg
		verb := "declared"
		if a.Sub == "rm" {
			verb = "undeclared"
		}
		return Out(w, a.JSON, fmt.Sprintf("%s %s %s", verb, label, Repr(a.Name)),
			map[string]interface{}{key: reg})
	}
	var lines []string
	for _, k := range sortedStringKeys(reg) {
		lines = append(lines, fmt.Sprintf("  %-24s %s", k, reg[k]))
	}
	human := fmt.Sprintf("(no %s declared in brain.json)", key)
	if len(lines) > 0 {
		human = strings.Join(lines, "\n")
	}
	return Out(w, a.JSON, human, map[string]interface{}{key: reg})
}

// --- relabel ---------------------------------------------------------------------

type RelabelArgs struct {
	IMeanIt bool
	Old     string
	Expect  int
	Apply   bool
	JSON    bool
}

// relabelTables are the tables relabel moves to this project's name.
var relabelTables = []string{"memories", "events", "leads", "tombstones", "mem_history"}

// Relabel claims rows that belong to this project but carry another label. It only ever
// moves them TO this project's own name: relabelling someone else's rows is not on offer.
func (s *Store) Relabel(w io.Writer, a RelabelArgs) error {
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM memories WHERE project=?", a.Old).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return s.noSuchLabel(a.Old)
	}
	if a.Old == s.Env.Project {
		return fmt.Errorf("%s is already this project's label", Repr(a.Old))
	}
	registered, err := s.refuseLiveProjectLabel(a.Old, n)
	if err != nil {
		return err
	}
	// The check above only fires for a project the shared index knows, which is opt-in
	// (`describe`). Not registered is exactly the case where the tool knows LEAST about
	// whose rows these are: without this, one command with --apply moved somebody else's
	// whole store into this one. Only on --apply: a dry run changes nothing, and the plan is
	// exactly what somebody needs to read before deciding whether these rows are theirs.
	if !registered && a.Apply && !a.IMeanIt {
		return fmt.Errorf("%s is not registered in the shared index, so nothing here can "+
			"tell whether those %d memories are a mislabelled copy of yours or a live "+
			"project that never ran `os-brain describe`.\n"+
			"If they are yours, pass --i-mean-it. If you are not certain they are yours, "+
			"they are not: leave a note instead.", Repr(a.Old), n)
	}

	if a.Expect > 0 && a.Expect != n {
		return fmt.Errorf("--expect %d but %s holds %d memories. Check you named the right "+
			"label", a.Expect, Repr(a.Old), n)
	}
	if !a.Apply {
		return Out(w, a.JSON, fmt.Sprintf("would move %d memories (and their events, "+
			"tombstones and history) from %s to %s\nre-run with --apply --expect %d once "+
			"you are sure this is the same project under another name",
			n, Repr(a.Old), Repr(s.Env.Project), n),
			map[string]interface{}{"from": a.Old, "to": s.Env.Project, "memories": n,
				"applied": false})
	}

	moved, err := s.relabelRows(a.Old)
	if err != nil {
		return err
	}
	var parts []string
	for _, k := range sortedStrings(moved) {
		if moved[k] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", k, moved[k]))
		}
	}
	return Out(w, a.JSON, fmt.Sprintf("claimed %d memories as %s (was %s); hashes recomputed\n%s",
		moved["memories"], Repr(s.Env.Project), Repr(a.Old), strings.Join(parts, ", ")),
		map[string]interface{}{"from": a.Old, "to": s.Env.Project, "moved": moved,
			"applied": true})
}

// noSuchLabel refuses a label with no memories. It says how many other labels there are,
// not which: listing every label present would answer "which projects share this file" for
// anyone who types a name that is not one, an existence oracle derived straight from the
// memories table rather than from the index a project opted into.
func (s *Store) noSuchLabel(old string) error {
	rows, err := s.DB.Query("SELECT project, COUNT(*) c FROM memories GROUP BY project " +
		"ORDER BY c DESC")
	if err != nil {
		return err
	}
	var labels []string
	for rows.Next() {
		var p string
		var c int
		if err := rows.Scan(&p, &c); err != nil {
			rows.Close()
			return err
		}
		labels = append(labels, fmt.Sprintf("%s (%d)", Repr(p), c))
	}
	rows.Close()
	return fmt.Errorf("no memories labelled %s in this store. %s",
		Repr(old), plural(len(labels), "other label")+" present; "+
			"os-brain global to see the projects that published themselves")
}

// refuseLiveProjectLabel refuses when the label belongs to another live project on this
// machine: relabel exists to recover a mislabelled copy of your OWN data, and pointed at a
// live project's label it would move their memories into this project's namespace. It
// reports whether the shared index knows the label at all.
func (s *Store) refuseLiveProjectLabel(old string, n int) (bool, error) {
	g := s.globalConnect(false)
	if g == nil {
		return false, nil
	}
	// The binding first, then every index row: taking whichever index row came first was
	// wrong, because a name can have several.
	mine := hashRoot(s.Env.ProjectRoot())
	var rootID string
	err := g.QueryRow("SELECT root_id FROM project_ids WHERE name=?", old).Scan(&rootID)
	if err == sql.ErrNoRows {
		err = g.QueryRow("SELECT root_id FROM projects WHERE name=? ORDER BY root_id = ? "+
			"LIMIT 1", old, mine).Scan(&rootID)
	}
	s.closeGlobal(g)
	if err != nil {
		return false, nil
	}
	// Identity, not location: the refusal never prints the other project's directory,
	// which would tell the caller where somebody else's work lives.
	if rootID != mine {
		return true, fmt.Errorf("%s is a live project registered elsewhere on this machine. "+
			"Those %d memories\nbelong to it, not to %s. Relabelling them would move "+
			"another project's memory into this one.\nIf they really are yours, that "+
			"project's brain.json is wrong: say so and let its own session fix it. "+
			"To tell them something, leave a note.", Repr(old), n, Repr(s.Env.Project))
	}
	return true, nil
}

// relabelRows moves every row labelled old to this project in one transaction and returns
// the count per table.
func (s *Store) relabelRows(old string) (map[string]int, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	moved := map[string]int{}
	for _, table := range relabelTables {
		res, err := tx.Exec("UPDATE "+table+" SET project=? WHERE project=?",
			s.Env.Project, old)
		if err != nil {
			return nil, err
		}
		c, _ := res.RowsAffected()
		moved[table] = int(c)
	}
	// A memory's identity includes its project, so this project's hashes have to be
	// recomputed; nobody else's.
	if err := s.rehash(tx, s.Env.Project); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return moved, nil
}

// --- absorb ----------------------------------------------------------------------

type AbsorbArgs struct {
	File       string
	Project    string
	ForceLabel bool
	DryRun     bool
	NoEvents   bool
	// IMeanIt is the second half of filing rows under another project's name; --project
	// naming it is the first.
	IMeanIt bool
	// AllowSecret lets through what the scan flags, as everywhere else.
	AllowSecret bool
	JSON        bool
}

// absorbSource is a standalone brain.db read and checked, ready to insert.
type absorbSource struct {
	path     string
	memories []map[string]interface{}
	clean    []*importedMemory // memories[i] as remember would store it
	events   []map[string]interface{}
}

// Absorb pulls a standalone brain.db into this store as a project. The source is opened
// read only and never modified: it stays a backup until you delete it yourself.
func (s *Store) Absorb(w io.Writer, a AbsorbArgs) error {
	src, err := filepath.Abs(a.File)
	if err != nil {
		return err
	}
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("no such database: %s", src)
	}
	if realpath(s.Env.DBPath) == realpath(src) {
		return fmt.Errorf("that is this database")
	}
	into, err := s.absorbTarget(src, a)
	if err != nil {
		return err
	}

	old, err := sql.Open("sqlite", SQLiteURI(src, "mode=ro"))
	if err != nil {
		return fmt.Errorf("cannot open %s: %w", src, err)
	}
	defer old.Close()
	now := Now()
	source, err := s.readAbsorbSource(old, src, into, a, now)
	if err != nil {
		return err
	}

	if a.DryRun {
		return Out(w, a.JSON, fmt.Sprintf("would absorb %d memories and %d events from\n  %s\n"+
			"into project %s", len(source.memories), len(source.events), src, Repr(into)),
			map[string]interface{}{"memories": len(source.memories), "events": len(source.events),
				"project": into, "applied": false})
	}

	added, skipped, relinked, nev, err := s.insertAbsorbed(source, into, now)
	if err != nil {
		return err
	}
	visible := "this project"
	tail := ""
	if into != s.Env.Project {
		visible = fmt.Sprintf("a project whose brain.json says %s", Repr(into))
		tail = fmt.Sprintf("\nthis project reads as %s, so it will NOT see them", Repr(s.Env.Project))
	}
	return Out(w, a.JSON, fmt.Sprintf("absorbed into %s: %d memories (%d already here), "+
		"%d supersession links, %d events\nsource untouched: %s\nthese rows are visible to %s%s", Repr(into), added, skipped, relinked, nev, src, visible, tail),
		map[string]interface{}{"project": into, "added": added, "skipped": skipped,
			"relinked": relinked, "events": nev, "source": src})
}

// absorbTarget decides which project the absorbed rows are filed under: --project, else what
// the source's brain.json calls itself, else this project.
func (s *Store) absorbTarget(src string, a AbsorbArgs) (string, error) {
	// The label is a foreign key in all but name: rows tagged with anything other than
	// what the owning project calls itself are a perfect copy nobody can read.
	srcCfg := filepath.Join(filepath.Dir(src), "brain.json")
	srcConf, err := LoadConfig(srcCfg)
	if err != nil {
		return "", err
	}
	srcProject := srcConf.Project
	into := a.Project
	if into == "" {
		into = srcProject
	}
	if into == "" {
		into = s.Env.Project
	}
	if a.Project != "" && srcProject != "" && a.Project != srcProject && !a.ForceLabel {
		return "", fmt.Errorf("--project %s is not what that brain calls itself (%s, from %s).\n"+
			"Rows labelled %s would be invisible to it: recall and brief filter on the name "+
			"in brain.json.\nDrop --project to file it as %s, or pass --force-label if the "+
			"mismatch is deliberate", Repr(a.Project), Repr(srcProject), srcCfg, Repr(a.Project), Repr(srcProject))
	}

	// Rows filed under another project's name read there as that project's own work, and
	// the name came from a brain.json beside a file somebody handed over: a crafted one could
	// put pinned "policy" rows at the top of a private project's brief. Another project
	// takes --project naming it and --i-mean-it; a private one is never written.
	if into != s.Env.Project {
		if s.importPrivate(into) {
			return "", fmt.Errorf("refusing to absorb into %s: it is a private project (or the "+
				"index cannot say it is not). It absorbs its own rows, from its own directory",
				Repr(into))
		}
		if a.Project != into || !a.IMeanIt {
			return "", fmt.Errorf("this would file the rows under %s, and this project is %s.\n"+
				"Rows under another project's name read there as its own work. If that is "+
				"really where they belong, say so twice: --project %s --i-mean-it. To keep "+
				"them here: --project %s --force-label", Repr(into), Repr(s.Env.Project),
				into, s.Env.Project)
		}
	}
	return into, nil
}

// readAbsorbSource reads the source's memories and events and puts them through the
// remember path's rules, the same ones import applies: known columns only, a type and scope
// remember would accept, no pins, no unearned 'confirmed', trust 'imported', nothing dated
// in the future, and the secret scan over every text column. One bad row refuses the whole
// absorb.
func (s *Store) readAbsorbSource(old *sql.DB, src, into string, a AbsorbArgs, now int64) (*absorbSource, error) {
	ver := 1
	if err := old.QueryRow("SELECT v FROM meta WHERE k='schema_version'").Scan(&ver); err != nil {
		ver = 1
	}
	if ver > SchemaVersion {
		return nil, fmt.Errorf("%s is schema v%d; this binary understands v%d", src, ver, SchemaVersion)
	}
	srcRows, err := scanRowMaps(old, true, "SELECT * FROM memories")
	if err != nil {
		// sqlite only finds out a file is not a database on the first read, so anything
		// that is not a brain (a JSON dump, a text file) surfaces here rather than at
		// open. Name the file: the driver's message alone does not.
		return nil, fmt.Errorf("cannot read %s: %w", src, err)
	}
	if labels := distinctLabels(srcRows); len(labels) > 0 && !contains(labels, into) {
		Warn("the source labels its own rows %v; absorbing them as %s", labels, Repr(into))
	}
	source := &absorbSource{path: src, memories: srcRows}
	if !a.NoEvents {
		source.events, _ = scanRowMaps(old, true, "SELECT * FROM events")
	}
	if source.clean, err = s.cleanAbsorbedMemories(src, into, srcRows, a.AllowSecret, now); err != nil {
		return nil, err
	}
	if err := checkAbsorbedEvents(src, source.events, a.AllowSecret); err != nil {
		return nil, err
	}
	return source, nil
}

// distinctLabels is the project labels on the rows, in first-seen order.
func distinctLabels(rows []map[string]interface{}) []string {
	var labels []string
	seen := map[string]bool{}
	for _, m := range rows {
		if p := jsonStr(m["project"], ""); p != "" && !seen[p] {
			seen[p] = true
			labels = append(labels, p)
		}
	}
	return labels
}

func (s *Store) cleanAbsorbedMemories(src, into string, rows []map[string]interface{},
	allowSecret bool, now int64) ([]*importedMemory, error) {
	cols, err := tableColumns(s.DB, "memories")
	if err != nil {
		return nil, err
	}
	clean := make([]*importedMemory, len(rows))
	for i, m := range rows {
		for _, c := range sortedIfaceKeys(m) {
			if !cols[c] {
				return nil, fmt.Errorf("%s: memories carries a column %s this brain does not have. "+
					"Refusing the whole absorb", src, Repr(c))
			}
		}
		if err := guardRow(allowSecret, "memories", i, m); err != nil {
			return nil, err
		}
		if clean[i], err = sanitiseMemory(m, into, "session", now); err != nil {
			return nil, fmt.Errorf("%s, memory %d: %v", src, i+1, err)
		}
	}
	return clean, nil
}

func checkAbsorbedEvents(src string, events []map[string]interface{}, allowSecret bool) error {
	for i, e := range events {
		if err := guardRow(allowSecret, "events", i, e); err != nil {
			return err
		}
		if err := CheckLines("the summary", jsonStr(e["summary"], ""), "the kind",
			jsonStr(e["kind"], "")); err != nil {
			return fmt.Errorf("%s, event %d: %v", src, i+1, err)
		}
		if err := CheckToken(jsonStr(e["scope"], "global"), "scope"); err != nil {
			return fmt.Errorf("%s, event %d: %v", src, i+1, err)
		}
	}
	return nil
}

// insertAbsorbed writes the checked rows in one transaction. A memory whose identity is
// already here is skipped, so absorbing the same file twice adds nothing; supersession
// links are restored only where both ends arrived.
func (s *Store) insertAbsorbed(src *absorbSource, into string, now int64) (added, skipped, relinked, events int, err error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, 0, 0, 0, err
	}
	defer tx.Rollback()

	idMap := map[int64]int64{}
	for i, m := range src.memories {
		im := src.clean[i]
		h := ContentHash(into, im.scope, im.title)
		var one int
		if err := tx.QueryRow("SELECT 1 FROM memories WHERE hash=?", h).Scan(&one); err == nil {
			skipped++
			continue
		} else if err != sql.ErrNoRows {
			return 0, 0, 0, 0, err
		}
		res, err := tx.Exec("INSERT INTO memories(project,scope,mem_type,title,body,source,"+
			"source_date,source_commit,confidence,importance,pinned,use_count,last_used,"+
			"verified_at,session,trust,hash,created_at,updated_at) "+
			"VALUES(?,?,?,?,?,?,?,?,?,?,0,?,?,?,?,'imported',?,?,?)",
			into, im.scope, im.typ, im.title, im.body, im.source, im.sourceDate,
			im.sourceCommit, im.confidence, im.importance, im.useCount, im.lastUsed,
			im.verifiedAt, im.session, h, im.createdAt, im.updatedAt)
		if err != nil {
			return 0, 0, 0, 0, err
		}
		newID, err := res.LastInsertId()
		if err != nil {
			return 0, 0, 0, 0, err
		}
		idMap[jsonInt(m["id"])] = newID
		if err := s.ftsPut(tx, newID, im.title, im.body); err != nil {
			return 0, 0, 0, 0, err
		}
		added++
	}
	for _, m := range src.memories {
		if m["superseded_by"] == nil || jsonInt(m["superseded_by"]) == 0 {
			continue
		}
		self, ok1 := idMap[jsonInt(m["id"])]
		next, ok2 := idMap[jsonInt(m["superseded_by"])]
		if ok1 && ok2 {
			if _, err := tx.Exec("UPDATE memories SET superseded_by=? WHERE id=?",
				next, self); err != nil {
				return 0, 0, 0, 0, err
			}
			relinked++
		}
	}
	for _, e := range src.events {
		if _, err := tx.Exec("INSERT INTO events(project,ts,scope,kind,actor,summary,ref,session) "+
			"VALUES(?,?,?,?,?,?,?,?)", into, clampTS(jsonIntOr(e["ts"], now), now),
			jsonStr(e["scope"], "global"), jsonStr(e["kind"], "note"),
			jsonStr(e["actor"], "import"), jsonStr(e["summary"], ""),
			jsonStr(e["ref"], ""), jsonStr(e["session"], "")); err != nil {
			return 0, 0, 0, 0, err
		}
		events++
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, 0, 0, err
	}
	return added, skipped, relinked, events, nil
}

// --- review ----------------------------------------------------------------------

type ReviewArgs struct {
	N           int
	StaleDays   int
	Duplicates  bool
	MaxPairwise int
	JSON        bool
}

// review collects one Review: the human lines and the JSON object, section by section.
type review struct {
	s     *Store
	a     ReviewArgs
	now   int64
	rows  []*Memory
	lines []string
	found map[string]interface{}
}

// Review is a hygiene pass. It surfaces candidates; the agent decides what to merge,
// promote, supersede or forget. It never changes anything itself.
func (s *Store) Review(w io.Writer, a ReviewArgs) error {
	if a.N <= 0 {
		a.N = 8
	}
	// Only a NEGATIVE stale-days is unset: 0 is a legitimate request for every
	// unverified tier A/B memory, and folding it into 180 answered a different question
	// than the one asked while looking like a clean brain.
	if a.StaleDays < 0 {
		a.StaleDays = 180
	}
	if a.MaxPairwise <= 0 {
		a.MaxPairwise = 600
	}
	rows, err := s.queryMemories("SELECT "+memoryCols+", 0.0 AS bm FROM memories m "+
		"WHERE m.project=? AND m.superseded_by IS NULL", s.Env.Project)
	if err != nil {
		return err
	}
	bar := strings.Repeat("=", 72)
	r := &review{s: s, a: a, now: Now(), rows: rows,
		lines: []string{bar, strings.ToUpper(s.Env.Project) + " BRAIN — REVIEW", bar},
		found: map[string]interface{}{}}

	r.duplicates()
	r.stale()
	r.cold()
	r.promotions()
	r.crossScope()
	r.unverified()
	r.assumed()
	r.reviewDue()
	r.pins()
	if err := r.superseded(); err != nil {
		return err
	}
	r.undeclaredScopes()
	// Excess above, absence below. `review` used to answer only "what is in here that
	// should not be", and answered "nothing to clean up" to a brain that had never used
	// half the product - which is how a nine-memory brain collected five omissions and
	// one warning.
	oms := s.GatherOmissions()
	for _, o := range oms {
		r.lines = append(r.lines, "\n"+o.Head)
		r.lines = append(r.lines, o.Lines...)
	}
	r.found["omissions"] = oms

	if len(r.lines) == 3 {
		r.lines = append(r.lines, "\nnothing to clean up, and nothing unused.")
	}
	r.lines = append(r.lines, "\n"+bar)
	return Out(w, a.JSON, strings.Join(r.lines, "\n"), r.found)
}

// section prints up to a.N memories under a heading, with an optional hint line, and
// returns every id for the JSON. An empty section prints nothing.
func (r *review) section(title string, ms []*Memory, render func(*Memory) string, hint string) []int64 {
	out := make([]int64, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.ID)
	}
	if len(ms) == 0 {
		return out
	}
	r.lines = append(r.lines, "\n## "+title)
	for i, m := range ms {
		if i == r.a.N {
			break
		}
		r.lines = append(r.lines, render(m))
	}
	if hint != "" {
		r.lines = append(r.lines, hint)
	}
	return out
}

func (r *review) where(keep func(*Memory) bool) []*Memory {
	var out []*Memory
	for _, m := range r.rows {
		if keep(m) {
			out = append(out, m)
		}
	}
	return out
}

type dupePair struct {
	score  float64
	a, b   int64
	at, bt string
}

// duplicates finds near-duplicate titles inside each scope. It compares every pair, so past
// MaxPairwise memories it is skipped unless asked for.
func (r *review) duplicates() {
	forDupes := r.rows
	if len(r.rows) > r.a.MaxPairwise && !r.a.Duplicates {
		r.lines = append(r.lines, fmt.Sprintf("\n## DUPLICATE SCAN SKIPPED — %d memories "+
			"(os-brain review --duplicates to run it anyway)", len(r.rows)))
		forDupes = nil
	}
	byScope := map[string][]*Memory{}
	for _, m := range forDupes {
		byScope[m.Scope] = append(byScope[m.Scope], m)
	}
	var dupes []dupePair
	for _, ms := range byScope {
		dupes = append(dupes, similarTitles(ms)...)
	}
	sort.SliceStable(dupes, func(i, j int) bool { return dupes[i].score > dupes[j].score })
	if len(dupes) > 0 {
		r.lines = append(r.lines, "\n## NEAR-DUPLICATE TITLES — merge into one, supersede the loser")
		for i, d := range dupes {
			if i == r.a.N {
				break
			}
			r.lines = append(r.lines, fmt.Sprintf("  %.2f  #%d %s", d.score, d.a, d.at))
			r.lines = append(r.lines, fmt.Sprintf("        #%d %s", d.b, d.bt))
		}
		r.lines = append(r.lines, "  merge: os-brain remember --scope <s> --type <t> --title "+
			"\"<merged>\" --body - --supersedes <id>  (then forget the other)")
	}
	out := []map[string]interface{}{}
	for _, d := range dupes {
		out = append(out, map[string]interface{}{"score": d.score, "a": d.a, "b": d.b})
	}
	r.found["duplicates"] = out
}

// similarTitles is every pair of memories whose title words overlap by half or more.
func similarTitles(ms []*Memory) []dupePair {
	toks := map[int64]map[string]bool{}
	for _, m := range ms {
		set := map[string]bool{}
		for _, t := range strings.Fields(NormTitle(m.Title)) {
			set[t] = true
		}
		toks[m.ID] = set
	}
	var out []dupePair
	for i, x := range ms {
		for _, y := range ms[i+1:] {
			ta, tb := toks[x.ID], toks[y.ID]
			if len(ta) == 0 || len(tb) == 0 {
				continue
			}
			if j := jaccard(ta, tb); j >= 0.5 {
				out = append(out, dupePair{score: round2(j), a: x.ID, b: y.ID, at: x.Title, bt: y.Title})
			}
		}
	}
	return out
}

func (r *review) stale() {
	stale := r.where(func(m *Memory) bool {
		return TierOf(m.MemType) == "D" && m.UpdatedAt < r.now-14*86400
	})
	r.found["stale"] = r.section("DECAYED EPHEMERA (tier D, >14d) — prune or promote", stale,
		func(m *Memory) string {
			return fmt.Sprintf("  #%d [%s] %s  (%s)", m.ID, OneLine(m.Scope), OneLine(m.Title), Day(m.UpdatedAt))
		}, "  os-brain prune --tier D --older-than 14   (dry run; add --apply)")
}

func (r *review) cold() {
	cold := r.where(func(m *Memory) bool {
		return m.UseCount == 0 && m.CreatedAt < r.now-60*86400 && m.Pinned == 0
	})
	r.found["cold"] = r.section("NEVER RECALLED (>60d) — still true, or noise?", cold,
		func(m *Memory) string {
			return fmt.Sprintf("  #%d [%s/%s] %s", m.ID, OneLine(m.Scope), m.MemType, OneLine(m.Title))
		}, "")
}

func (r *review) promotions() {
	promo := r.where(func(m *Memory) bool { return m.Scope != "global" && m.UseCount >= 5 })
	sort.SliceStable(promo, func(i, j int) bool { return promo[i].UseCount > promo[j].UseCount })
	r.found["promote"] = r.section("PROMOTION CANDIDATES — recalled often, may be project-wide",
		promo, func(m *Memory) string {
			return fmt.Sprintf("  #%d [%s] used %dx  %s", m.ID, OneLine(m.Scope), m.UseCount, OneLine(m.Title))
		}, "  os-brain promote <id>            (moves it to global)")
}

// crossScope finds the same title filed in two scopes, which is how a brain ends up
// disagreeing with itself.
func (r *review) crossScope() {
	byTitle := map[string][]*Memory{}
	var titleOrder []string
	for _, m := range r.rows {
		k := NormTitle(m.Title)
		if _, seen := byTitle[k]; !seen {
			titleOrder = append(titleOrder, k)
		}
		byTitle[k] = append(byTitle[k], m)
	}
	var cross [][]*Memory
	for _, k := range titleOrder {
		ms := byTitle[k]
		scopes := map[string]bool{}
		for _, m := range ms {
			scopes[m.Scope] = true
		}
		if len(scopes) > 1 {
			cross = append(cross, ms)
		}
	}
	if len(cross) > 0 {
		r.lines = append(r.lines, "\n## SAME TITLE IN TWO SCOPES — one of them is stale, pick one")
		for i, ms := range cross {
			if i == r.a.N {
				break
			}
			var parts []string
			for _, m := range ms {
				parts = append(parts, fmt.Sprintf("#%d [%s]", m.ID, m.Scope))
			}
			r.lines = append(r.lines, "  "+strings.Join(parts, " / ")+"  "+OneLine(ms[0].Title))
		}
		r.lines = append(r.lines, "  keep one: os-brain promote <id> --to <widest scope>, then "+
			"forget the other")
	}
	out := [][]int64{}
	for _, ms := range cross {
		var ids []int64
		for _, m := range ms {
			ids = append(ids, m.ID)
		}
		out = append(out, ids)
	}
	r.found["cross_scope"] = out
}

// lastChecked is when a memory was last verified, or created if never.
func lastChecked(m *Memory) int64 {
	if m.VerifiedAt != nil {
		return *m.VerifiedAt
	}
	return m.CreatedAt
}

func (r *review) unverified() {
	unver := r.where(func(m *Memory) bool {
		t := TierOf(m.MemType)
		return (t == "A" || t == "B") && lastChecked(m) < r.now-int64(r.a.StaleDays)*86400
	})
	// Sorted on (verified_at or created_at), not on their sum: a memory verified long ago
	// but created recently sorts differently under the two rules.
	sort.SliceStable(unver, func(i, j int) bool { return lastChecked(unver[i]) < lastChecked(unver[j]) })
	r.found["unverified"] = r.section(fmt.Sprintf("UNVERIFIED >%dd (tier A/B) — still true?",
		r.a.StaleDays), unver, func(m *Memory) string {
		last := "never"
		if m.VerifiedAt != nil {
			last = Day(*m.VerifiedAt)
		}
		return fmt.Sprintf("  #%d [%s/%s] %s  (last verified %s)", m.ID, OneLine(m.Scope), m.MemType,
			OneLine(m.Title), last)
	}, "  re-check, then: os-brain verify <id> [--confidence confirmed]")
}

func (r *review) assumed() {
	soft := r.where(func(m *Memory) bool {
		return m.Confidence == "assumed" && (m.Pinned == 1 || TierOf(m.MemType) == "A" ||
			TierOf(m.MemType) == "B")
	})
	r.found["assumed"] = r.section("ASSUMED BUT LOAD-BEARING — verify or downgrade", soft,
		func(m *Memory) string {
			return fmt.Sprintf("  #%d [%s/%s] %s", m.ID, OneLine(m.Scope), m.MemType, OneLine(m.Title))
		}, "")
}

func (r *review) reviewDue() {
	soon := r.where(func(m *Memory) bool { return m.ReviewOn != nil && *m.ReviewOn <= r.now+7*86400 })
	sort.SliceStable(soon, func(i, j int) bool { return *soon[i].ReviewOn < *soon[j].ReviewOn })
	r.found["review_due"] = r.section("REVIEW DUE OR DUE SOON", soon, func(m *Memory) string {
		return fmt.Sprintf("  #%d %s  %s", m.ID, Day(*m.ReviewOn), OneLine(m.Title))
	}, "")
}

// pins warns when pinning has stopped meaning anything: past about a fifth of the brain,
// or past a dozen memories loaded into every brief.
func (r *review) pins() {
	npin := 0
	for _, m := range r.rows {
		npin += m.Pinned
	}
	total := len(r.rows)
	if total > 0 && float64(npin)/float64(total) > 0.2 && npin > 8 {
		r.lines = append(r.lines, fmt.Sprintf("\n## PIN RATIO — %d of %d memories are pinned (%d%%)",
			npin, total, int(100*float64(npin)/float64(total)+0.5)))
		r.lines = append(r.lines, "  Past roughly a fifth, 'read first' stops meaning anything. "+
			"If things were pinned\n  to make them visible next session, the NEW SINCE block "+
			"already does that.")
	}
	if npin > 12 {
		r.lines = append(r.lines, fmt.Sprintf("\n## PINNED BLOAT — %d pinned memories load into "+
			"every brief", npin))
		r.lines = append(r.lines, "  unpin the ones that are merely important: os-brain update <id> --unpin")
	}
	r.found["pinned"] = npin
}

func (r *review) superseded() error {
	var nsup int
	if err := r.s.DB.QueryRow("SELECT COUNT(*) FROM memories WHERE project=? AND "+
		"superseded_by IS NOT NULL", r.s.Env.Project).Scan(&nsup); err != nil {
		return err
	}
	if nsup > 0 {
		verb := "ies are"
		if nsup == 1 {
			verb = "y is"
		}
		r.lines = append(r.lines, fmt.Sprintf("\n## SUPERSEDED — %d memor%s out of recall but "+
			"still stored", nsup, verb))
		r.lines = append(r.lines, "  os-brain prune --superseded --older-than 90   (dry run; add --apply)")
	}
	r.found["superseded"] = nsup
	return nil
}

// undeclaredScopes lists scopes in use that brain.json does not declare. Only checked once
// the project declares any scopes at all: with none declared there is no list to be off.
func (r *review) undeclaredScopes() {
	var undeclared []string
	declared := r.s.Env.Config.Scopes
	if len(declared) > 0 {
		seen := map[string]bool{}
		for _, m := range r.rows {
			if seen[m.Scope] {
				continue
			}
			seen[m.Scope] = true
			if !scopeDeclared(declared, m.Scope) {
				undeclared = append(undeclared, m.Scope)
			}
		}
		sort.Strings(undeclared)
	}
	if len(undeclared) > 0 {
		r.lines = append(r.lines, "\n## UNDECLARED SCOPES — declare or refile")
		for _, sc := range undeclared {
			r.lines = append(r.lines, fmt.Sprintf("  os-brain scope add %s --desc \"...\"", sc))
		}
	}
	if undeclared == nil {
		undeclared = []string{}
	}
	r.found["undeclared_scopes"] = undeclared
}

// scopeDeclared reports whether scope matches a declared name or glob pattern.
func scopeDeclared(declared map[string]string, scope string) bool {
	for pat := range declared {
		if ok, _ := filepath.Match(pat, scope); ok || pat == scope {
			return true
		}
	}
	return false
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}
