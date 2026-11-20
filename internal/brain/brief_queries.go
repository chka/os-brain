package brain

import (
	"fmt"
	"strconv"
	"strings"
)

// renderMemories is how the brief prints a list of memories: one title line with the
// trust flags, then the body clipped to clip characters and indented under it.
func renderMemories(ms []*Memory, now int64, clip int) []string {
	var lines []string
	for _, m := range ms {
		lines = append(lines, fmt.Sprintf("  #%d [%s]%s %s", m.ID, m.MemType,
			trustFlags(m, now), OneLine(m.Title)))
		if m.Body != "" {
			lines = append(lines, "     "+strings.ReplaceAll(BodyText(Clip(m.Body, clip)), "\n", "\n     "))
		}
	}
	return lines
}

// trustFlags renders what the confidence system exists to express, where it is read:
// a confidence other than the default, a verification that has gone stale, and how far
// the tree has moved since the memory was written.
func trustFlags(m *Memory, now int64) string {
	var bits []string
	if conf := NormConfidence(m.Confidence); conf != DefaultConfidence {
		bits = append(bits, conf)
	}
	if m.VerifiedAt != nil {
		if days := (now - *m.VerifiedAt) / 86400; days >= 180 {
			bits = append(bits, fmt.Sprintf("unverified %dd", days))
		}
	} else if t := TierOf(m.MemType); t == "A" || t == "B" {
		// Old truth reads exactly like current truth.
		bits = append(bits, "never verified")
	}
	// Same idea as the line above, for the half of it that git can settle outright.
	if m.Fresh != nil {
		bits = append(bits, freshnessFlag(m.Fresh))
	}
	if len(bits) == 0 {
		return ""
	}
	return " [" + strings.Join(bits, ", ") + "]"
}

func freshnessFlag(f *Freshness) string {
	switch {
	case f.Unchanged:
		return "tree unchanged"
	case f.Unknown:
		return "commit not in tree"
	case f.Commits == 1:
		return "1 commit since"
	}
	return fmt.Sprintf("%d commits since", f.Commits)
}

type scopeCount struct {
	Scope string `json:"scope"`
	Count int    `json:"count"`
}

// scopeCounts is the live memory count per scope, largest first. Never nil, so the
// brief's JSON carries [] for a project with no memories.
func (s *Store) scopeCounts() ([]scopeCount, error) {
	out, err := s.labelCounts("SELECT scope, COUNT(*) c FROM memories WHERE project=? AND "+
		"superseded_by IS NULL GROUP BY scope ORDER BY c DESC", s.Env.Project)
	if out == nil && err == nil {
		out = []scopeCount{}
	}
	return out, err
}

// otherLabels is the row count under every project label except this one, for telling a
// project that sees nothing whether the store is really empty.
func (s *Store) otherLabels() ([]scopeCount, error) {
	return s.labelCounts("SELECT project, COUNT(*) c FROM memories WHERE project <> ? "+
		"GROUP BY project ORDER BY c DESC", s.Env.Project)
}

func (s *Store) labelCounts(q string, args ...interface{}) ([]scopeCount, error) {
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []scopeCount
	for rows.Next() {
		var sc scopeCount
		if err := rows.Scan(&sc.Scope, &sc.Count); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// projectRootIDs is name -> root from the shared index, for telling a label that belongs
// to a live project apart from one this project could legitimately claim.
func (s *Store) projectRootIDs() map[string]string {
	out := map[string]string{}
	if !s.tableExists("projects") {
		return out
	}
	rows, err := s.DB.Query("SELECT name, root_id FROM projects")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var name, rootID string
		if err := rows.Scan(&name, &rootID); err != nil {
			return out
		}
		out[name] = rootID
	}
	return out
}

// PipelineRow is one status bucket of the sales pipeline. The JSON keys are the column
// aliases of the query (`c` and `v`), which is what ns-brain has always emitted.
type PipelineRow struct {
	Status string `json:"status"`
	Count  int64  `json:"c"`
	Value  int64  `json:"v"`
}

func (s *Store) pipelineByStatus() ([]PipelineRow, error) {
	rows, err := s.DB.Query("SELECT status, COUNT(*) c, COALESCE(SUM(value_est),0) v "+
		"FROM leads WHERE project=? GROUP BY status ORDER BY status", s.Env.Project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PipelineRow{}
	for rows.Next() {
		var p PipelineRow
		if err := rows.Scan(&p.Status, &p.Count, &p.Value); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) leadsQuery(where string, args ...interface{}) ([]*Lead, error) {
	rows, err := s.DB.Query("SELECT "+leadCols+" FROM leads WHERE project=? "+where,
		append([]interface{}{s.Env.Project}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Lead{}
	for rows.Next() {
		l, err := scanLead(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) hotLeads() ([]*Lead, error) {
	return s.leadsQuery("AND status IN ("+placeholders(len(LeadHotStatuses))+") "+
		"ORDER BY value_est DESC LIMIT ?",
		append(AnyOf(LeadHotStatuses), briefHotLeads)...)
}

func (s *Store) leadsWithNextAction() ([]*Lead, error) {
	return s.leadsQuery("AND next_action != '' AND status NOT IN ("+
		placeholders(len(LeadClosedStatuses))+") ORDER BY next_date LIMIT ?",
		append(AnyOf(LeadClosedStatuses), briefNextActions)...)
}

func (s *Store) recentEvents(n int) ([]Event, error) {
	rows, err := s.DB.Query("SELECT id, project, ts, scope, kind, actor, summary, ref, session FROM events "+
		"WHERE project=? ORDER BY ts DESC, id DESC LIMIT ?", s.Env.Project, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Project, &e.TS, &e.Scope, &e.Kind, &e.Actor,
			&e.Summary, &e.Ref, &e.Session); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) count(q string, args ...interface{}) (int, error) {
	var n int
	err := s.DB.QueryRow(q, args...).Scan(&n)
	return n, err
}

// metaStr reads a value from the store's meta table, or "" when it is not set.
func (s *Store) metaStr(key string) string {
	var v string
	if err := s.DB.QueryRow("SELECT v FROM meta WHERE k=?", key).Scan(&v); err != nil {
		return ""
	}
	return v
}

// metaInt reads a numeric meta value, or 0 when it is unset or unreadable.
func (s *Store) metaInt(key string) int64 {
	// `env` builds a Store with no database (EnvBlockFor): the clock, the paths and git
	// are this machine's to answer, and there is no last-session cursor to read.
	if s.DB == nil {
		return 0
	}
	n, _ := strconv.ParseInt(s.metaStr(key), 10, 64)
	return n
}
