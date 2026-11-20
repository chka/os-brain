package brain

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// `open` answers "what is outstanding?", asked in those words or any of a dozen others,
// which is the question the brain was worst at. A session asked it once read the brief,
// distrusted it, and re-derived the answer from sixty commit subjects, a TODO file of
// nearly two thousand lines, a listing of the specs directory and a query against
// production. Its own account of why is the specification for this command:
//
//   "the brain tells me what was believed when someone wrote it, and I have no cheap way
//    to tell which of that is still true, so I re-derive from the code. [...] grep is the
//    reflex, the brain is an instruction I have to consciously override the reflex with,
//    and against a vague question the reflex wins because it gives me something to do
//    immediately."
//
// Three defects, answered together. There was no query that meant "what is unfinished",
// so the answer needed synthesis and synthesis needs sources. A memory records what was
// BELIEVED when it was written, never whether the work finished, so a session that finds
// one wrong learns to check them all. And nothing told a session what verification costs
// or where to stop, so it verified everything.
//
// So: assemble the three things that do carry state (open tasks, the memories recent
// sessions wrote about what they were mid-way through, and what actually happened), stamp
// every perishable claim with how far the tree has moved under it, and end with the
// budget. Not "trust this", which nothing has earned: verify the one or two claims you are
// about to act on, then WRITE DOWN what you found so the next session does not pay again.
// A memory that says "NOTHING IMPLEMENTED YET" about work that shipped weeks ago stays
// wrong forever if every session that discovers it merely routes around it.

type OpenArgs struct {
	N       int
	Full    bool
	Compact bool
	JSON    bool
}

// OpenState is what the brain can say about unfinished work, rendered by RenderOpen.
type OpenState struct {
	Project  string    `json:"project"`
	Tasks    []*Task   `json:"tasks"`
	InFlight []*Memory `json:"in_flight"`
	Recent   []Event   `json:"recent"`
	// Compact is the hook's form: it fires on a message rather than on a command, so it
	// truncates harder still. Full is the opposite end and prints task bodies, which the
	// default does not: the default is meant to be the CHEAP answer, and a handful of open
	// tasks can carry kilobytes of body between them. `os-brain task list` prints them in
	// full.
	Full    bool `json:"-"`
	Compact bool `json:"-"`
}

const (
	openDefaultN       = 6
	openBodyChars      = 200
	openCompactChars   = 120
	openFullMultiplier = 4
)

func (s *Store) Open(w io.Writer, a OpenArgs) error {
	st, err := s.OpenState(a)
	if err != nil {
		return err
	}
	return Out(w, a.JSON, RenderOpen(st), st)
}

func (s *Store) OpenState(a OpenArgs) (*OpenState, error) {
	if a.N == 0 {
		a.N = openDefaultN
	}
	st := &OpenState{Project: s.Env.Project, Full: a.Full, Compact: a.Compact}

	if s.tableExists("tasks") {
		rows, err := s.tasks(TaskLiveSQL)
		if err != nil {
			return nil, err
		}
		for _, t := range rows {
			st.Tasks = append(st.Tasks, &t.Task)
		}
	}

	inFlight, err := s.inFlight(a.N)
	if err != nil {
		return nil, err
	}
	st.InFlight = inFlight

	evs, err := s.recentEvents(a.N)
	if err != nil {
		return nil, err
	}
	st.Recent = evs
	return st, nil
}

// inFlight reads the newest state and commitment memories: what a session was in the
// middle of, and what somebody undertook to do. Every other type answers "what is true",
// which is the brief's job and not this one's.
//
// Ordered by when the CLAIM is dated, not by when the row was last touched. A share, a
// migration or a rehash moves updated_at on memories nobody has thought about in months,
// and ordering by it once put a long-finished release at the top of a section headed
// "in flight".
func (s *Store) inFlight(n int) ([]*Memory, error) {
	ms, err := s.queryMemories("SELECT "+memoryCols+", 0.0 AS bm FROM memories WHERE project=? "+
		"AND superseded_by IS NULL AND mem_type IN ('state','commitment') "+
		"ORDER BY COALESCE(NULLIF(source_date,''), NULLIF(happened_on,''), "+
		"date(created_at,'unixepoch')) DESC, updated_at DESC LIMIT ?", s.Env.Project, n)
	if err != nil {
		return nil, err
	}
	s.StampFreshness(ms)
	return ms, nil
}

// RenderOpen is the single implementation, called by `open` and by the prompt hook. A
// second copy that disagreed about what counts as open would be worse than none.
func RenderOpen(o *OpenState) string {
	b := []string{fmt.Sprintf("## WHAT IS OPEN — %s, answered from the brain at %s", o.Project,
		time.Now().Format("2006-01-02 15:04"))}
	b = append(b, openTaskLines(o)...)
	if len(o.InFlight) > 0 {
		b = append(b, "", "IN FLIGHT — what recent sessions said they were in the middle of")
		for _, m := range o.InFlight {
			b = append(b, indent(renderOpenMem(m, o.Compact, o.Full), "  "))
		}
	}
	if len(o.Recent) > 0 {
		b = append(b, "", "WHAT ACTUALLY HAPPENED")
		chars := openBodyChars
		if o.Compact {
			chars = openCompactChars
		}
		for _, e := range o.Recent {
			b = append(b, fmt.Sprintf("  %s [%s] %s", ISO(e.TS), OneLine(e.Kind),
				clipRunes(oneLine(e.Summary), chars)))
		}
	}
	// The budget. Without it the sections above are just more input to a session that has
	// already decided to go and check, and checking is unbounded by default.
	b = append(b, "",
		"THAT IS THE ANSWER. Re-deriving it from git log, a TODO file or the file tree costs",
		"thousands of tokens and mostly re-confirms the above. Verify only what you are about",
		"to ACT on — then record what you found, so the next session does not pay for it again:",
		"  os-brain verify <id>                                        it still holds",
		"  os-brain remember \"<what is true now>\" --supersedes <id>     it does not",
		"  os-brain task done <id>                                     it shipped")
	return strings.Join(b, "\n")
}

