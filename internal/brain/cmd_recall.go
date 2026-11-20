package brain

import (
	"database/sql"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
)

// bm25 column weights: a title hit is worth eight body hits.
const bm25Weights = "8.0, 1.0"

// RecallBodyRunes is how much of a body recall prints. The rest is one `get` away, and a
// recall that printed every body whole put megabytes into an agent's context.
const RecallBodyRunes = 2000

// maxSupersedeHops bounds the walk from an outdated memory to its current successor, so a
// damaged chain that loops cannot hang a search.
const maxSupersedeHops = 8

type RecallArgs struct {
	Query       string
	Scope       string
	N           int
	Type        string
	Tier        string
	Since       int
	All         bool
	AllProjects bool
	Strict      bool
	Browse      bool
	NoTouch     bool
	OnDate      string
	FromDate    string
	ToDate      string
	JSON        bool
}

// recallMatch is what the lexical search found and how much to trust it.
type recallMatch struct {
	rows    []*Memory
	matched bool // rows came from the index, not from browsing the scope
	loose   bool // rows came from the any-term fallback: leads, not answers
	nTerms  int  // query terms, for CoverageWeight
}

// Recall runs FTS5 and ranks by the composite score. It fails closed: with no lexical
// match it returns nothing rather than the whole scope, unless a human explicitly asks to
// browse. A recall miss once fed a forget loop and destroyed real memories, because the
// "showing the whole scope" warning existed only in the human output.
func (s *Store) Recall(w io.Writer, a RecallArgs) error {
	now := Now()
	if a.N <= 0 {
		a.N = 8
	}
	if err := checkRecallFilters(a); err != nil {
		return err
	}
	where, params, err := s.recallWhere(a, now)
	if err != nil {
		return err
	}
	if a.AllProjects && !s.Env.Shared {
		// Honest about reach: this project keeps its own store, so there is nothing else
		// in here to search.
		Warn("this project has its own store, so --all-projects only searches it. " +
			"For what other projects have shared: os-brain global --recall \"<topic>\"")
	}

	// JSON callers are machines, and a machine cannot read the warning that a browse
	// fallback is not a match. Strict by default there.
	strict := a.Strict || (a.JSON && !a.Browse)

	// Measures are searched alongside memories: a series that reads as never-recorded is
	// the failure this integration exists to prevent.
	var measures []*MeasureSummary
	if a.Query != "" {
		measures = s.MeasuresMatching(a.Query, a.Scope)
	}
	var ident map[string]string
	if s.identityQuery(a.Query) {
		ident = s.IdentityCard()
	}

	var m recallMatch
	if a.Query != "" {
		if FTSQuery(a.Query, "OR") == "" {
			return Out(w, a.JSON, fmt.Sprintf("(%s has no searchable characters)", Repr(a.Query)),
				map[string]interface{}{"matched": false, "query": a.Query, "scope": a.Scope,
					"count": 0, "results": []*Memory{}})
		}
		if m, err = s.lexicalMatch(a.Query, where, params); err != nil {
			return err
		}
		if !m.matched && (a.Type != "" || a.Tier != "") {
			// Distinguish "nothing matched" from "matches existed, your filter removed
			// them". The second must not degrade into a whole-scope dump.
			n, err := s.countWithoutTypeFilter(a)
			if err != nil {
				return err
			}
			if n > 0 {
				return Out(w, a.JSON, fmt.Sprintf("(%d memories match %s, none of them %s; "+
					"drop the filter to see them)", n, Repr(a.Query), typeFilterFlag(a)),
					map[string]interface{}{"matched": false, "filtered_out": n,
						"query": a.Query, "scope": a.Scope, "count": 0, "results": []*Memory{}})
			}
		}
	}

	rows := m.rows
	if len(rows) == 0 {
		m.matched = false
		if a.Query != "" && strict {
			return s.renderStrictMiss(w, a, ident, measures)
		}
		// No query, or a lexical miss with browsing allowed: score the whole scope.
		if rows, err = s.browseScope(where, params, a.N); err != nil {
			return err
		}
	}

	if !a.All {
		if rows, err = s.forwardSuperseded(rows, a.AllProjects); err != nil {
			return err
		}
	}
	scoreRecallRows(rows, a.Scope, m, now)
	sortByScore(rows)
	if len(rows) > a.N {
		rows = rows[:a.N]
	}
	// After truncation, so a conflict is only reported against something the caller can
	// actually see. Flagging a tie with a memory that fell off the end would be telling
	// someone to go and compare with a row they were not shown.
	MarkConflicts(rows)

	// Reinforce only a real match. Counting the browse fallback as "used" is a feedback
	// loop that promotes whatever the last miss happened to return.
	if !a.NoTouch && m.matched {
		if err := s.markUsed(rows, now); err != nil {
			return err
		}
	}

	// After truncation: the verdict is only needed for what is actually rendered.
	s.StampFreshness(rows)

	human := s.renderRecall(a, ident, m, rows, measures)
	if rows == nil {
		// An empty result must serialise as [], never null: a caller doing
		// results.length on a miss should read zero, not crash.
		rows = []*Memory{}
	}
	if measures == nil {
		measures = []*MeasureSummary{}
	}
	return Out(w, a.JSON, human, map[string]interface{}{
		"matched": m.matched, "loose": m.loose, "query": a.Query, "scope": a.Scope,
		"count": len(rows), "results": rows, "identity": ident,
		"measures": measures, "measures_matched": len(measures) > 0})
}

