package project

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chka/os-brain/internal/diag"
	"github.com/chka/os-brain/internal/dump"
	"github.com/chka/os-brain/internal/fsguard"
	"github.com/chka/os-brain/internal/pyfmt"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/textguard"
	"github.com/chka/os-brain/internal/timefmt"
)

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
		if err := textguard.CheckToken(a.Key, "context key"); err != nil {
			return err
		}
		// `context set` has no --allow-secret flag, so the refusal must not offer one.
		if err := textguard.GuardTextNoOverride(a.Value); err != nil {
			return err
		}
		cfg.Context[a.Key] = a.Value
		if err := store.SaveConfig(s.Env.Here, cfg); err != nil {
			return err
		}
		s.Env.Config = cfg
		return store.Out(w, a.JSON, fmt.Sprintf("context %s = %s", a.Key, a.Value),
			map[string]interface{}{"context": cfg.Context})
	case "rm":
		if _, ok := cfg.Context[a.Key]; !ok {
			return fmt.Errorf("no context key %s", pyfmt.Repr(a.Key))
		}
		delete(cfg.Context, a.Key)
		if err := store.SaveConfig(s.Env.Here, cfg); err != nil {
			return err
		}
		s.Env.Config = cfg
		return store.Out(w, a.JSON, fmt.Sprintf("removed context key %s", pyfmt.Repr(a.Key)),
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
	return store.Out(w, a.JSON, human, map[string]interface{}{"context": cfg.Context})
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
		if t := strings.TrimSpace(a.Name); t == "*" || t == "**" {
			return fmt.Errorf("%s matches every %s and would disable the check", pyfmt.Repr(a.Name), label)
		}
		// A pattern is checked as the name it would match: '*' itself is not a token
		// character, but a glob is a legitimate declaration.
		if err := textguard.CheckToken(strings.ReplaceAll(a.Name, "*", "x"), label); err != nil {
			return err
		}
		reg[a.Name] = a.Desc
	case "rm":
		if _, ok := reg[a.Name]; !ok {
			return fmt.Errorf("%s %s is not declared", label, pyfmt.Repr(a.Name))
		}
		delete(reg, a.Name)
	}
	if a.Sub == "add" || a.Sub == "rm" {
		if a.Kinds {
			cfg.Kinds = reg
		} else {
			cfg.Scopes = reg
		}
		if err := store.SaveConfig(s.Env.Here, cfg); err != nil {
			return err
		}
		s.Env.Config = cfg
		verb := "declared"
		if a.Sub == "rm" {
			verb = "undeclared"
		}
		return store.Out(w, a.JSON, fmt.Sprintf("%s %s %s", verb, label, pyfmt.Repr(a.Name)),
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
	return store.Out(w, a.JSON, human, map[string]interface{}{key: reg})
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
		return fmt.Errorf("%s is already this project's label", pyfmt.Repr(a.Old))
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
			"they are not: leave a note instead.", pyfmt.Repr(a.Old), n)
	}

	if a.Expect > 0 && a.Expect != n {
		return fmt.Errorf("--expect %d but %s holds %d memories. Check you named the right "+
			"label", a.Expect, pyfmt.Repr(a.Old), n)
	}
	if !a.Apply {
		return store.Out(w, a.JSON, fmt.Sprintf("would move %d memories (and their events, "+
			"tombstones and history) from %s to %s\nre-run with --apply --expect %d once "+
			"you are sure this is the same project under another name",
			n, pyfmt.Repr(a.Old), pyfmt.Repr(s.Env.Project), n),
			map[string]interface{}{"from": a.Old, "to": s.Env.Project, "memories": n,
				"applied": false})
	}

	moved, err := s.relabelRows(a.Old)
	if err != nil {
		return err
	}
	var parts []string
	for _, k := range store.SortedStrings(moved) {
		if moved[k] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", k, moved[k]))
		}
	}
	return store.Out(w, a.JSON, fmt.Sprintf("claimed %d memories as %s (was %s); hashes recomputed\n%s",
		moved["memories"], pyfmt.Repr(s.Env.Project), pyfmt.Repr(a.Old), strings.Join(parts, ", ")),
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
		labels = append(labels, fmt.Sprintf("%s (%d)", pyfmt.Repr(p), c))
	}
	rows.Close()
	return fmt.Errorf("no memories labelled %s in this store. %s",
		pyfmt.Repr(old), timefmt.Plural(len(labels), "other label")+" present; "+
			"os-brain global to see the projects that published themselves")
}

