package brain

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// The coverage map answers the question a search cannot: not "what matches this" but
// "what is here at all".
//
// The failure behind it: a session asked whether the brain held a fact, got no hit on the
// words it happened to search with, and concluded the brain did not know, when it held
// the answer under different vocabulary the whole time. Retrieval assumes a query, and a
// query assumes the searcher already suspects the answer exists. A store can hold the
// perfect answer, score it perfectly, and never be asked.
//
// So coverage reports what this project knows ABOUT: categories with a count and how
// recently each was touched. A few lines turn "I do not know whether the brain knows" into
// "it says it holds three deployment facts", which is enough to make a session go and look.

// Category is one line of the coverage map.
type Category struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	Last  int64  `json:"last"`
}

// Coverage reports what THIS project's memory holds, grouped by memory type and, as a
// second grouping, by scope, ordered by count. Superseded memories are excluded: a
// retracted fact answering "what do you know about X" would misreport the map exactly as
// it would misreport recall.
func (s *Store) Coverage() ([]Category, error) {
	byType, err := s.coverageBy("mem_type")
	if err != nil {
		return nil, err
	}
	byScope, err := s.coverageBy("scope")
	if err != nil {
		return nil, err
	}
	for i := range byScope {
		byScope[i].Name = "scope:" + byScope[i].Name
	}
	out := append(byType, byScope...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out, nil
}

// coverageBy counts live memories grouped by one column. column is always one of the two
// literals Coverage passes, never caller input, which is why it can be spliced in.
func (s *Store) coverageBy(column string) ([]Category, error) {
	rows, err := s.DB.Query("SELECT "+column+", COUNT(*), MAX(updated_at) FROM memories "+
		"WHERE project=? AND superseded_by IS NULL GROUP BY "+column+" ORDER BY COUNT(*) DESC",
		s.Env.Project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Category{}
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.Name, &c.Count, &c.Last); err != nil {
			return out, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Neighbourhood is what a recall MISS returns: the categories this project holds that are
// nearest the query, so a session that found nothing is told what IS here rather than
// concluding nothing is.
func (s *Store) Neighbourhood(query string, n int) ([]Category, error) {
	if n <= 0 {
		n = 5
	}
	cats, err := s.Coverage()
	if err != nil {
		return nil, err
	}
	return NeighbourhoodOf(query, cats, n), nil
}

// NeighbourhoodOf ranks categories already counted (largest first) by how near their names
// are to the query. Matching is deliberately simple and lexical, the share of tokens
// between the query and a category's name, because a similarity metric dressed up as more
// than that would be a claim this store cannot back.
//
// If nothing is near, it falls back to the largest categories and says nothing about
// relevance: returning nothing at all is the exact failure this exists to prevent, so an
// unranked list beats an empty one.
func NeighbourhoodOf(query string, cats []Category, n int) []Category {
	if n <= 0 {
		n = 5
	}
	if len(cats) == 0 {
		return cats
	}
	qtoks := tokenSet(query)
	type scored struct {
		c Category
		v float64
	}
	var near []scored
	for _, c := range cats {
		ctoks := tokenSet(strings.TrimPrefix(c.Name, "scope:"))
		if v := jaccard(qtoks, ctoks); v > 0 {
			near = append(near, scored{c, v})
		}
	}
	if len(near) == 0 {
		if len(cats) > n {
			return cats[:n]
		}
		return cats
	}
	sort.SliceStable(near, func(i, j int) bool { return near[i].v > near[j].v })
	out := make([]Category, 0, n)
	for _, sc := range near {
		out = append(out, sc.c)
		if len(out) == n {
			break
		}
	}
	return out
}

// tokenSet splits on whitespace AND on the separators that appear inside the names this
// compares. A scope is ':'-nested, so splitting on whitespace alone left
// "channel:deploy:prod" as one token that could never overlap a query saying "deploy
// prod", which made the neighbourhood blind to scope names, the half most likely to match
// what somebody typed.
func tokenSet(s string) map[string]bool {
	out := map[string]bool{}
	fields := strings.FieldsFunc(lower(s), func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', ':', '/', '-', '_', '.', ',', ';', '!', '?',
			'(', ')', '[', ']', '{', '}', '"', '\'':
			return true
		}
		return false
	})
	for _, f := range fields {
		if f != "" {
			out[f] = true
		}
	}
	return out
}

// CoverageCmd prints the coverage map, or with --near only the categories nearest a word.
func (s *Store) CoverageCmd(w io.Writer, near string, asJSON bool) error {
	var cats []Category
	var err error
	if near != "" {
		cats, err = s.Neighbourhood(near, 8)
	} else {
		cats, err = s.Coverage()
	}
	if err != nil {
		return err
	}
	if len(cats) == 0 {
		return Out(w, asJSON, "this project holds nothing yet", []Category{})
	}
	var lines []string
	if near != "" {
		lines = append(lines, fmt.Sprintf("nearest to %s:", Repr(near)))
	}
	for _, c := range cats {
		lines = append(lines, fmt.Sprintf("  %-22s %3d   last %s", c.Name, c.Count, Day(c.Last)))
	}
	return Out(w, asJSON, strings.Join(lines, "\n"), cats)
}