// checkRecallFilters refuses a filter that names nothing. A typo'd filter silently returns
// nothing, which reads exactly like "no such memory".
func checkRecallFilters(a RecallArgs) error {
	if a.Type != "" && !AllTypes()[a.Type] {
		return fmt.Errorf("unknown type %s. valid: %s", Repr(a.Type), joinSorted(AllTypes()))
	}
	// Exactly one letter: a substring test let "BCD", which is inside "ABCDN", run as tier N.
	if t := strings.ToUpper(a.Tier); a.Tier != "" && (len(t) != 1 || !strings.Contains("ABCDN", t)) {
		return fmt.Errorf("unknown tier %s. valid: A B C D N", Repr(a.Tier))
	}
	return nil
}

// recallWhere builds the SQL filter every query of this recall shares: scope, project,
// --since, the date flags and the type or tier.
//
// Superseded rows deliberately STAY IN THE SEARCH. A query written in the outdated wording
// must find the outdated row so forwardSuperseded can answer with its successor. Filtering
// them out here made the forwarding unreachable: a retraction was only found when the
// correction happened to share words with the question, which is exactly the case where the
// forwarding was not needed. The exclusion happens after the walk instead.
func (s *Store) recallWhere(a RecallArgs, now int64) (string, []interface{}, error) {
	where, params := "1=1", []interface{}{}
	if a.Scope != "" {
		where, params = ScopeFilter(a.Scope, "m")
	}
	pw, pp := s.ProjectFilter("m", a.AllProjects)
	where += " AND " + pw
	params = append(params, pp...)
	if a.Since > 0 {
		where += " AND m.updated_at >= ?"
		params = append(params, now-int64(a.Since)*86400)
	}
	happened := HappenedSQL("m")
	for _, f := range []struct{ val, op, label string }{
		{a.OnDate, "=", "--on"}, {a.FromDate, ">=", "--from"}, {a.ToDate, "<=", "--to"},
	} {
		if f.val == "" {
			continue
		}
		d, err := ParseDate(f.val, f.label)
		if err != nil {
			return "", nil, err
		}
		where += fmt.Sprintf(" AND %s %s ?", happened, f.op)
		params = append(params, d)
	}
	if a.Type != "" {
		where += " AND m.mem_type = ?"
		params = append(params, a.Type)
	} else if a.Tier != "" {
		types := tierTypes(strings.ToUpper(a.Tier))
		op, names := "IN", sortedKeys(types)
		if types == nil {
			// Tier N: everything that is not one of the tiered memory types.
			op, names = "NOT IN", sortedKeys(MemTypes())
		}
		where += " AND m.mem_type " + op + " (" + placeholders(len(names)) + ")"
		params = append(params, toIfaces(names)...)
	}
	return where, params, nil
}

