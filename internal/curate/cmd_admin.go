package curate

import (
	"database/sql"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chka/os-brain/internal/lexicon"
	"github.com/chka/os-brain/internal/pyfmt"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/textguard"
	"github.com/chka/os-brain/internal/timefmt"
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
	if err := textguard.CheckToken(dest, "scope"); err != nil {
		return err
	}
	s.CheckDeclared(dest, "scope")
	// The scope is part of a memory's identity, so the hash moves with it, and a memory
	// already holding that identity in the destination is a merge, not a refile.
	h := store.ContentHash(s.Env.Project, dest, m.Title)
	var clash int64
	err = s.DB.QueryRow("SELECT id FROM memories WHERE hash=? AND id<>?", h, a.ID).Scan(&clash)
	if err == nil {
		return fmt.Errorf("#%d already holds that title in %s; merge them instead", clash, pyfmt.Repr(dest))
	} else if err != sql.ErrNoRows {
		return err
	}
	if _, err := s.DB.Exec("UPDATE memories SET scope=?, hash=?, updated_at=? WHERE id=?",
		dest, h, timefmt.Now(), a.ID); err != nil {
		return err
	}
	return store.Out(w, a.JSON, fmt.Sprintf("promoted #%d from %s to %s", a.ID, m.Scope, dest),
		map[string]interface{}{"id": a.ID, "from": m.Scope, "to": dest})
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
	rows  []*store.Memory
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
	rows, err := s.QueryMemories("SELECT "+store.MemoryCols+", 0.0 AS bm FROM memories m "+
		"WHERE m.project=? AND m.superseded_by IS NULL", s.Env.Project)
	if err != nil {
		return err
	}
	bar := strings.Repeat("=", 72)
	r := &review{s: s, a: a, now: timefmt.Now(), rows: rows,
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
	return store.Out(w, a.JSON, strings.Join(r.lines, "\n"), r.found)
}

// section prints up to a.N memories under a heading, with an optional hint line, and
// returns every id for the JSON. An empty section prints nothing.
func (r *review) section(title string, ms []*store.Memory, render func(*store.Memory) string, hint string) []int64 {
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

func (r *review) where(keep func(*store.Memory) bool) []*store.Memory {
	var out []*store.Memory
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
	byScope := map[string][]*store.Memory{}
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
func similarTitles(ms []*store.Memory) []dupePair {
	toks := map[int64]map[string]bool{}
	for _, m := range ms {
		set := map[string]bool{}
		for _, t := range strings.Fields(lexicon.NormTitle(m.Title)) {
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
			if j := lexicon.Jaccard(ta, tb); j >= 0.5 {
				out = append(out, dupePair{score: store.Round2(j), a: x.ID, b: y.ID, at: x.Title, bt: y.Title})
			}
		}
	}
	return out
}

func (r *review) stale() {
	stale := r.where(func(m *store.Memory) bool {
		return store.TierOf(m.MemType) == "D" && m.UpdatedAt < r.now-14*86400
	})
	r.found["stale"] = r.section("DECAYED EPHEMERA (tier D, >14d) — prune or promote", stale,
		func(m *store.Memory) string {
			return fmt.Sprintf("  #%d [%s] %s  (%s)", m.ID, textguard.OneLine(m.Scope), textguard.OneLine(m.Title), timefmt.Day(m.UpdatedAt))
		}, "  os-brain prune --tier D --older-than 14   (dry run; add --apply)")
}

func (r *review) cold() {
	cold := r.where(func(m *store.Memory) bool {
		return m.UseCount == 0 && m.CreatedAt < r.now-60*86400 && m.Pinned == 0
	})
	r.found["cold"] = r.section("NEVER RECALLED (>60d) — still true, or noise?", cold,
		func(m *store.Memory) string {
			return fmt.Sprintf("  #%d [%s/%s] %s", m.ID, textguard.OneLine(m.Scope), m.MemType, textguard.OneLine(m.Title))
		}, "")
}

func (r *review) promotions() {
	promo := r.where(func(m *store.Memory) bool { return m.Scope != "global" && m.UseCount >= 5 })
	sort.SliceStable(promo, func(i, j int) bool { return promo[i].UseCount > promo[j].UseCount })
	r.found["promote"] = r.section("PROMOTION CANDIDATES — recalled often, may be project-wide",
		promo, func(m *store.Memory) string {
			return fmt.Sprintf("  #%d [%s] used %dx  %s", m.ID, textguard.OneLine(m.Scope), m.UseCount, textguard.OneLine(m.Title))
		}, "  os-brain promote <id>            (moves it to global)")
}

// crossScope finds the same title filed in two scopes, which is how a brain ends up
// disagreeing with itself.
func (r *review) crossScope() {
	byTitle := map[string][]*store.Memory{}
	var titleOrder []string
	for _, m := range r.rows {
		k := lexicon.NormTitle(m.Title)
		if _, seen := byTitle[k]; !seen {
			titleOrder = append(titleOrder, k)
		}
		byTitle[k] = append(byTitle[k], m)
	}
	var cross [][]*store.Memory
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
			r.lines = append(r.lines, "  "+strings.Join(parts, " / ")+"  "+textguard.OneLine(ms[0].Title))
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
func lastChecked(m *store.Memory) int64 {
	if m.VerifiedAt != nil {
		return *m.VerifiedAt
	}
	return m.CreatedAt
}

func (r *review) unverified() {
	unver := r.where(func(m *store.Memory) bool {
		t := store.TierOf(m.MemType)
		return (t == "A" || t == "B") && lastChecked(m) < r.now-int64(r.a.StaleDays)*86400
	})
	// Sorted on (verified_at or created_at), not on their sum: a memory verified long ago
	// but created recently sorts differently under the two rules.
	sort.SliceStable(unver, func(i, j int) bool { return lastChecked(unver[i]) < lastChecked(unver[j]) })
	r.found["unverified"] = r.section(fmt.Sprintf("UNVERIFIED >%dd (tier A/B) — still true?",
		r.a.StaleDays), unver, func(m *store.Memory) string {
		last := "never"
		if m.VerifiedAt != nil {
			last = timefmt.Day(*m.VerifiedAt)
		}
		return fmt.Sprintf("  #%d [%s/%s] %s  (last verified %s)", m.ID, textguard.OneLine(m.Scope), m.MemType,
			textguard.OneLine(m.Title), last)
	}, "  re-check, then: os-brain verify <id> [--confidence confirmed]")
}

func (r *review) assumed() {
	soft := r.where(func(m *store.Memory) bool {
		return m.Confidence == "assumed" && (m.Pinned == 1 || store.TierOf(m.MemType) == "A" ||
			store.TierOf(m.MemType) == "B")
	})
	r.found["assumed"] = r.section("ASSUMED BUT LOAD-BEARING — verify or downgrade", soft,
		func(m *store.Memory) string {
			return fmt.Sprintf("  #%d [%s/%s] %s", m.ID, textguard.OneLine(m.Scope), m.MemType, textguard.OneLine(m.Title))
		}, "")
}

func (r *review) reviewDue() {
	soon := r.where(func(m *store.Memory) bool { return m.ReviewOn != nil && *m.ReviewOn <= r.now+7*86400 })
	sort.SliceStable(soon, func(i, j int) bool { return *soon[i].ReviewOn < *soon[j].ReviewOn })
	r.found["review_due"] = r.section("REVIEW DUE OR DUE SOON", soon, func(m *store.Memory) string {
		return fmt.Sprintf("  #%d %s  %s", m.ID, timefmt.Day(*m.ReviewOn), textguard.OneLine(m.Title))
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
