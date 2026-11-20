package brain

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

// `map` is the index of what is in a brain: scopes, memory types, event kinds, measures,
// pinned memories and recent tombstones, reconciled against the scopes and kinds that
// brain.json declares. It reads; it never changes anything.

type ScopeEntry struct {
	Scope  string         `json:"scope"`
	Count  int            `json:"count"`
	Last   int64          `json:"last"`
	Pinned int            `json:"pinned"`
	Tiers  map[string]int `json:"tiers"`
	Top    []TopMemory    `json:"top"`
	scored []scoredTop
}

type TopMemory struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

type scoredTop struct {
	score float64
	m     TopMemory
}

// scopeIndex is the per-scope index: counts by tier, last touched, and the top memories
// by the same score recall uses, so the map agrees with what a recall would surface.
func (s *Store) scopeIndex(top int) (map[string]*ScopeEntry, error) {
	if top <= 0 {
		top = 3
	}
	now := Now()
	rows, err := s.queryMemories("SELECT "+memoryCols+", 0.0 AS bm FROM memories m "+
		"WHERE m.project=? AND m.superseded_by IS NULL", s.Env.Project)
	if err != nil {
		return nil, err
	}
	idx := map[string]*ScopeEntry{}
	for _, r := range rows {
		e, ok := idx[r.Scope]
		if !ok {
			e = &ScopeEntry{Scope: r.Scope, Tiers: map[string]int{
				"A": 0, "B": 0, "C": 0, "D": 0, "N": 0}}
			idx[r.Scope] = e
		}
		e.Count++
		e.Pinned += r.Pinned
		if r.UpdatedAt > e.Last {
			e.Last = r.UpdatedAt
		}
		e.Tiers[TierOf(r.MemType)]++
		// A neutral text match (0.5) leaves only type, age, use, importance and
		// confidence to rank by; a pin doubles the score as it does in recall.
		sc := ScoreMem(0.5, r.MemType, r.UpdatedAt, now, r.UseCount, r.Importance, r.Confidence)
		if r.Pinned == 1 {
			sc *= 2.0
		}
		e.scored = append(e.scored, scoredTop{sc, TopMemory{r.ID, r.MemType, r.Title}})
	}
	for _, e := range idx {
		e.Top = topMemories(e.scored, top)
	}
	return idx, nil
}

func topMemories(scored []scoredTop, top int) []TopMemory {
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	n := top
	if len(scored) < n {
		n = len(scored)
	}
	out := make([]TopMemory, 0, n)
	for _, st := range scored[:n] {
		out = append(out, st.m)
	}
	return out
}

// globMatch lets brain.json declare scopes and kinds as patterns ("client:*"). An exact
// name always matches, even one a malformed pattern would reject.
func globMatch(pattern, name string) bool {
	if pattern == name {
		return true
	}
	ok, err := filepath.Match(pattern, name)
	return err == nil && ok
}

type MapArgs struct {
	Top  int
	JSON bool
}

// mapCounts is the headline numbers.
type mapCounts struct {
	memories, superseded, events, leads int
}

// scopeReconciliation is the scopes in use set against the scopes declared.
type scopeReconciliation struct {
	inUse         []string // every scope with a live memory, sorted
	declared      []string // in use and matched by a declared pattern (or nothing is declared)
	undeclared    []string // in use, matched by no declared pattern
	emptyDeclared []string // declared patterns that match no scope in use
}

type typeCount struct {
	k string
	c int
}

type kindRow struct {
	kind string
	c    int
	t    int64
}

// tomb is the whole tombstone row, so a JSON caller gets the hash: it is how a caller
// tells whether a memory it is about to write is one that was deliberately forgotten.
type tomb struct {
	Hash        string `json:"hash"`
	Project     string `json:"project"`
	Scope       string `json:"scope"`
	Title       string `json:"title"`
	Reason      string `json:"reason"`
	ForgottenAt int64  `json:"forgotten_at"`
}

