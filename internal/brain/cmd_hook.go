package brain

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// hookPrefixMinRunes is how long a word must be before the hook will prefix-match it.
// Five keeps "deploy*", "vault*" and "nginx*" working while stopping "tea*" from finding
// "team".
const hookPrefixMinRunes = 5

// nudgeCooldown is the seconds between two preference nudges in one session: a reminder on
// every message is nagging, not a mechanism.
const nudgeCooldown = 900

// preferenceRE matches phrases that mean "this is how I want you to work", not "here is a
// fact". Deliberately narrow: a nudge that fires on ordinary technical prose gets ignored,
// and then the real ones get ignored with it. Greek is included because a brain is used in
// the language its owner works in.
var preferenceRE = regexp.MustCompile(`(?i)` + strings.Join([]string{
	`\bi (?:would )?(?:prefer|hate|like|want|expect|always|never)\b`,
	`\b(?:from now on|going forward|in future|next time)\b`,
	`\b(?:please )?(?:don'?t|do not|never|stop) (?:ever )?(?:do|say|use|add|write|ask|call|start|keep|include|suggest)\b`,
	`\byou (?:should|must|need to) (?:always|never|stop|avoid)\b`,
	`\byou (?:always|never) (?:ask|tell|say|add|use|write|suggest|start)\b`,
	`\bstop (?:doing|saying|using|adding|asking|writing)\b`,
	`\b(?:my|our) (?:style|preference|convention|rule) is\b`,
	`προτιμ(?:ώ|άω)`,
	// Go's \b is ASCII-only, so `\b(?:μη|μην)` would never fire: the boundary is tested
	// against μ, which RE2 does not consider a word character. Every Greek cue therefore
	// uses an explicit Unicode-aware left boundary.
	`(?:^|[^\p{L}\p{N}])(?:μη|μην) (?:μου )?\S+`,
	`πάντα να`,
	`ποτέ (?:μη|να)`,
	`από (?:εδώ|δω) και (?:πέρα|μπρος)`,
	`σταμάτα`,
	`(?:δεν )?μου αρέσει`,
	`θέλω να`,
}, "|"))

type HookRecallArgs struct {
	N         int
	Chars     int
	MinChars  int
	MinScore  float64
	Scope     string
	IdleAfter int
	NoClock   bool
	NoNudge   bool
}

// withDefaults fills what the hook command left unset. The values are tuned: a hook that
// surfaces a different set of memories, or truncates them differently, changes every turn.
func (a HookRecallArgs) withDefaults() HookRecallArgs {
	if a.N == 0 {
		a.N = 3
	}
	if a.Chars == 0 {
		a.Chars = 180
	}
	if a.MinChars == 0 {
		a.MinChars = 15
	}
	if a.MinScore == 0 {
		a.MinScore = 0.55
	}
	if a.IdleAfter == 0 {
		a.IdleAfter = 20
	}
	return a
}

// hookContextBlock is what Gemini CLI prepends to the prompt it hands BeforeAgent: every
// additionalContext injected so far, the whole brief included.
var hookContextBlock = regexp.MustCompile(`(?s)<hook_context>.*?</hook_context>`)

// HookPromptText is what the user typed, without context a host folded into the prompt
// field. Matched against the brief, recall answered the brief's own words: it injected the
// "what is open" block because the brief says "outstanding" and "next", and Capture read
// the brief's lines as rules the user had just stated.
func HookPromptText(p string) string {
	return strings.TrimSpace(hookContextBlock.ReplaceAllString(p, ""))
}

type hookPayload struct {
	Prompt string `json:"prompt"`
}