// openTaskLines lists the open tasks, or explains why their absence makes the answer thin.
func openTaskLines(o *OpenState) []string {
	if len(o.Tasks) == 0 {
		return []string{"",
			"OPEN TASKS — none filed, which is why this answer is thinner than it looks.",
			"  A memory records what was BELIEVED when it was written, never whether the work",
			"  finished, so nothing here becomes a task list on its own. File what is unfinished:",
			"    os-brain task add \"<what is unfinished>\" --owner agent --when \"<when it is due>\""}
	}
	hint := ""
	if !o.Full {
		hint = "  — bodies in `os-brain task list` or `os-brain open --full`"
	}
	lines := []string{"", fmt.Sprintf("OPEN TASKS (%d)%s", len(o.Tasks), hint)}
	for _, t := range o.Tasks {
		line := RenderTask(t, t.DueAt != nil && *t.DueAt <= Now())
		if !o.Full {
			line = strings.SplitN(line, "\n", 2)[0]
		}
		lines = append(lines, indent(line, "  "))
	}
	return lines
}

func renderOpenMem(m *Memory, compact, full bool) string {
	line := fmt.Sprintf("#%d [%s] %s", m.ID, m.MemType, OneLine(m.Title))
	var tags []string
	if d := m.SourceDate; d != "" {
		tags = append(tags, OneLine(d))
	} else if m.HappenedOn != "" {
		tags = append(tags, OneLine(m.HappenedOn))
	}
	if m.Fresh != nil && !m.Fresh.Unchanged {
		tags = append(tags, m.Fresh.Note)
	}
	if len(tags) > 0 {
		line += "  (" + strings.Join(tags, " · ") + ")"
	}
	if body := oneLine(m.Body); body != "" {
		chars := openBodyChars
		switch {
		case compact:
			chars = openCompactChars
		case full:
			chars = openFullMultiplier * openBodyChars
		}
		line += "\n    " + clipRunes(body, chars)
	}
	return line
}

// clipRunes cuts s to n characters, marking the cut with an ellipsis.
func clipRunes(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return strings.TrimRight(string([]rune(s)[:n]), " ") + "…"
}

// oneLine collapses all whitespace, newlines included, to single spaces.
func oneLine(s string) string { return OneLine(strings.Join(strings.Fields(s), " ")) }

// openQuestionRE are the ways a person asks "what is unfinished". Written from the
// question that started this ("what's outstanding?") and widened to the phrasings that
// mean the same thing, because a session whose brain answers one wording and stays silent
// on the next learns not to expect an answer at all.
//
// Wider than the preference cues are, and deliberately so: the cost of a false positive
// here is fifteen lines of this project's own open work injected into a turn that did not
// ask for it, while the cost of a miss is the whole failure this command exists to stop.
// Greek because a brain is used in the language its owner works in.
var openQuestionRE = regexp.MustCompile(`(?i)` + strings.Join([]string{
	`\bwhat(?:'?s| is| are| was)?\b[^.?!]{0,30}\b(?:outstanding|unfinished|still open|left to do|left over|remaining|pending|not done|blocked)\b`,
	`\bwhat(?:'?s| is)?\s+(?:the\s+)?(?:next|left|open|remaining|status)\b`,
	`\bwhat(?:'?s| is| are)?\b[^.?!]{0,20}\b(?:on the (?:list|roadmap)|to do|todo|still to)\b`,
	`\bwhat (?:needs|is there) (?:doing|to do|left)\b`,
	`\bwhat should (?:i|we|you) (?:do|work on)\b`,
	`\banything (?:outstanding|left|pending|still open)\b`,
	`\bwhere (?:did we|do we|are we|were we)\b[^.?!]{0,20}\b(?:leave off|left off|stand|up to|at)\b`,
	`\bwhere (?:do|does) (?:things|it|this|the project) stand\b`,
	`\bcatch me up\b`,
	`τι εκκρεμ`,
	`τι (?:μένει|έμεινε|απομένει)`,
	`(?:πού|που) (?:είμαστε|είχαμε μείνει|μείναμε)`,
	`τι (?:πρέπει να )?κάνουμε (?:μετά|τώρα)`,
}, "|"))

// IsOpenQuestion reports whether a prompt asks what is unfinished, so the prompt hook can
// answer it from the brain before the session goes digging.
func IsOpenQuestion(prompt string) bool { return openQuestionRE.MatchString(prompt) }

// OpenBlock is the hook form: compact, and empty rather than misleading when something
// goes wrong. It never fails the turn: a hook that breaks a message is worse than a hook
// that says nothing.
func (s *Store) OpenBlock(n int) string {
	st, err := s.OpenState(OpenArgs{N: n, Compact: true})
	if err != nil {
		return ""
	}
	return RenderOpen(st)
}