// lexicalMatch asks the index for any row holding any query term, then keeps the rows that
// hold enough of the terms (CoverageFloor).
//
// It used to search for all terms first and widen to any term only when that found
// NOTHING, so one extra word in a query silently removed the best row: a memory titled with
// three of four query words lost to five weaker rows that held all four somewhere in their
// bodies. Now any term finds the candidates and coverage decides which of them stay.
func (s *Store) lexicalMatch(query, where string, params []interface{}) (recallMatch, error) {
	found, err := s.ftsSearch(FTSQuery(query, "OR"), where, params)
	if err != nil {
		return recallMatch{}, err
	}
	terms := QueryTerms(query)
	s.termCoverage(FTSTerms(query), found, where, params)
	floor := CoverageFloor(len(terms))
	var m recallMatch
	for _, r := range found {
		if r.Cov >= floor {
			m.rows = append(m.rows, r)
		}
	}
	if len(m.rows) == 0 {
		// Nothing reached the floor, as with a natural-language sentence. Fall back to
		// any-term and flag it loose; CoverageWeight still orders the rows.
		m.rows = found
		m.loose = len(found) > 0 && floor > 1
	}
	m.nTerms = len(terms)
	m.matched = len(m.rows) > 0
	return m, nil
}

// countWithoutTypeFilter counts the query's matches with the --type or --tier filter
// dropped, over what this search may read. Counted across every project instead, the
// number would be a word oracle into private projects in a shared store.
func (s *Store) countWithoutTypeFilter(a RecallArgs) (int, error) {
	where, params := "1=1", []interface{}{}
	if a.Scope != "" {
		where, params = ScopeFilter(a.Scope, "m")
	}
	if !a.All {
		where += " AND m.superseded_by IS NULL"
	}
	pw, pp := s.ProjectFilter("m", a.AllProjects)
	where += " AND " + pw
	params = append(params, pp...)
	return s.ftsCount(FTSQuery(a.Query, "OR"), where, params)
}

// typeFilterFlag names the filter that removed every match, as the user typed it.
func typeFilterFlag(a RecallArgs) string {
	if a.Type != "" {
		return "--type " + a.Type
	}
	return "--tier " + strings.ToUpper(a.Tier)
}

// renderStrictMiss answers a query that matched nothing when browsing is not allowed.
// Browsing the whole scope on a miss is fine for a human reading the warning, and
// catastrophic for `recall | forget`, so nothing here carries a memory id.
//
// The identity card is the one thing that still answers on a strict miss, and this is the
// case that matters most: "what is this project" in a brain where no memory mentions it. It
// is config, not a memory, so it cannot feed a `recall | forget` loop.
func (s *Store) renderStrictMiss(w io.Writer, a RecallArgs, ident map[string]string,
	measures []*MeasureSummary) error {
	prefix := ""
	if ident != nil {
		prefix = strings.Join(identityLines(ident), "\n") + "\n\n"
	}
	if len(measures) > 0 {
		var body []string
		for _, sm := range measures {
			body = append(body, MeasureLine(sm, "  "))
		}
		return Out(w, a.JSON, fmt.Sprintf("%s(no match for %s in memory, but there "+
			"are measures)\n%s\n  os-brain measure series <key> for the history",
			prefix, Repr(a.Query), strings.Join(body, "\n")),
			map[string]interface{}{"matched": false, "measures_matched": true,
				"query": a.Query, "scope": a.Scope, "count": 0,
				"results": []*Memory{}, "measures": measures, "identity": ident})
	}
	tail := "; --browse to see the scope anyway)"
	if ident != nil {
		tail = "; the identity above is config, not a memory)"
	}
	// A miss is the moment a session is most likely to give up and ask the user something
	// the store already holds, or go and grep the filesystem for it. So it is worth spending
	// output on: not the nearest memory, which would be the whole-scope failure again, but
	// what this project holds NEAR the query, by category. "It has three deployment facts"
	// is enough to make somebody look; silence is not.
	near, _ := s.Neighbourhood(a.Query, 5)
	body := ""
	for _, c := range near {
		body += fmt.Sprintf("\n  %-20s %d", c.Name, c.Count)
	}
	if body != "" {
		body = "\n  this project does hold:" + body +
			"\n  os-brain coverage · os-brain recall \"<one of those words>\""
	}
	return Out(w, a.JSON, fmt.Sprintf("%s(no match for %s in memory%s%s", prefix,
		Repr(a.Query), tail, body),
		map[string]interface{}{"matched": false, "query": a.Query, "scope": a.Scope,
			"count": 0, "results": []*Memory{}, "measures": []*MeasureSummary{},
			"identity": ident, "neighbourhood": near})
}

