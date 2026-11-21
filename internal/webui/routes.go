package webui

// The page's data routes. Each one runs CLI commands through call() and reshapes their JSON
// for the page; none of them queries the store itself. Counting rows for a filter chip is
// the most any of them computes.

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

type uiRoute func(*url.URL) (any, error)

func uiRoutes() map[string]uiRoute {
	return map[string]uiRoute{
		"/api/brief":    uiBrief,
		"/api/memories": uiMemories,
		"/api/tree":     uiTree,
		"/api/leads":    uiLeads,
		"/api/events":   uiEvents,
		"/api/measures": uiMeasures,
		"/api/stats":    func(*url.URL) (any, error) { return callMap("stats") },
		"/api/map":      uiMap,
		"/api/meta":     uiMeta,
	}
}

func uiBrief(*url.URL) (any, error) {
	// --no-cursor matters: opening a web page must not consume the "what is new since I
	// last looked" marker that the next session is going to read.
	brief, err := callMap("brief", "--no-cursor")
	if err != nil {
		return nil, err
	}
	stats, err := callMap("stats")
	if err != nil {
		return nil, err
	}
	brief["stats"] = stats
	return brief, nil
}

func uiMemories(u *url.URL) (any, error) {
	// --no-touch: looking at a memory on a page is not a session using it, and must not
	// count as one.
	args := []string{"recall", "--no-touch", "--browse", "-n", clampN(qs(u, "n"), 200, 1000)}
	if v := qs(u, "scope"); v != "" {
		args = append(args, "--scope", v)
	}
	if v := qs(u, "type"); v != "" {
		args = append(args, "--type", v)
	}
	if u.Query().Get("all") == "1" {
		args = append(args, "--all")
	}
	recall, err := callMap(positional(args, qs(u, "q"))...)
	if err != nil {
		return nil, err
	}
	m, err := callMap("map")
	if err != nil {
		return nil, err
	}
	scopes := []map[string]any{}
	for _, s := range asList(m["scopes"]) {
		r, _ := s.(map[string]any)
		scopes = append(scopes, map[string]any{"scope": r["scope"], "c": r["count"]})
	}
	return map[string]any{
		"memories": orEmpty(recall["results"]),
		"matched":  recall["matched"],
		"scopes":   scopes,
		"types":    orEmpty(m["types"]),
	}, nil
}

func uiTree(*url.URL) (any, error) {
	recall, err := callMap("recall", "--no-touch", "--browse", "-n", "2000")
	if err != nil {
		return nil, err
	}
	m, err := callMap("map")
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"memories":   orEmpty(recall["results"]),
		"declared":   orEmptyMap(m["declared"]),
		"tombstones": orEmpty(m["tombstones"]),
	}, nil
}

func uiLeads(u *url.URL) (any, error) {
	args := []string{"lead", "list", "-n", clampN(qs(u, "n"), 500, 1000)}
	if v := qs(u, "status"); v != "" {
		args = append(args, "--status", v)
	}
	if v := qs(u, "segment"); v != "" {
		args = append(args, "--segment", v)
	}
	filtered, err := callList(args...)
	if err != nil {
		return nil, err
	}
	// The chips count every lead, not just the filtered ones, so a chip does not vanish
	// the moment it is picked.
	every, err := callList("lead", "list", "-n", "500")
	if err != nil {
		return nil, err
	}
	if q := strings.ToLower(qs(u, "q")); q != "" {
		filtered = leadsMatching(filtered, q)
	}
	return map[string]any{"leads": filtered,
		"statuses": tally(every, "status"), "segments": tally(every, "segment")}, nil
}

// leadsMatching keeps the leads whose text fields contain q (already lowercased). The lead
// command has no free-text search, so this filter is the one the page offers.
func leadsMatching(leads []any, q string) []any {
	kept := []any{}
	for _, l := range leads {
		m, _ := l.(map[string]any)
		hay := strings.ToLower(fmt.Sprint(m["name"], " ", m["company"], " ",
			m["role"], " ", m["notes"], " ", m["next_action"]))
		if strings.Contains(hay, q) {
			kept = append(kept, l)
		}
	}
	return kept
}

func uiEvents(u *url.URL) (any, error) {
	args := []string{"events", "-n", clampN(qs(u, "n"), 300, 1000)}
	if v := qs(u, "kind"); v != "" {
		args = append(args, "--kind", v)
	}
	if v := qs(u, "scope"); v != "" {
		args = append(args, "--scope", v)
	}
	events, err := callList(positional(args, qs(u, "q"))...)
	if err != nil {
		return nil, err
	}
	every, err := callList("events", "-n", "1000")
	if err != nil {
		return nil, err
	}
	return map[string]any{"events": events,
		"kinds": tally(every, "kind"), "scopes": tally(every, "scope")}, nil
}