// Map is the index of what is in here: scopes, types, event kinds, measures and pinned
// memories, reconciled against what brain.json declares.
func (s *Store) Map(w io.Writer, a MapArgs) error {
	if a.Top <= 0 {
		a.Top = 3
	}
	regScopes := s.Env.Config.Scopes
	regKinds := s.Env.Config.Kinds
	idx, err := s.scopeIndex(a.Top)
	if err != nil {
		return err
	}
	counts := s.mapCounts()
	rec := reconcileScopes(idx, regScopes)

	bar := strings.Repeat("=", 72)
	lines := []string{bar, strings.ToUpper(s.Env.Project) + " BRAIN — MAP", bar,
		fmt.Sprintf("%d memories (%d superseded) · %d events · %d leads · %d scopes in use "+
			"· schema v%d", counts.memories, counts.superseded, counts.events, counts.leads,
			len(idx), SchemaVersion)}

	header := "SCOPES"
	if len(regScopes) > 0 {
		header = "SCOPES (declared)"
	}
	lines = append(lines, renderScopeSection(idx, regScopes, rec.declared, header, "")...)
	lines = append(lines, renderScopeSection(idx, regScopes, rec.undeclared,
		"UNDECLARED SCOPES IN USE",
		"declare these or refile them: os-brain scope add <name> --desc \"...\"")...)
	if len(rec.emptyDeclared) > 0 {
		lines = append(lines, "", "## DECLARED BUT EMPTY")
		for _, p := range rec.emptyDeclared {
			lines = append(lines, fmt.Sprintf("  %-24s %s", p, regScopes[p]))
		}
	}

	types := s.mapTypeCounts()
	lines = append(lines, renderTypesByTier(types)...)
	kinds := s.mapEventKinds()
	lines = append(lines, renderEventKinds(kinds, regKinds)...)
	series := s.measureOverview("")
	lines = append(lines, renderMapMeasures(series)...)

	pins, err := s.queryMemories("SELECT "+memoryCols+", 0.0 AS bm FROM memories m WHERE "+
		"m.project=? AND m.pinned=1 AND m.superseded_by IS NULL ORDER BY m.importance DESC",
		s.Env.Project)
	if err != nil {
		return err
	}
	if len(pins) > 0 {
		lines = append(lines, "", fmt.Sprintf("## PINNED (%d)", len(pins)))
		for _, r := range pins {
			lines = append(lines, fmt.Sprintf("  #%d [%s/%s] %s", r.ID, OneLine(r.Scope), r.MemType, OneLine(r.Title)))
		}
	}

	tombs := s.recentTombstones()
	lines = append(lines, renderTombstones(tombs, s.countRows("SELECT COUNT(*) FROM tombstones WHERE project=?"))...)
	lines = append(lines, "", bar)
	lines = append(lines, "read a scope: os-brain recall \"<q>\" --scope <scope>   "+
		"(scopes nest on ':' and see their ancestors)")

	scopeList := make([]*ScopeEntry, 0, len(idx))
	for _, sc := range rec.inUse {
		scopeList = append(scopeList, idx[sc])
	}
	typeObj := []map[string]interface{}{}
	for _, e := range types {
		typeObj = append(typeObj, map[string]interface{}{"mem_type": e.k, "c": e.c})
	}
	kindObj := []map[string]interface{}{}
	for _, e := range kinds {
		kindObj = append(kindObj, map[string]interface{}{"kind": e.kind, "c": e.c, "t": e.t})
	}
	pinObj := []map[string]interface{}{}
	for _, r := range pins {
		pinObj = append(pinObj, map[string]interface{}{"id": r.ID, "scope": r.Scope,
			"mem_type": r.MemType, "title": r.Title})
	}
	// Empty lists and maps are written as [] and {}, never null, so a JSON reader can
	// iterate every field without a nil check.
	return Out(w, a.JSON, strings.Join(lines, "\n"), map[string]interface{}{
		"project": s.Env.Project,
		"counts": map[string]interface{}{"memories": counts.memories,
			"superseded": counts.superseded, "events": counts.events, "leads": counts.leads},
		"declared": orEmptyMap(regScopes), "declaredKinds": orEmptyMap(regKinds),
		"scopes": scopeList, "undeclared_scopes": orEmptyList(rec.undeclared),
		"declared_empty": orEmptyList(rec.emptyDeclared), "types": typeObj, "kinds": kindObj,
		"measures": series, "pinned": pinObj, "tombstones": tombs})
}