// HookRecall is the UserPromptSubmit hook: recall from the user's own words, because the
// facts you need most are the ones you would not know to search for. It reads the hook
// JSON on stdin, prints a short block only when the match is strong, and never
// reinforces: counting an injection as a use would promote whatever the hook guessed.
func (s *Store) HookRecall(stdin io.Reader, w io.Writer, a HookRecallArgs) error {
	a = a.withDefaults()
	var payload hookPayload
	if err := json.NewDecoder(stdin).Decode(&payload); err != nil {
		// A malformed payload is not this hook's problem to report: it runs on every
		// message and must never break the turn.
		return nil
	}
	prompt := trim(HookPromptText(payload.Prompt))

	if !a.NoClock {
		if err := s.printPromptClock(w, a.IdleAfter); err != nil {
			return err
		}
	}

	// Learn first, debate second. What the user said is stored BEFORE the turn is
	// answered, and this hook is the only place that can promise it: everything else
	// depends on the model choosing to write something down, which is the failure this
	// whole design exists to remove. It runs before the length gate and before retrieval,
	// because a short message can still be a standing rule, and it never blocks: a
	// refusal or a database error leaves the turn alone.
	captured, _ := s.Capture(prompt, "")

	// The heartbeat. This hook already runs on every user message, which is what makes a
	// reminder reach a session that is ALREADY OPEN without anything pushing: the user
	// typing is what collects it. Before the length gate, because whether something is
	// due has nothing to do with how long their message was.
	if block := s.DueBlock(true); block != "" {
		fmt.Fprintln(w, block)
	}

	// "What's outstanding?" is the question the brain was worst at, and one a session
	// answered by re-deriving the whole project from git log and a long TODO file. It has
	// an answer here, so give it here, unasked: an instruction to run a command is
	// something a session must remember to override its reflex with, and against a vague
	// question the reflex wins. Before the length gate, because "what's next?" is twelve
	// characters and is exactly the question this is for.
	if IsOpenQuestion(prompt) {
		if block := s.OpenBlock(4); block != "" {
			fmt.Fprintln(w, block)
		}
	}

	if len([]rune(prompt)) < a.MinChars {
		return nil
	}
	// Before the retrieval work and independent of it: whether the user just stated a
	// preference has nothing to do with whether a memory matches their words, and
	// hanging it off the recall path meant a prompt that matched nothing was also a
	// preference that went uncaptured.
	// The nudge asks for the durable thing: a stored preference. Capture already has the
	// sentence, but an utterance is raw material, not a rule. So the nudge speaks first
	// when it has something to say, and capture's line fills the silence the rate limit
	// leaves - one line about one sentence, never two.
	if !s.maybeNudgePreference(w, a, prompt) && captured != nil && captured.Class == "directive" {
		fmt.Fprintf(w, "[brain] heard as a standing rule (%s). It is captured; say it again "+
			"in another session and it becomes law.\n", captured.Why)
	}

	fq, err := s.discriminatingQuery(prompt)
	if err != nil || fq == "" {
		return err
	}
	if matches := s.hookMatches(fq, a); len(matches) > 0 {
		s.printHookMatches(w, matches, a.Chars)
	}
	return nil
}

// printPromptClock prints the date and time, adds how long it has been since the last
// message when that is long enough to matter, then records this message's time. The time is
// kept per project and per session: the shared store's meta table is every project's, so
// one bare key reported the gap since whichever project was typed in last.
func (s *Store) printPromptClock(w io.Writer, idleAfterMinutes int) error {
	now := time.Now()
	mine, project := s.projectKey(s.sessionKey("last_prompt_at")), s.projectKey("last_prompt_at")
	last := s.metaInt(mine)
	if last == 0 {
		last = s.metaInt(project)
	}
	line := fmt.Sprintf("[brain] %s (%s %s)", now.Format("2006-01-02 15:04 MST"),
		now.Format("Monday"), DayPart(now.Hour()))
	if last > 0 {
		if gap := now.Unix() - last; gap >= int64(idleAfterMinutes)*60 {
			line += " · " + strings.TrimSuffix(HumanGap(gap), " ago") +
				" since the last message, so the clock has moved"
		}
	}
	fmt.Fprintln(w, line)
	for _, k := range []string{mine, project} {
		if _, err := s.DB.Exec("INSERT OR REPLACE INTO meta(k,v) VALUES(?,?)", k,
			strconv.FormatInt(now.Unix(), 10)); err != nil {
			return err
		}
	}
	return nil
}

