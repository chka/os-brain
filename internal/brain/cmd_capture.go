package brain

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"
)

// Capture: what the user said, verbatim, before the turn is answered.
//
// The rule this file exists to keep is an asymmetry. Storing a one-off AS a rule silently
// rewrites every future session and is indistinguishable afterwards from a rule the user
// actually stated, while missing a rule costs one restatement that the ladder catches on
// the second telling. So Classify is written to fail toward "not a rule" at every decision
// point, and the classes that DO carry weight (directive, taste) are reached only through
// narrow, literal cues.
//
// EXHIBIT is checked first, ahead of directive and taste, for the same reason: a pasted
// transcript containing somebody else's "always do X" must never be read as the user's
// own standing rule. Inventing a ruling from a third party's words is the worse direction,
// so an unclear boundary between "the user said this" and "the user showed me this"
// resolves to exhibit.

// utteranceClasses are the four outcomes of Classify, in the order --class names them.
var utteranceClasses = []string{"command", "taste", "directive", "exhibit"}

// Classification is what the classifier decided and why, so a session (or a later reader)
// can see the cue rather than trusting the label.
type Classification struct {
	Class string `json:"class"` // command | taste | directive | exhibit
	Why   string `json:"why"`
	Lang  string `json:"lang"` // en | other
}

// CaptureResult is what a hook gets back. Stored is false when nothing was written, which
// is not an error: an empty prompt, a credential, or a store that would not take it.
// Capture must never fail the user's turn.
type CaptureResult struct {
	ID      int64  `json:"id,omitempty"`
	Class   string `json:"class"`
	Why     string `json:"class_why"`
	Lang    string `json:"lang"`
	Stored  bool   `json:"stored"`
	Refused string `json:"refused,omitempty"`
}

// exhibitPhrase matches an explicit frame ("for example", "this is session 1") or an
// attribution to someone other than the user ("a friend wrote", "someone said").
var exhibitPhrase = regexp.MustCompile(`(?i)this is session\s*\d+|for example|` +
	`(a friend|someone|another agent)\s+(wrote|said)`)

// directiveCue is deliberately narrow and lexical, not semantic: a wider net would catch
// ordinary descriptions of what already happens and mint a standing rule from them.
var directiveCue = regexp.MustCompile(`(?i)\bi want you to\b|\bfrom now on\b|\balways\b|` +
	`\bnever\b|\beach time\b|\bevery time\b|\bwhenever\b|\bby default\b|\bmake sure you\b`)

// tasteCue: a preference, not an instruction. "I like red" is not "push after each
// change" even though both are short first-person statements.
var tasteCue = regexp.MustCompile(`(?i)\bi like\b|\bi love\b|\bi hate\b|\bi prefer\b|` +
	`\bi can'?t stand\b|\bi cannot stand\b`)

// Classify sorts an utterance into one of the four classes. It is pure so it can be
// tested without a store.
func Classify(text string) Classification {
	t := strings.TrimSpace(text)
	lang := detectLang(t)
	// Inference is English-only by design: a standing rule phrased in another language is
	// not recognised at capture, only later, when it is restated and the ladder picks it
	// up. Classifying it here anyway would be a guess dressed up as detection, and the
	// language counter in Utterances exists to make that failure visible rather than
	// silently patched over with a half-tuned matcher.
	if lang != "en" {
		return Classification{Class: "command", Why: "no classification attempted: not English", Lang: lang}
	}
	if why := exhibitCue(t); why != "" {
		return Classification{Class: "exhibit", Why: why, Lang: lang}
	}
	if m := directiveCue.FindString(t); m != "" {
		return Classification{Class: "directive", Why: "cue:" + strings.ToLower(m), Lang: lang}
	}
	if m := tasteCue.FindString(t); m != "" {
		return Classification{Class: "taste", Why: "cue:" + strings.ToLower(m), Lang: lang}
	}
	return Classification{Class: "command", Why: "no cue matched", Lang: lang}
}