func orEmptyList(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func orEmptyMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// countRows runs a COUNT(*) query that takes the project as its one argument.
func (s *Store) countRows(q string) int {
	var n int
	_ = s.DB.QueryRow(q, s.Env.Project).Scan(&n)
	return n
}

func (s *Store) mapCounts() mapCounts {
	return mapCounts{
		memories:   s.countRows("SELECT COUNT(*) FROM memories WHERE project=? AND superseded_by IS NULL"),
		superseded: s.countRows("SELECT COUNT(*) FROM memories WHERE project=? AND superseded_by IS NOT NULL"),
		events:     s.countRows("SELECT COUNT(*) FROM events WHERE project=?"),
		leads:      s.countRows("SELECT COUNT(*) FROM leads WHERE project=?"),
	}
}

// reconcileScopes sorts the scopes in use into declared and undeclared, and finds the
// declared patterns nothing uses. With nothing declared, every scope counts as declared:
// a brain that never wrote a scope list has nothing to be out of step with.
func reconcileScopes(idx map[string]*ScopeEntry, regScopes map[string]string) scopeReconciliation {
	var rec scopeReconciliation
	rec.inUse = []string{}
	for sc := range idx {
		rec.inUse = append(rec.inUse, sc)
	}
	sort.Strings(rec.inUse)

	matchedPatterns := map[string]bool{}
	for _, sc := range rec.inUse {
		hit := false
		for pat := range regScopes {
			if globMatch(pat, sc) {
				matchedPatterns[pat] = true
				hit = true
			}
		}
		if !hit && len(regScopes) > 0 {
			rec.undeclared = append(rec.undeclared, sc)
		} else {
			rec.declared = append(rec.declared, sc)
		}
	}
	for pat := range regScopes {
		if !matchedPatterns[pat] {
			rec.emptyDeclared = append(rec.emptyDeclared, pat)
		}
	}
	sort.Strings(rec.emptyDeclared)
	return rec
}

// scopeDescription is the description of the first declared pattern, in sorted order,
// that matches the scope.
func scopeDescription(regScopes map[string]string, scope string) string {
	var pats []string
	for p := range regScopes {
		pats = append(pats, p)
	}
	sort.Strings(pats)
	for _, p := range pats {
		if globMatch(p, scope) {
			return regScopes[p]
		}
	}
	return ""
}

// renderScopeSection is one "## header" section of scopes: counts by tier, the
// declared description, and the top memories of each. Nothing at all when names is empty.
func renderScopeSection(idx map[string]*ScopeEntry, regScopes map[string]string,
	names []string, header, note string) []string {
	if len(names) == 0 {
		return nil
	}
	lines := []string{"", "## " + header}
	if note != "" {
		lines = append(lines, "  "+note)
	}
	for _, sc := range names {
		e := idx[sc]
		t := e.Tiers
		lines = append(lines, fmt.Sprintf("  %-24s %4d mems  [A%d B%d C%d D%d N%d]  last %s",
			sc, e.Count, t["A"], t["B"], t["C"], t["D"], t["N"], Day(e.Last)))
		if desc := scopeDescription(regScopes, sc); desc != "" {
			lines = append(lines, "      "+OneLine(desc))
		}
		for _, m := range e.Top {
			lines = append(lines, fmt.Sprintf("      #%d [%s] %s", m.ID, m.Type, OneLine(m.Title)))
		}
	}
	return lines
}

// mapTypeCounts is live memories per type, most common first.
func (s *Store) mapTypeCounts() []typeCount {
	var types []typeCount
	rows, err := s.DB.Query("SELECT mem_type, COUNT(*) c FROM memories WHERE project=? "+
		"AND superseded_by IS NULL GROUP BY mem_type ORDER BY c DESC", s.Env.Project)
	if err != nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var e typeCount
		if err := rows.Scan(&e.k, &e.c); err == nil {
			types = append(types, e)
		}
	}
	return types
}

// renderTypesByTier groups the type counts under their decay tier, so the map shows how
// much of the brain fades and how fast.
func renderTypesByTier(types []typeCount) []string {
	if len(types) == 0 {
		return nil
	}
	lines := []string{"", "## TYPES BY TIER"}
	for _, t := range []struct{ tier, label string }{
		{"A", "A never decays"}, {"B", "B never decays"}, {"C", "C ~60d half-life"},
		{"D", "D ~4d half-life"}, {"N", "docs no decay"},
	} {
		var part []string
		for _, e := range types {
			if TierOf(e.k) == t.tier {
				part = append(part, fmt.Sprintf("%s %d", e.k, e.c))
			}
		}
		if len(part) > 0 {
			lines = append(lines, fmt.Sprintf("  %-18s %s", t.label, strings.Join(part, ", ")))
		}
	}
	return lines
}