// hookMatches is the live memories of this project that match the query strongly enough to
// volunteer, best first, at most a.N. A failed query is an empty answer: the hook never
// breaks the turn.
func (s *Store) hookMatches(fq string, a HookRecallArgs) []*Memory {
	where, params := "m.superseded_by IS NULL", []interface{}{fq}
	if a.Scope != "" {
		chain := ScopeAncestors(a.Scope)
		where += " AND m.scope IN (" + placeholders(len(chain)) + ")"
		params = append(params, toIfaces(chain)...)
	}
	// Never volunteer a memory the project marked private into the model's context.
	for _, pat := range s.hookExclude() {
		where += " AND m.scope NOT GLOB ?"
		params = append(params, pat)
	}
	where += " AND m.project = ?"
	params = append(params, s.Env.Project)

	now := Now()
	rows, err := s.queryMemories(fmt.Sprintf("SELECT %s, bm25(mem_fts) AS bm FROM mem_fts "+
		"JOIN memories m ON m.id = mem_fts.rowid WHERE mem_fts MATCH ? AND %s",
		prefixed(memoryCols, "m"), where), params...)
	if err != nil {
		return nil
	}

	tiny := s.isTinyBrain()
	var scored []*Memory
	for _, r := range rows {
		// No pin boost, for the same reason as recall: a pin says "read by default",
		// which the brief already satisfies. Here it only ever promoted memories the
		// user did not ask about over the one they did.
		r.Score = ScoreMem(RelevanceFromBM25(r.BM), r.MemType, r.UpdatedAt, now,
			r.UseCount, r.Importance, r.Confidence)
		if tiny || r.Score >= a.MinScore {
			scored = append(scored, r)
		}
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	if len(scored) > a.N {
		scored = scored[:a.N]
	}
	return scored
}

// isTinyBrain reports a project with fewer than three live memories. FTS5's bm25 floors the
// IDF of a term held by half the rows or more at almost nothing, and in a brain of one or
// two memories every term is, so no match could reach the score floor and a brand-new brain
// never recalled anything on a prompt. Below three rows the discriminating terms matching
// IS the evidence, and the most that can arrive is two lines.
func (s *Store) isTinyBrain() bool {
	total, err := s.count("SELECT COUNT(*) FROM memories WHERE project=? AND "+
		"superseded_by IS NULL", s.Env.Project)
	return err == nil && total < 3
}

// printHookMatches is the block the session reads. Its header names the project and says
// the brain is a local file: it is the one line a session sees on EVERY message, so it is
// the only place that answer is repeated after the brief has scrolled away or been
// summarised out.
func (s *Store) printHookMatches(w io.Writer, matches []*Memory, maxChars int) {
	fmt.Fprintf(w, "[%s brain · local file] possibly relevant memories (os-brain get <id> "+
		"for the full text):\n", OneLine(s.Env.Project))
	for _, r := range matches {
		body := oneLine(r.Body)
		if len([]rune(body)) > maxChars {
			body = strings.TrimRight(string([]rune(body)[:maxChars]), " ") + "…"
		}
		conf := ""
		if r.Confidence != DefaultConfidence {
			conf = " [" + r.Confidence + "]"
		}
		line := fmt.Sprintf("  #%d [%s/%s]%s %s", r.ID, OneLine(r.Scope), r.MemType, conf, OneLine(r.Title))
		if body != "" {
			line += " — " + body
		}
		fmt.Fprintln(w, line)
	}
}

func (s *Store) hookExclude() []string {
	if len(s.Env.Config.HookExclude) > 0 {
		return s.Env.Config.HookExclude
	}
	// Defaults, not a config requirement: a project that has never thought about this
	// still must not leak a scope called private into the model's context.
	return []string{"private", "private:*", "lead:*"}
}

// discriminatingQuery is the prompt's terms, minus the ones that match most of the brain
// anyway. The hook fires on every user message, so a false positive is noise in every
// turn and teaches people to skip the block: "what is the weather like today" matched on
// 'the' and 'is' and injected three unrelated memories.
//
// bm25 cannot be the filter here: a term in half the memories has zero IDF and scores
// the same as no match at all, which is right for ranking and useless below about ten
// memories. Document frequency is corpus-size independent.
//
// Frequency alone is not enough either, and the failure runs the wrong way: a small
// brain of terse technical prose contains "project" far more often than "what" or
// "does", so a DF gate on its own dropped the only topical term and kept the filler.
// Class removes function words first; DF then does what it is good at, which is dropping
// domain terms this particular brain says constantly.
func (s *Store) discriminatingQuery(prompt string) (string, error) {
	terms := hookTerms(prompt)
	if len(terms) == 0 {
		return "", nil
	}
	total, err := s.count("SELECT COUNT(*) FROM memories WHERE project=? AND "+
		"superseded_by IS NULL", s.Env.Project)
	if err != nil || total == 0 {
		return "", err
	}
	ceiling := total / 2
	if ceiling < 1 {
		ceiling = 1
	}
	var keep []string
	for _, t := range terms {
		df, err := s.count("SELECT COUNT(*) FROM mem_fts JOIN memories m ON m.id = mem_fts.rowid "+
			"WHERE mem_fts MATCH ? AND m.project=? AND m.superseded_by IS NULL", t, s.Env.Project)
		if err != nil {
			continue
		}
		if df > 0 && df <= ceiling {
			keep = append(keep, t)
		}
	}
	return strings.Join(keep, " OR "), nil
}

// hookTerms is the prompt's non-stopwords as FTS terms. Only words of hookPrefixMinRunes or
// more get a prefix wildcard. FTSQuery appends `*` so "deploy" finds "deployment", which is
// right when someone typed a query; here it fires on every message, and a three-letter
// prefix is not evidence of anything: "tea*" matched memories through "team", "back*"
// through "backup" and "backfill", and "long*" through "belongs", which is what made an
// off-topic prompt inject three memories. The short word itself is kept: an exact "tea"
// still matches a memory about tea.
func hookTerms(prompt string) []string {
	var terms []string
	for _, w := range QueryTerms(prompt) {
		if IsStopword(w) {
			continue
		}
		terms = append(terms, FTSTerm(w, len([]rune(w)) >= hookPrefixMinRunes))
	}
	return terms
}

// maybeNudgePreference fires at the moment a standing preference is expressed, which is
// the only moment anyone reliably remembers it. Rate limited, and silent if it is
// already stored. It reports whether it said anything, so the caller can avoid printing
// a second line about the same sentence: two lines telling somebody the same thing is how
// one useful line becomes wallpaper.
func (s *Store) maybeNudgePreference(w io.Writer, a HookRecallArgs, prompt string) bool {
	if a.NoNudge || !preferenceRE.MatchString(prompt) {
		return false
	}
	if Now()-s.sessionCursor("last_pref_nudge") < nudgeCooldown {
		return false
	}
	if s.preferenceAlreadyStored(prompt) {
		return false
	}
	if err := s.setSessionCursor("last_pref_nudge", Now()); err != nil {
		return false
	}
	fmt.Fprintln(w, "[brain] that reads like a standing preference, not a one-off. If it is, "+
		"store it now: os-brain prefer \"<the rule in their words>\" --why \"<what prompted it>\"")
	return true
}

// preferenceAlreadyStored asks whether a stored preference-like memory has a title close to
// the prompt. It is a similarity question: an OR query lets one stopword in an unrelated
// preference silence the nudge forever, and an AND over every content word never matches a
// rewording of the same rule.
func (s *Store) preferenceAlreadyStored(prompt string) bool {
	words := contentWords(prompt)
	if len(words) == 0 {
		return false
	}
	rows, err := s.DB.Query("SELECT title FROM memories WHERE superseded_by IS NULL AND "+
		"project=? AND mem_type IN ('preference','relationship','constraint','policy') "+
		"LIMIT 500", s.Env.Project)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			return false
		}
		if jaccard(words, contentWords(title)) >= 0.45 {
			return true
		}
	}
	return false
}