// browseScope returns candidates when there is nothing to match on. It reads well past n
// (at least 200) because the composite score, not this ORDER BY, decides the final order.
func (s *Store) browseScope(where string, params []interface{}, n int) ([]*Memory, error) {
	limit := n * 20
	if limit < 200 {
		limit = 200
	}
	return s.queryMemories("SELECT "+memoryCols+", 0.0 AS bm FROM memories m WHERE "+
		where+" ORDER BY pinned DESC, importance DESC, updated_at DESC LIMIT ?",
		append(append([]interface{}{}, params...), limit)...)
}

// forwardSuperseded makes a query that only matches the outdated version still answer with
// the current one: it follows superseded_by forward and de-duplicates onto the successor,
// carrying the best match strength and coverage of every row that led there, so a forwarded
// successor ranks on how well the query actually matched. The rows that led there are
// recorded as Via. Order is first appearance.
func (s *Store) forwardSuperseded(rows []*Memory, allProjects bool) ([]*Memory, error) {
	best := map[int64]*Memory{}
	var order []int64
	for _, r := range rows {
		row, dangling, err := s.walkToCurrent(r, allProjects)
		if err != nil {
			return nil, err
		}
		// The walk landed on a live memory, or on the end of a broken chain. Either way,
		// an outdated row never reaches the caller unless --all asked for it.
		if row.SupersededBy != nil && !dangling {
			continue
		}
		cur := *row
		cov := r.Cov
		if row.ID != r.ID {
			cur.Via = []ViaRef{{ID: r.ID, Title: r.Title, UpdatedAt: r.UpdatedAt}}
		}
		if prev, ok := best[row.ID]; ok {
			cur.Via = append(append([]ViaRef{}, prev.Via...), cur.Via...)
			cur.BM = math.Min(r.BM, prev.BM) // bm25 is negative; more negative is better
			if prev.Cov > cov {
				cov = prev.Cov
			}
		} else {
			cur.BM = r.BM
			order = append(order, row.ID)
		}
		cur.Cov = cov
		best[row.ID] = &cur
	}
	// Reuses rows' backing array: every row has been read by now, and there are never more
	// successors than rows.
	rows = rows[:0]
	for _, id := range order {
		rows = append(rows, best[id])
	}
	return rows, nil
}

// walkToCurrent follows r's superseded_by links forward, at most maxSupersedeHops of them.
// dangling reports that a link pointed at a memory that no longer exists.
func (s *Store) walkToCurrent(r *Memory, allProjects bool) (row *Memory, dangling bool, err error) {
	row = r
	for hops := 0; row.SupersededBy != nil && hops < maxSupersedeHops; hops++ {
		next, err := s.byID(*row.SupersededBy, allProjects)
		if err != nil {
			return nil, false, err
		}
		if next == nil {
			// The successor is gone, which no command does (forget refuses to delete a
			// memory that supersedes others), so this is damage. Answer with the row rather
			// than drop it: a memory that silently vanishes from search is worse than one
			// whose retraction was lost, and doctor reports the dangling link either way.
			return row, true, nil
		}
		row = next
	}
	return row, false, nil
}

// scoreRecallRows stamps each row with its final score: relevance (bm25 scaled by query
// coverage on a match, a flat BrowseRelevance otherwise) through ScoreMem, the pin boost on
// a browse, and the weight of the scope that was asked for. Rounded before the sort.
func scoreRecallRows(rows []*Memory, scope string, m recallMatch, now int64) {
	// Naming a scope has to prioritise it: its ancestors stay visible, they just stop
	// outranking it. A glob names no single scope, so it weights nothing.
	var chain []string
	if asked := trim(scope); asked != "" && !strings.ContainsAny(asked, "*?") {
		chain = ScopeAncestors(asked)
	}
	for _, r := range rows {
		relevance := BrowseRelevance
		if m.matched {
			relevance = RelevanceFromBM25(r.BM) * CoverageWeight(r.Cov, m.nTerms)
		}
		score := ScoreMem(relevance, r.MemType, r.UpdatedAt, now, r.UseCount, r.Importance, r.Confidence)
		if r.Pinned == 1 && !m.matched {
			score *= PinBoost
		}
		score *= ScopeWeight(chain, r.Scope)
		r.Score = RoundScore(score)
		r.Matched = m.matched
		r.Loose = m.loose
		r.InRecall = true
		r.Tier = TierOf(r.MemType)
	}
}