// refuseLiveProjectLabel refuses when the label belongs to another live project on this
// machine: relabel exists to recover a mislabelled copy of your OWN data, and pointed at a
// live project's label it would move their memories into this project's namespace. It
// reports whether the shared index knows the label at all.
func (s *Store) refuseLiveProjectLabel(old string, n int) (bool, error) {
	g := s.GlobalConnect(false)
	if g == nil {
		return false, nil
	}
	// The binding first, then every index row: taking whichever index row came first was
	// wrong, because a name can have several.
	mine := store.HashRoot(s.Env.ProjectRoot())
	var rootID string
	err := g.QueryRow("SELECT root_id FROM project_ids WHERE name=?", old).Scan(&rootID)
	if err == sql.ErrNoRows {
		err = g.QueryRow("SELECT root_id FROM projects WHERE name=? ORDER BY root_id = ? "+
			"LIMIT 1", old, mine).Scan(&rootID)
	}
	s.CloseGlobal(g)
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
			"To tell them something, leave a note.", pyfmt.Repr(old), n, pyfmt.Repr(s.Env.Project))
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
	if err := s.Rehash(tx, s.Env.Project); err != nil {
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
	clean    []*dump.ImportedMemory // memories[i] as remember would store it
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
	if fsguard.Realpath(s.Env.DBPath) == fsguard.Realpath(src) {
		return fmt.Errorf("that is this database")
	}
	into, err := s.absorbTarget(src, a)
	if err != nil {
		return err
	}

	old, err := sql.Open("sqlite", store.SQLiteURI(src, "mode=ro"))
	if err != nil {
		return fmt.Errorf("cannot open %s: %w", src, err)
	}
	defer old.Close()
	now := timefmt.Now()
	source, err := s.readAbsorbSource(old, src, into, a, now)
	if err != nil {
		return err
	}

	if a.DryRun {
		return store.Out(w, a.JSON, fmt.Sprintf("would absorb %d memories and %d events from\n  %s\n"+
			"into project %s", len(source.memories), len(source.events), src, pyfmt.Repr(into)),
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
		visible = fmt.Sprintf("a project whose brain.json says %s", pyfmt.Repr(into))
		tail = fmt.Sprintf("\nthis project reads as %s, so it will NOT see them", pyfmt.Repr(s.Env.Project))
	}
	return store.Out(w, a.JSON, fmt.Sprintf("absorbed into %s: %d memories (%d already here), "+
		"%d supersession links, %d events\nsource untouched: %s\nthese rows are visible to %s%s", pyfmt.Repr(into), added, skipped, relinked, nev, src, visible, tail),
		map[string]interface{}{"project": into, "added": added, "skipped": skipped,
			"relinked": relinked, "events": nev, "source": src})
}

// absorbTarget decides which project the absorbed rows are filed under: --project, else what
// the source's brain.json calls itself, else this project.
func (s *Store) absorbTarget(src string, a AbsorbArgs) (string, error) {
	// The label is a foreign key in all but name: rows tagged with anything other than
	// what the owning project calls itself are a perfect copy nobody can read.
	srcCfg := filepath.Join(filepath.Dir(src), "brain.json")
	srcConf, err := store.LoadConfig(srcCfg)
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
			"mismatch is deliberate", pyfmt.Repr(a.Project), pyfmt.Repr(srcProject), srcCfg, pyfmt.Repr(a.Project), pyfmt.Repr(srcProject))
	}

	// Rows filed under another project's name read there as that project's own work, and
	// the name came from a brain.json beside a file somebody handed over: a crafted one could
	// put pinned "policy" rows at the top of a private project's brief. Another project
	// takes --project naming it and --i-mean-it; a private one is never written.
	if into != s.Env.Project {
		if dump.New(s.Store).ImportPrivate(into) {
			return "", fmt.Errorf("refusing to absorb into %s: it is a private project (or the "+
				"index cannot say it is not). It absorbs its own rows, from its own directory",
				pyfmt.Repr(into))
		}
		if a.Project != into || !a.IMeanIt {
			return "", fmt.Errorf("this would file the rows under %s, and this project is %s.\n"+
				"Rows under another project's name read there as its own work. If that is "+
				"really where they belong, say so twice: --project %s --i-mean-it. To keep "+
				"them here: --project %s --force-label", pyfmt.Repr(into), pyfmt.Repr(s.Env.Project),
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
	if ver > store.SchemaVersion {
		return nil, fmt.Errorf("%s is schema v%d; this binary understands v%d", src, ver, store.SchemaVersion)
	}
	srcRows, err := scanRowMaps(old, true, "SELECT * FROM memories")
	if err != nil {
		// sqlite only finds out a file is not a database on the first read, so anything
		// that is not a brain (a JSON dump, a text file) surfaces here rather than at
		// open. Name the file: the driver's message alone does not.
		return nil, fmt.Errorf("cannot read %s: %w", src, err)
	}
	if labels := distinctLabels(srcRows); len(labels) > 0 && !store.Contains(labels, into) {
		diag.Warn("the source labels its own rows %v; absorbing them as %s", labels, pyfmt.Repr(into))
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
		if p := dump.JSONStr(m["project"], ""); p != "" && !seen[p] {
			seen[p] = true
			labels = append(labels, p)
		}
	}
	return labels
}

func (s *Store) cleanAbsorbedMemories(src, into string, rows []map[string]interface{},
	allowSecret bool, now int64) ([]*dump.ImportedMemory, error) {
	cols, err := store.TableColumns(s.DB, "memories")
	if err != nil {
		return nil, err
	}
	clean := make([]*dump.ImportedMemory, len(rows))
	for i, m := range rows {
		for _, c := range store.SortedIfaceKeys(m) {
			if !cols[c] {
				return nil, fmt.Errorf("%s: memories carries a column %s this brain does not have. "+
					"Refusing the whole absorb", src, pyfmt.Repr(c))
			}
		}
		if err := dump.GuardRow(allowSecret, "memories", i, m); err != nil {
			return nil, err
		}
		if clean[i], err = dump.SanitiseMemory(m, into, "session", now); err != nil {
			return nil, fmt.Errorf("%s, memory %d: %v", src, i+1, err)
		}
	}
	return clean, nil
}

func checkAbsorbedEvents(src string, events []map[string]interface{}, allowSecret bool) error {
	for i, e := range events {
		if err := dump.GuardRow(allowSecret, "events", i, e); err != nil {
			return err
		}
		if err := textguard.CheckLines("the summary", dump.JSONStr(e["summary"], ""), "the kind",
			dump.JSONStr(e["kind"], "")); err != nil {
			return fmt.Errorf("%s, event %d: %v", src, i+1, err)
		}
		if err := textguard.CheckToken(dump.JSONStr(e["scope"], "global"), "scope"); err != nil {
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
		h := im.Hash() // im was sanitised with into as its owner
		var one int
		if err := tx.QueryRow("SELECT 1 FROM memories WHERE hash=?", h).Scan(&one); err == nil {
			skipped++
			continue
		} else if err != sql.ErrNoRows {
			return 0, 0, 0, 0, err
		}
		newID, err := dump.New(s.Store).InsertImportedMemory(tx, im, h)
		if err != nil {
			return 0, 0, 0, 0, err
		}
		idMap[dump.JSONInt(m["id"])] = newID
		added++
	}
	for _, m := range src.memories {
		if m["superseded_by"] == nil || dump.JSONInt(m["superseded_by"]) == 0 {
			continue
		}
		self, ok1 := idMap[dump.JSONInt(m["id"])]
		next, ok2 := idMap[dump.JSONInt(m["superseded_by"])]
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
			"VALUES(?,?,?,?,?,?,?,?)", into, dump.ClampTS(dump.JSONIntOr(e["ts"], now), now),
			dump.JSONStr(e["scope"], "global"), dump.JSONStr(e["kind"], "note"),
			dump.JSONStr(e["actor"], "import"), dump.JSONStr(e["summary"], ""),
			dump.JSONStr(e["ref"], ""), dump.JSONStr(e["session"], "")); err != nil {
			return 0, 0, 0, 0, err
		}
		events++
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, 0, 0, err
	}
	return added, skipped, relinked, events, nil
}