// uiMeasures lists every measure and, when a key is picked, that measure's series.
func uiMeasures(u *url.URL) (any, error) {
	listArgs := []string{"measure", "list"}
	scope := qs(u, "scope")
	if scope != "" {
		listArgs = append(listArgs, "--scope", scope)
	}
	list, err := callMap(listArgs...)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"measures": orEmpty(list["measures"]),
		"count": list["count"], "series": nil}
	key := qs(u, "key")
	if key == "" {
		return out, nil
	}
	args := []string{"measure", "series"}
	if scope != "" {
		args = append(args, "--scope", scope)
	}
	if v := qs(u, "from"); v != "" {
		args = append(args, "--from", v)
	}
	if v := qs(u, "to"); v != "" {
		args = append(args, "--to", v)
	}
	// A bad date makes the CLI exit non-zero; let that surface rather than rendering an
	// empty series, which reads as "no readings" instead of "your filter is wrong".
	series, err := callMap(positional(args, key)...)
	if err != nil {
		return nil, err
	}
	out["series"] = series
	return out, nil
}

func uiMap(*url.URL) (any, error) {
	m, err := callMap("map")
	if err != nil {
		return nil, err
	}
	if m["declared"] == nil {
		m["declared"] = map[string]any{}
	}
	if m["declaredKinds"] == nil {
		m["declaredKinds"] = map[string]any{}
	}
	counts, _ := m["counts"].(map[string]any)
	if counts != nil {
		m["superseded"] = counts["superseded"]
	}
	return m, nil
}

// uiMeta says which brain this page is looking at, in the page itself.
func uiMeta(*url.URL) (any, error) {
	env, err := callMap("env")
	if err != nil {
		return nil, err
	}
	return map[string]any{"project": env["project"], "db": env["db"],
		"edition": env["edition"], "machine": env["machine"], "now": env["now"]}, nil
}

// uiForget is the page's one write. It runs `forget` like everything else here, so the
// pinned, guardrail and chain-head refusals arrive in the command's own words.
func uiForget(body map[string]any) (any, error) {
	id, _ := body["id"].(float64)
	if id < 1 {
		return nil, fmt.Errorf("forget needs a memory id")
	}
	reason, _ := body["reason"].(string)
	reason = strings.TrimSpace(reason)
	// The CLI accepts an empty reason. The UI does not: a tombstone with a blank reason
	// tells the next session a fact was deleted and nothing about whether to believe it.
	if reason == "" {
		return nil, fmt.Errorf("say why: the reason is what the tombstone carries")
	}
	args := []string{"forget", strconv.FormatInt(int64(id), 10), "--reason", reason}
	if force, _ := body["force"].(bool); force {
		args = append(args, "--force")
	}
	return callMap(args...)
}

// positional appends the page's text behind "--", after every flag, so a query of
// `--all-projects` is searched for rather than obeyed.
func positional(args []string, text ...string) []string {
	var words []string
	for _, t := range text {
		if t != "" {
			words = append(words, t)
		}
	}
	if len(words) == 0 {
		return args
	}
	return append(append(args, "--"), words...)
}

func qs(u *url.URL, k string) string { return strings.TrimSpace(u.Query().Get(k)) }

// clampN reads a row count from the page, defaulting a missing or bad one and capping it,
// so a hand-edited URL cannot ask the CLI for every row in the store.
func clampN(v string, dflt, max int) string {
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		n = dflt
	}
	if n > max {
		n = max
	}
	return strconv.Itoa(n)
}

// tally counts a field across rows, for the filter chips, in first-seen order.
func tally(rows []any, key string) []map[string]any {
	order := []string{}
	seen := map[string]map[string]any{}
	for _, r := range rows {
		m, _ := r.(map[string]any)
		v, _ := m[key].(string)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; !ok {
			seen[v] = map[string]any{key: v, "c": 0}
			order = append(order, v)
		}
		seen[v]["c"] = seen[v]["c"].(int) + 1
	}
	out := make([]map[string]any, 0, len(order))
	for _, k := range order {
		out = append(out, seen[k])
	}
	return out
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// orEmpty and orEmptyMap turn a missing value into an empty one, so the page can iterate
// without a null check on every field.
func orEmpty(v any) any {
	if v == nil {
		return []any{}
	}
	return v
}

func orEmptyMap(v any) any {
	if v == nil {
		return map[string]any{}
	}
	return v
}