// sortByScore orders by score, then pinned first, then newest, then highest id. The id makes
// the order total, so two memories written in the same second cannot swap places between
// runs depending on the row order SQLite happened to return.
func sortByScore(rows []*Memory) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Score != rows[j].Score {
			return rows[i].Score > rows[j].Score
		}
		if rows[i].Pinned != rows[j].Pinned {
			return rows[i].Pinned > rows[j].Pinned
		}
		if rows[i].UpdatedAt != rows[j].UpdatedAt {
			return rows[i].UpdatedAt > rows[j].UpdatedAt
		}
		return rows[i].ID > rows[j].ID
	})
}

// markUsed counts a real match as a use. Only this project's rows: a search from here is not
// a use of another project's memory, and bumping it would rerank that project's store.
func (s *Store) markUsed(rows []*Memory, now int64) error {
	for _, r := range rows {
		if _, err := s.DB.Exec("UPDATE memories SET use_count=use_count+1, last_used=? "+
			"WHERE id=? AND project=?", now, r.ID, s.Env.Project); err != nil {
			return err
		}
	}
	return nil
}

// LooseNote heads a recall whose rows came from the any-term fallback. They are matches, so
// "matched" stays true, but a caller told to act on "matched" could not tell ten rows that
// share one word with the query from a real hit: a query about a desktop's boot fault once
// returned ten unrelated design memories this way, at relevance 0.14 to 0.33.
func LooseNote(query string) string {
	return fmt.Sprintf("(loose match for %s: no memory holds most of its words, these hold "+
		"at least one. Treat them as leads, not answers)", Repr(query))
}

// renderRecall is the human output: identity card, the loose or browse warning, one entry
// per row, then any matching measures.
func (s *Store) renderRecall(a RecallArgs, ident map[string]string, m recallMatch,
	rows []*Memory, measures []*MeasureSummary) string {
	var lines []string
	if ident != nil {
		lines = append(lines, identityLines(ident)...)
		lines = append(lines, "")
	}
	if m.loose && len(rows) > 0 {
		lines = append(lines, LooseNote(a.Query))
	}
	if a.Query != "" && !m.matched && len(rows) > 0 {
		lines = append(lines, fmt.Sprintf("(no lexical match for %s; showing the "+
			"highest-scoring memories in scope)", Repr(a.Query)))
	}
	dated := a.OnDate != "" || a.FromDate != "" || a.ToDate != ""
	for _, r := range rows {
		lines = append(lines, s.recallEntry(r, dated)...)
	}
	if len(measures) > 0 {
		lines = append(lines, "", fmt.Sprintf("MEASURES matching %s", Repr(a.Query)))
		for _, sm := range measures {
			lines = append(lines, MeasureLine(sm, "  "))
		}
		lines = append(lines, "  os-brain measure series <key> for the history")
	}
	human := strings.Join(lines, "\n")
	if human == "" {
		human = "(no memories)"
	}
	return human
}

// recallEntry renders one row: the headline, the clipped body, provenance, the rows it was
// reached through, and any conflict or freshness verdict. dated shows the day a memory is
// about, which is only interesting when the caller filtered by date.
func (s *Store) recallEntry(r *Memory, dated bool) []string {
	pin := " "
	if r.Pinned == 1 {
		pin = "*"
	}
	old := ""
	if r.SupersededBy != nil {
		old = "  [superseded]"
	}
	conf := ""
	if r.Confidence != DefaultConfidence {
		conf = " [" + r.Confidence + "]"
	}
	home := ""
	if r.Project != "" && r.Project != s.Env.Project {
		home = " <" + OneLine(r.Project) + ">"
	}
	when := ""
	if dated && r.HappenedOn != "" {
		when = " " + r.HappenedOn
	}
	lines := []string{fmt.Sprintf("#%d%s%s%s [%s/%s] (score %.3f) %s%s%s",
		r.ID, pin, home, OneLine(when), OneLine(r.Scope), r.MemType, r.Score, OneLine(r.Title), conf, old)}
	if r.Body != "" {
		body := ClipWith(BodyText(r.Body), RecallBodyRunes, fmt.Sprintf("os-brain get %d", r.ID))
		lines = append(lines, "      "+strings.ReplaceAll(body, "\n", "\n      "))
	}
	var prov []string
	if r.Source != "" && r.Source != "session" {
		prov = append(prov, OneLine(r.Source))
	}
	if r.SourceDate != "" {
		prov = append(prov, OneLine(r.SourceDate))
	}
	if len(prov) > 0 {
		lines = append(lines, "      ("+strings.Join(prov, " · ")+")")
	}
	for _, v := range r.Via {
		lines = append(lines, "      "+RenderVia(v))
	}
	if r.Conflict != nil {
		lines = append(lines, "      ! "+r.Conflict.Note)
	}
	if r.Fresh != nil {
		lines = append(lines, "      "+r.Fresh.Note)
	}
	return lines
}