// exhibitCue names the cue that marks t as something shown rather than said, or returns
// "" when there is none: a fenced code block, a quoted line, or an explicit frame.
func exhibitCue(t string) string {
	if strings.Contains(t, "```") {
		return "cue:fenced code block"
	}
	for _, line := range strings.Split(t, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), ">") {
			return "cue:quoted line"
		}
	}
	if m := exhibitPhrase.FindString(t); m != "" {
		return "cue:" + strings.ToLower(m)
	}
	return ""
}

// detectLang is a script count, not a language model: enough to tell English-alphabet
// text from everything else, which is all classification needs. Text with no letters at
// all (a bare command, a path) counts as "en": there is nothing to classify away from,
// and the cue matchers will not fire on it regardless.
func detectLang(t string) string {
	var latin, other int
	for _, r := range t {
		if !unicode.IsLetter(r) {
			continue
		}
		if unicode.Is(unicode.Latin, r) {
			latin++
		} else {
			other++
		}
	}
	if other > latin {
		return "other"
	}
	return "en"
}

// captureMaxRunes clips before storing. A pasted log captured verbatim is still evidence,
// but an unbounded row is how one paste dominates every later scan of utterances; 8000
// runes keeps a whole paragraph or a short paste intact.
const captureMaxRunes = 8000

// Capture stores the utterance, classifies it, and returns a result the caller can always
// use. A credential refusal or an empty prompt comes back as Stored:false with a nil
// error; a database failure comes back as Stored:false plus an error the caller is free
// to ignore, because a hook must never fail the user's turn.
func (s *Store) Capture(text, session string) (*CaptureResult, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return &CaptureResult{Stored: false}, nil
	}
	// Credential-shaped text is refused on every write path in this store, and capture is
	// not an exception just because it runs on every turn rather than on request. There is
	// no --allow-secret escape here: nobody is on the other end of a hook to ask, so "not
	// stored" is the only safe default.
	if err := GuardTextNoOverride(trimmed); err != nil {
		return &CaptureResult{Stored: false, Refused: err.Error()}, nil
	}

	clipped := Clip(trimmed, captureMaxRunes)
	cls := Classify(trimmed)
	if session == "" {
		session = s.Env.SessionID()
	}
	res, err := s.DB.Exec(
		"INSERT INTO utterances(project, ts, text, class, class_why, lang, session) "+
			"VALUES(?,?,?,?,?,?,?)",
		s.Env.Project, Now(), clipped, cls.Class, cls.Why, cls.Lang, session)
	if err != nil {
		// Utterances are never deduped, so this is a real database failure (schema or IO),
		// never a unique-constraint collision.
		return &CaptureResult{Class: cls.Class, Why: cls.Why, Lang: cls.Lang, Stored: false}, err
	}
	id, _ := res.LastInsertId()
	s.expireUtterances()
	return &CaptureResult{
		ID: id, Class: cls.Class, Why: cls.Why, Lang: cls.Lang, Stored: true,
	}, nil
}

// UtteranceDaysDefault is how long a prompt is kept when brain.json does not say. Every
// prompt is captured verbatim, pastes included, and the ladder only ever reads recent ones;
// kept forever, the table is every log and config anybody pasted, travelling with share and
// export.
const UtteranceDaysDefault = 30

// expireUtterances drops this project's prompts past the retention, except one a memory
// was distilled from: from_utterance is that memory's provenance.
func (s *Store) expireUtterances() {
	days := s.Env.Config.UtteranceDays
	if days <= 0 {
		days = UtteranceDaysDefault
	}
	// A hundred years: large enough to mean "keep", small enough that the cutoff below
	// cannot overflow.
	days = min(days, 36500)
	_, _ = s.DB.Exec("DELETE FROM utterances WHERE project=? AND ts < ? AND id NOT IN "+
		"(SELECT from_utterance FROM memories WHERE from_utterance IS NOT NULL)",
		s.Env.Project, Now()-int64(days)*86400)
}

type CaptureArgs struct {
	Text    string
	Session string
	JSON    bool
}

// CaptureCmd exists mainly for the hook, and prints almost nothing. Capture runs on every
// user message: a command that announces itself on every turn is a command people mute,
// and muting this one takes the directive line with it.
func (s *Store) CaptureCmd(w io.Writer, a CaptureArgs) error {
	res, err := s.Capture(a.Text, a.Session)
	if err != nil {
		return err
	}
	human := captureLine(res)
	if human == "" && !a.JSON {
		return nil
	}
	return Out(w, a.JSON, human, res)
}