// mapEventKinds is events per kind, most common first, with the last time each was seen.
func (s *Store) mapEventKinds() []kindRow {
	var kinds []kindRow
	rows, err := s.DB.Query("SELECT kind, COUNT(*) c, MAX(ts) t FROM events WHERE "+
		"project=? GROUP BY kind ORDER BY c DESC", s.Env.Project)
	if err != nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var e kindRow
		if err := rows.Scan(&e.kind, &e.c, &e.t); err == nil {
			kinds = append(kinds, e)
		}
	}
	return kinds
}

// renderEventKinds lists the kinds in use, flags those no declared pattern matches, and
// names the declared kinds nothing has used.
func renderEventKinds(kinds []kindRow, regKinds map[string]string) []string {
	if len(kinds) == 0 {
		return nil
	}
	lines := []string{"", "## EVENT KINDS"}
	seen := map[string]bool{}
	for _, e := range kinds {
		seen[e.kind] = true
		flag := ""
		if len(regKinds) > 0 && !kindDeclared(regKinds, e.kind) {
			flag = "   <- undeclared"
		}
		lines = append(lines, fmt.Sprintf("  %-24s %4d  last %s%s", e.kind, e.c,
			Day(e.t), flag))
	}
	// Compared by exact name, not as a pattern: a declared glob is listed here unless a
	// kind spelled exactly like the pattern has been used.
	var unused []string
	for k := range regKinds {
		if !seen[k] {
			unused = append(unused, k)
		}
	}
	sort.Strings(unused)
	if len(unused) > 0 {
		lines = append(lines, "  declared, unused: "+strings.Join(unused, ", "))
	}
	return lines
}

func kindDeclared(regKinds map[string]string, kind string) bool {
	for pat := range regKinds {
		if globMatch(pat, kind) {
			return true
		}
	}
	return false
}

// renderMapMeasures is one line per series: points, date span, latest value and band.
func renderMapMeasures(series []*MeasureSummary) []string {
	if len(series) == 0 {
		return nil
	}
	lines := []string{"", fmt.Sprintf("## MEASURES (%d series)", len(series))}
	for _, sm := range series {
		span := sm.LatestOn
		if sm.FirstOn != "" {
			span = sm.FirstOn + ".." + sm.LatestOn
		}
		band := ""
		if sm.Low != nil || sm.High != nil {
			band = fmt.Sprintf("  band %s..%s", fmtMeasure(sm.Low), fmtMeasure(sm.High))
		}
		tail := ""
		if sm.Breach != "" {
			tail = "   <- outside band"
		}
		lines = append(lines, fmt.Sprintf("  %-24s %4d pts  %s  %s%s%s%s", sm.Key, sm.N,
			span, fm(sm.Latest), unitSuffix(sm.Unit), band, tail))
	}
	return lines
}

// recentTombstones is the ten most recent forgettings, newest first.
func (s *Store) recentTombstones() []tomb {
	tombs := []tomb{}
	rows, err := s.DB.Query("SELECT hash, project, scope, title, reason, forgotten_at "+
		"FROM tombstones WHERE project=? ORDER BY forgotten_at DESC LIMIT 10",
		s.Env.Project)
	if err != nil {
		return tombs
	}
	defer rows.Close()
	for rows.Next() {
		var t tomb
		if err := rows.Scan(&t.Hash, &t.Project, &t.Scope, &t.Title, &t.Reason,
			&t.ForgottenAt); err == nil {
			tombs = append(tombs, t)
		}
	}
	return tombs
}

// renderTombstones shows what was forgotten so a session does not learn it straight back.
// The header carries the full count while the list shows only the recent ones.
func renderTombstones(tombs []tomb, total int) []string {
	if len(tombs) == 0 {
		return nil
	}
	lines := []string{"", fmt.Sprintf("## FORGOTTEN (%d) — do not relearn without "+
		"new evidence", total)}
	for _, t := range tombs {
		extra := ""
		if t.Reason != "" {
			extra = ", " + OneLine(t.Reason)
		}
		lines = append(lines, fmt.Sprintf("  [%s] %s  (%s%s)", OneLine(t.Scope), OneLine(t.Title),
			Day(t.ForgottenAt), extra))
	}
	return lines
}