func (s *Store) ftsSearch(match, where string, params []interface{}) ([]*Memory, error) {
	q := fmt.Sprintf("SELECT %s, bm25(mem_fts, %s) AS bm FROM mem_fts "+
		"JOIN memories m ON m.id = mem_fts.rowid WHERE mem_fts MATCH ? AND %s",
		prefixed(memoryCols, "m"), bm25Weights, where)
	// Every term is quoted, so no sentence can be a malformed expression. An error here is
	// the index failing, and reporting it as "no match" is the silent miss a fail-closed
	// recall must not produce.
	return s.queryMemories(q, append([]interface{}{match}, params...)...)
}

// termCoverage stamps each row with how many of the query's terms it holds, asked of the
// index one term at a time so the answer is the tokeniser's and not a second opinion. Each
// question carries the search's own filter: unfiltered, every prompt would scan every
// project's index once per word.
func (s *Store) termCoverage(terms []string, rows []*Memory, where string, params []interface{}) {
	if len(rows) == 0 {
		return
	}
	byID := make(map[int64]*Memory, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	for _, t := range terms {
		hit, err := s.DB.Query("SELECT mem_fts.rowid FROM mem_fts JOIN memories m ON "+
			"m.id = mem_fts.rowid WHERE mem_fts MATCH ? AND "+where,
			append([]interface{}{t}, params...)...)
		if err != nil {
			continue
		}
		for hit.Next() {
			var id int64
			if hit.Scan(&id) == nil && byID[id] != nil {
				byID[id].Cov++
			}
		}
		hit.Close()
	}
}

func (s *Store) ftsCount(match, where string, params []interface{}) (int, error) {
	var n int
	err := s.DB.QueryRow("SELECT COUNT(*) FROM mem_fts JOIN memories m ON m.id = mem_fts.rowid "+
		"WHERE mem_fts MATCH ? AND "+where, append([]interface{}{match}, params...)...).Scan(&n)
	return n, err
}

// querier is whatever can run a query: the store's own handle or an open transaction.
// Reads issued inside a write transaction MUST use the transaction, because the store
// keeps one connection and the write holds it.
type querier interface {
	Query(string, ...interface{}) (*sql.Rows, error)
}

func (s *Store) queryMemories(q string, args ...interface{}) ([]*Memory, error) {
	return scanMemories(s.DB, q, args...)
}

func scanMemories(db querier, q string, args ...interface{}) ([]*Memory, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Initialised, not nil: every one of these lists reaches JSON, and a nil slice
	// marshals to null where callers expect [].
	out := []*Memory{}
	for rows.Next() {
		m, err := scanMemory(rows, true)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// byID is Readable with "not here" as nil rather than an error, for the internal walks
// (a supersession chain, a restore) that treat a missing row as damage to report.
func (s *Store) byID(id int64, allProjects bool) (*Memory, error) {
	m, err := s.Readable(id, allProjects)
	if err != nil && err.Error() == NoMemory(id).Error() {
		return nil, nil
	}
	return m, err
}

// tierTypes is the set of memory types in a tier; nil for tier N, which is defined by
// what it is not.
func tierTypes(tier string) map[string]bool {
	switch tier {
	case "A":
		return TierA
	case "B":
		return TierB
	case "C":
		return TierC
	case "D":
		return TierD
	}
	return nil
}

// prefixed qualifies every column of a comma-separated list with a table alias.
func prefixed(cols, alias string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = alias + "." + strings.TrimSpace(strings.ReplaceAll(p, "\n\t", ""))
	}
	return strings.Join(parts, ", ")
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func toIfaces(ss []string) []interface{} {
	out := make([]interface{}, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func joinSorted(m map[string]bool) string { return strings.Join(sortedKeys(m), ", ") }