// captureLine is the human output of a capture, empty for an ordinary turn.
func captureLine(res *CaptureResult) string {
	switch {
	case res.Refused != "":
		return "not stored: " + res.Refused
	case !res.Stored:
		return "nothing to store"
	case res.Class == "directive":
		// The one class worth saying out loud: a standing rule that was heard as a rule is
		// exactly what the user wants to know was heard.
		return fmt.Sprintf("captured #%d as a directive (%s). "+
			"Say it again in another session and it becomes law.", res.ID, res.Why)
	}
	return ""
}

type UtterancesArgs struct {
	N            int
	Class        string
	Unclassified bool
	JSON         bool
}

type utteranceRow struct {
	ID      int64  `json:"id"`
	TS      int64  `json:"ts"`
	Text    string `json:"text"`
	Class   string `json:"class"`
	Why     string `json:"class_why"`
	Lang    string `json:"lang"`
	Session string `json:"session"`
}

// Utterances reads back what was said. The count of unclassified utterances is the point
// of the lang column: English-only inference fails SILENTLY, so the store has to be able
// to say how often it declined to classify, or nobody can tell whether that limit matters.
func (s *Store) Utterances(w io.Writer, a UtterancesArgs) error {
	if a.N <= 0 {
		a.N = 20
	}
	rows, err := s.recentUtterances(a)
	if err != nil {
		return err
	}
	var lines []string
	for _, r := range rows {
		tag := r.Class
		if r.Lang != "en" {
			tag += ", " + r.Lang + ": not classified"
		}
		lines = append(lines, fmt.Sprintf("#%d %s [%s] %s", r.ID, Day(r.TS), tag, OneLine(Clip(r.Text, 160))))
	}
	total, other := s.utteranceCounts()
	if len(lines) == 0 {
		lines = append(lines, "nothing captured yet")
	}
	if other > 0 {
		lines = append(lines, "", fmt.Sprintf("%d of %d were not in English and got no "+
			"classification: a standing rule phrased in another language is stored as an "+
			"ordinary utterance and only picked up when it is restated.",
			other, total))
	}
	return Out(w, a.JSON, strings.Join(lines, "\n"),
		map[string]interface{}{"utterances": rows, "total": total,
			"unclassified": other})
}

// recentUtterances is the filtered, newest-first page Utterances prints.
func (s *Store) recentUtterances(a UtterancesArgs) ([]utteranceRow, error) {
	where := []string{"project = ?"}
	args := []interface{}{s.Env.Project}
	if a.Class != "" {
		// A typo'd filter that returns nothing reads exactly like an empty store, and this
		// command's empty answer literally says "nothing captured yet". Fail closed and
		// name the values, as --type and --tier do on recall.
		if !contains(utteranceClasses, a.Class) {
			return nil, fmt.Errorf("--class is one of command, taste, directive, exhibit, not %s",
				Repr(a.Class))
		}
		where = append(where, "class = ?")
		args = append(args, a.Class)
	}
	if a.Unclassified {
		where = append(where, "lang <> 'en'")
	}
	rows, err := s.DB.Query("SELECT id, ts, text, class, class_why, lang, session FROM utterances "+
		"WHERE "+strings.Join(where, " AND ")+" ORDER BY ts DESC, id DESC LIMIT ?",
		append(args, a.N)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []utteranceRow{}
	for rows.Next() {
		var r utteranceRow
		if err := rows.Scan(&r.ID, &r.TS, &r.Text, &r.Class, &r.Why, &r.Lang, &r.Session); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// utteranceCounts is every utterance this project holds and how many of them were not
// classified, regardless of the filter the listing used.
func (s *Store) utteranceCounts() (total, other int) {
	_ = s.DB.QueryRow("SELECT COUNT(*), COALESCE(SUM(lang <> 'en'),0) FROM utterances "+
		"WHERE project=?", s.Env.Project).Scan(&total, &other)
	return total, other
}
