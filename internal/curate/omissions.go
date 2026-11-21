package curate

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/textguard"
)

// Omissions are the checks nobody owned. `doctor` asks whether the store is INTACT and
// `review` asks whether it carries EXCESS (duplicates, over-pinning, superseded rows).
// Neither asks whether a brain is THIN, and that is not an oversight in either command:
// every guard in this codebase was written standing over wreckage, and an omission leaves
// none. Nobody files an incident titled "every memory went into the default scope and
// nothing happened", because nothing happened. So the guard list became a scar map, and
// scars only form where something was cut.
//
// Two further reasons the existing machinery could not catch it. "Fail closed on
// ambiguity" is about a command that WAS issued and whose meaning is unclear; a command
// never issued produces no ambiguity to fail closed on. And the checks that would notice
// thinness need an EXPECTATION of what a brain should hold, which reads like the
// project-specific knowledge this binary refuses to carry. The way out is to look for
// dimensions the brain has NEVER USED AT ALL, which needs no expectation: zero is zero in
// every project.
//
// Every check here therefore has the same shape: a floor, so a young brain is never
// nagged; a signal that the dimension is genuinely unused rather than lightly used; and
// the exact command that ends it. A nudge that is not actionable teaches people to ignore
// nudges.
type Omission struct {
	Key   string   `json:"key"`
	Head  string   `json:"head"`
	Lines []string `json:"lines"`
}

// OmissionFacts is what the checks need. GatherOmissions reads them from the store; the
// checks themselves are pure functions of these facts, so they can be tested without one.
type OmissionFacts struct {
	Live           int
	InDefaultScope int
	DeclaredScopes int
	Events         int
	Tasks          int
	Pinned         int
	About          string
	// UnboundRules are memories whose text reads as a standing rule but whose type does
	// not carry one into every brief.
	UnboundRules []OmissionRow
	// Dated are memories carrying a date still in the future.
	Dated []OmissionRow
}

type OmissionRow struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
	// Detail is the date for a dated row, empty otherwise.
	Detail string `json:"detail,omitempty"`
}

// OmissionFloor is the number of live memories below which nothing is reported. A brain
// with four memories has not omitted a scope table; it has not needed one yet.
const OmissionFloor = 5

// Omissions runs every check, in the order `review` prints them.
func Omissions(f OmissionFacts) []Omission {
	if f.Live < OmissionFloor {
		return nil
	}
	checks := []func(OmissionFacts) *Omission{
		unscopedOmission, unboundRulesOmission, undatedOmission,
		neverLoggedOmission, undescribedOmission,
	}
	var out []Omission
	for _, check := range checks {
		if o := check(f); o != nil {
			out = append(out, *o)
		}
	}
	return out
}

// unscopedOmission: CheckDeclared and review's UNDECLARED SCOPES are both gated on a
// scope table existing, so their advice reaches only projects that already declared one,
// the shape that needs it least. The shape that needs it most, everything defaulting to
// `global` forever, is the one guaranteed to hear nothing.
func unscopedOmission(f OmissionFacts) *Omission {
	if f.DeclaredScopes != 0 || f.InDefaultScope != f.Live {
		return nil
	}
	return &Omission{"unscoped",
		fmt.Sprintf("## NEVER SCOPED — all %d memories are in `%s` and no scope table "+
			"is declared", f.Live, store.DefaultScope),
		[]string{
			"  One scope means recall cannot separate two subjects that share a word, " +
				"and it gets",
			"  harder to unpick with every memory. Declare the axes this project " +
				"actually has:",
			"    os-brain scope add <name> --desc \"...\"      then --scope on the next write",
		}}
}

// unboundRulesOmission: rules stored as something that does not bind. BindingTypes are
// injected into every brief whether pinned or not; a `decision` or a `knowledge` row
// saying "never do X" falls out of the brief the moment anything more topical arrives,
// and the rule is then enforced by luck.
func unboundRulesOmission(f OmissionFacts) *Omission {
	if len(f.UnboundRules) == 0 {
		return nil
	}
	lines := make([]string, 0, len(f.UnboundRules)+2)
	for _, r := range f.UnboundRules {
		lines = append(lines, fmt.Sprintf("  #%d [%s] %s", r.ID, r.Type, textguard.OneLine(textguard.Clip(r.Title, 66))))
	}
	lines = append(lines,
		"  A standing rule has to be a type the brief always carries, or it binds "+
			"nobody after",
		"  it scrolls away:  os-brain update <id> --type constraint")
	return &Omission{"unbound_rules",
		fmt.Sprintf("## RULES THAT DO NOT BIND — %d memor%s reads as a standing rule "+
			"and is stored as something else", len(f.UnboundRules),
			omPlural(len(f.UnboundRules))), lines}
}