// contentWords is the set of normalised words of four letters or more.
func contentWords(text string) map[string]bool {
	out := map[string]bool{}
	for _, t := range strings.Fields(NormTitle(text)) {
		if len([]rune(t)) >= 4 {
			out[t] = true
		}
	}
	return out
}

// jaccard is the overlap of two word sets: shared words over all words, 0 when either is empty.
func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

type PreferArgs struct {
	Text       string
	Why        string
	About      string
	Importance int
	Confirmed  bool
	Supersedes int64
	Update     bool
	Source     string
	Scope      string
	Pin        bool
	JSON       bool
}

// Prefer is deliberately one short command, because friction is why this never gets
// recorded. It writes down how this person wants to be worked with, the moment they say
// it: a preference, or a relationship when it is about someone (--about).
func (s *Store) Prefer(w io.Writer, a PreferArgs) error {
	memType := "preference"
	if a.About != "" {
		memType = "relationship"
	}
	importance := a.Importance
	if importance == 0 {
		importance = 4
	}
	confidence := DefaultConfidence
	if a.Confirmed {
		confidence = "confirmed"
	}
	source := a.Source
	if source == "" {
		source = "stated by the user"
	}
	scope := a.Scope
	if scope == "" {
		scope = "global"
	}
	return s.Remember(w, RememberArgs{
		Scope:      scope,
		Type:       memType,
		Title:      a.Text,
		Body:       a.Why,
		Importance: importance,
		Confidence: confidence,
		Source:     source,
		SourceDate: time.Now().Format("2006-01-02"),
		Supersedes: a.Supersedes,
		Update:     a.Update,
		Pin:        a.Pin,
		JSON:       a.JSON,
	})
}
