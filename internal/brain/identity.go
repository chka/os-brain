package brain

import (
	"fmt"
	"sort"
	"strings"
)

// IdentityCues are the words that mean "what is this project". Recall answers them from
// brain.json rather than hoping a memory happens to say so: four identity queries against
// a project's own brain once returned nothing, and the session went and read CLAUDE.md
// instead.
var IdentityCues = set("project", "projects", "repo", "repos", "repository", "codebase",
	"product", "purpose", "overview", "identity", "readme", "stack")

// IdentityCard is project identity straight from brain.json context. Never a memory row.
// Deliberately does not go through contextFacts: that shells out to git, and this runs
// inside recall.
func (s *Store) IdentityCard() map[string]string {
	card := map[string]string{"project": s.Env.Project, "root": s.Env.ProjectRoot()}
	for k, v := range s.Env.Config.Context {
		if k != "root" {
			card[k] = v
		}
	}
	return card
}

// identityQuery reports whether the query is asking what this project is. Slightly
// permissive on purpose: the card is a few lines shown ABOVE the ranked results rather
// than instead of them, so a false positive costs almost nothing while a false negative
// costs the entire bug this exists to fix.
func (s *Store) identityQuery(query string) bool {
	if query == "" {
		return false
	}
	terms := map[string]bool{}
	for _, t := range QueryTerms(query) {
		terms[strings.ToLower(t)] = true
	}
	for t := range terms {
		if IdentityCues[t] {
			return true
		}
	}
	for _, t := range QueryTerms(s.Env.Project) {
		t = strings.ToLower(t)
		if terms[t] && !IsStopword(t) {
			return true
		}
	}
	return false
}

// identityLines renders the card shown above recall's results: project and root first,
// then the brain.json context keys in sorted order.
func identityLines(card map[string]string) []string {
	lines := []string{
		"PROJECT IDENTITY — from brain.json, which is what this project IS",
		fmt.Sprintf("  project   %s at %s", card["project"], card["root"]),
	}
	var keys []string
	for k := range card {
		if k != "project" && k != "root" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		lines = append(lines, fmt.Sprintf("  %-9s %s", k, card[k]))
	}
	if len(keys) == 0 {
		lines = append(lines, "  (no description set: os-brain context set about "+
			"\"<what this project is>\")")
	}
	return lines
}