// undatedOmission: dates written into prose, where nothing will ever surface them. The
// brain parses schedules and computes lateness, and looks at none of it unless somebody
// types `task`, so a project that is nothing but deadlines can hold every one of them and
// report `no tasks`.
func undatedOmission(f OmissionFacts) *Omission {
	if f.Tasks != 0 || len(f.Dated) == 0 {
		return nil
	}
	lines := make([]string, 0, len(f.Dated)+2)
	for _, r := range f.Dated {
		lines = append(lines, fmt.Sprintf("  #%d  %s  %s", r.ID, r.Detail,
			textguard.OneLine(textguard.Clip(r.Title, 60))))
	}
	lines = append(lines,
		"  A date in a body is prose: nothing reads it, nothing is due, nothing arrives.",
		"    os-brain task add \"<what is owed>\" --when <date>")
	return &Omission{"undated",
		fmt.Sprintf("## DEADLINES WITH NO TASK — %d memor%s carries a future date and "+
			"this brain has no tasks", len(f.Dated),
			omPlural(len(f.Dated))), lines}
}

// neverLoggedOmission: remembering and logging answer different questions (what is true,
// and what happened), and the Stop hook goes quiet the moment either one is non-zero. So
// a project can store steadily and log nothing, for its whole life, and never be told.
func neverLoggedOmission(f OmissionFacts) *Omission {
	if f.Events != 0 {
		return nil
	}
	return &Omission{"never_logged",
		fmt.Sprintf("## NEVER LOGGED — %d memories stored, 0 events", f.Live),
		[]string{
			"  Memories say what is TRUE; the log says what HAPPENED. `os-brain open` " +
				"answers",
			"  \"where did we leave off\" from the log, and with none it has nothing to " +
				"answer from.",
			"    os-brain log --kind <kind> \"<what happened>\"",
		}}
}

func undescribedOmission(f OmissionFacts) *Omission {
	if strings.TrimSpace(f.About) != "" {
		return nil
	}
	return &Omission{"undescribed",
		"## NOT IN THE SHARED INDEX — no one-line summary",
		[]string{"  Other projects search the index before solving a problem twice.",
			"    os-brain describe \"<what this is>\" --stack \"...\""}}
}

func omPlural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// dateForm is one way of writing a calendar date, and which capture group holds each part.
type dateForm struct {
	re               *regexp.Regexp
	year, month, day int
}

var dateForms = []dateForm{
	{regexp.MustCompile(`\b(\d{1,2})/(\d{1,2})/(\d{4})\b`), 3, 2, 1}, // 31/12/2026
	{regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})\b`), 1, 2, 3},     // 2026-12-31
}

// FutureDates returns the earliest date in the text that is still ahead, formatted as it
// was written. Only explicit calendar dates count: "next quarter" is an intention, a date
// is a commitment, and only one of the two can be turned into a task without guessing.
func FutureDates(text string, now time.Time) string {
	best := ""
	var bestT time.Time
	for _, form := range dateForms {
		for _, m := range form.re.FindAllStringSubmatch(text, -1) {
			y, _ := strconv.Atoi(m[form.year])
			mo, _ := strconv.Atoi(m[form.month])
			d, _ := strconv.Atoi(m[form.day])
			if mo < 1 || mo > 12 || d < 1 || d > 31 {
				continue
			}
			// The end of the day, so a date that is today still counts as ahead.
			t := time.Date(y, time.Month(mo), d, 23, 59, 59, 0, now.Location())
			if t.After(now) && (best == "" || t.Before(bestT)) {
				best, bestT = m[0], t
			}
		}
	}
	return best
}

// OmissionSummary is the one line a brief can afford: which dimensions this brain has
// never used at all. The full report costs a command nobody runs, so without this line a
// project can go on omitting them without ever being told.
func OmissionSummary(oms []Omission) string {
	names := map[string]string{
		"unscoped": "scopes", "never_logged": "the log", "undated": "tasks",
		"unbound_rules": "binding rules", "undescribed": "the shared index",
	}
	var parts []string
	for _, o := range oms {
		if n := names[o.Key]; n != "" {
			parts = append(parts, n)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "## THIS BRAIN HAS NEVER USED — " + strings.Join(parts, ", ") +
		" · os-brain review says what each one costs"
}

// GatherOmissions reads the facts out of the store and runs the checks. A read that
// fails reports nothing rather than a false omission.
func (s *Store) GatherOmissions() []Omission {
	f := OmissionFacts{DeclaredScopes: len(s.Env.Config.Scopes),
		About: s.Env.Config.Context["about"]}
	rows, err := s.DB.Query("SELECT id, mem_type, scope, title, pinned FROM memories "+
		"WHERE project=? AND superseded_by IS NULL", s.Env.Project)
	if err != nil {
		return nil
	}
	now := time.Now()
	for rows.Next() {
		var id int64
		var mt, scope, title string
		var pinned int
		if rows.Scan(&id, &mt, &scope, &title, &pinned) != nil {
			continue
		}
		f.Live++
		f.Pinned += pinned
		if scope == store.DefaultScope {
			f.InDefaultScope++
		}
		if store.RuleShaped(title) && !store.BindingTypes[mt] {
			f.UnboundRules = append(f.UnboundRules, OmissionRow{id, mt, title, ""})
		}
		if d := FutureDates(title, now); d != "" {
			f.Dated = append(f.Dated, OmissionRow{id, mt, title, d})
		}
	}
	rows.Close()
	_ = s.DB.QueryRow("SELECT COUNT(*) FROM events WHERE project=?", s.Env.Project).
		Scan(&f.Events)
	_ = s.DB.QueryRow("SELECT COUNT(*) FROM tasks WHERE project=?", s.Env.Project).
		Scan(&f.Tasks)
	return Omissions(f)
}
