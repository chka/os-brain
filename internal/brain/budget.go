package brain

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// The brief is the only output guaranteed to be read, and it was quietly not being read.
//
// Claude Code truncates a hook's stdout into the session at roughly 2KB, writing the rest
// to a file and injecting a preview. Real briefs ran to 30KB and more, so the session saw
// the identity block, part of the axioms and nothing else: PINNED, GUARDRAILS, the
// preferences, the tasks that are due, all of it past the cut, in a tool whose entire
// thesis is that the brief arrives before the first turn.
//
// The rule is that the brain does the cutting, from the least important end, and says so.
// A host that truncates keeps the first N bytes, which is the WORST possible selection: it
// keeps the boilerplate and drops the binding rules.
//
// Budget works down a ladder, cheapest loss first, and stops as soon as the brief fits:
//
//  1. clip over-long lines
//  2. strip entry bodies, keeping each entry's own line
//  3. shorten long sections to 12, 6, 3, 1 entries, then to the header alone
//  4. drop whole sections
//
// Every pass works from the least important section up, and every cut is announced.

// DefaultMaxBytes is the budget when nobody says otherwise.
//
// It was 1800, chosen to sit inside the ~2KB a truncating host injects. That became
// redundant once sections were emitted in priority order: the host's cut and this budget
// then defend the same thing from the same end, and paying both throws away bytes the host
// would have delivered.
//
// Under a host that truncates, a longer brief loses nothing: the first 2KB it keeps is the
// same first 2KB, because guardrails and preferences are printed first by construction.
// Under a host that injects everything, 1800 was pure loss. Measured on a real project: 1800
// delivered ONE of twenty-one binding rules, and 6000 delivers fifteen records for about
// 1.5k tokens, against 16k tokens of hand-written CLAUDE.md the same session was already
// reading in full. The budget's remaining job is to stop 35KB of brief going into a context
// window, not to guess where somebody else cuts.
const DefaultMaxBytes = 6000

// budgetOrder is the priority, most important first. A section not named here sits at the
// boundary between "worth keeping" and "drop first", which is the safe place for something
// nobody has classified: it survives a mild trim and goes early under a hard one.
var budgetOrder = []string{
	"GUARDRAILS",
	"THE BRAIN IS SMALLER",
	"ASK BEFORE ANYTHING ELSE",
	"HOW THEY WANT TO WORK",
	"PINNED",
	"DUE FOR REVIEW",
	"THIS PROJECT HAS A BRAIN",
	"AXIOMS",
	"MEASURES",
	"WORKING TREE",
	"NEW SINCE",
	"PROBES",
	"INBOX",
	"THE LAST SESSION HERE LEFT NOTHING BEHIND",
	"ANOTHER MEMORY STORE",
	"SAFETY COPY",
	"STANDARDS",
	"PIPELINE",
	"HOT LEADS",
	"NEXT ACTIONS",
	"KNOWS ABOUT",
	"WHERE THINGS LIVE",
	"OTHER PROJECTS",
	"RECENT ACTIVITY",
}

// rank is the position of a section in budgetOrder; unlisted sections land in the middle.
func rank(header string) int {
	h := strings.TrimSpace(strings.TrimPrefix(header, "##"))
	for i, name := range budgetOrder {
		if strings.HasPrefix(strings.ToUpper(h), name) {
			return i
		}
	}
	return len(budgetOrder) / 2
}

// MaxBytesFrom resolves the budget: an explicit flag wins, then OS_BRAIN_MAX_BYTES, then the
// caller's own default. Zero or negative means no limit.
//
// The default is applied on the HOOK path only. `wakeup` is injected into a session by a
// host that truncates, so it is budgeted; `brief` is requested (by a person at a terminal,
// by the viewer, by an agent mid-session reading it as tool output) and a requested read
// should return what was asked for. One number, two callers, and the difference is who is
// doing the cutting downstream.
func MaxBytesFrom(flag int, flagSet bool, fallback int) int {
	if flagSet {
		return flag
	}
	if v := os.Getenv("OS_BRAIN_MAX_BYTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// budgetClip is where a single rendered line is cut before any section is sacrificed. One
// memory with a 3KB body should not cost the whole PINNED block, which is what dropping
// sections alone does: the first pass is entirely inside the sections.
const budgetClip = 220

func clipLine(ln string) string {
	if len(ln) <= budgetClip {
		return ln
	}
	cut := budgetClip
	// Step back off a UTF-8 continuation byte, then to a space, so the clip lands neither
	// mid-rune nor mid-word. Slicing bytes blind would put a replacement character into the
	// one output guaranteed to be read, for any store not written in ASCII.
	for cut > budgetClip-40 && cut < len(ln) && ln[cut]&0xC0 == 0x80 {
		cut--
	}
	if sp := strings.LastIndex(ln[:cut], " "); sp > budgetClip-60 {
		cut = sp
	}
	return ln[:cut] + fmt.Sprintf(" …(+%d chars)", len(ln)-cut)
}

// isContinuation reports whether a line continues the entry above it rather than starting
// a new one. renderMemories indents a memory's body by five spaces and its own title by
// two, and every other renderer in the brief indents by two, so the discriminator is the
// rendering rule rather than a guess about content.
func isContinuation(ln string) bool { return strings.HasPrefix(ln, "     ") }

// Budget trims rendered brief lines to fit max bytes (no limit when max <= 0), working down
// the ladder described at the top of this file. The preamble (everything before the first
// `## ` header) and the identity banner are never cut: they say which project and which hour
// the brief belongs to, and a brief that does not is worse than no brief.
func Budget(lines []string, max int) []string {
	if max <= 0 || joinedLen(lines) <= max {
		return lines
	}
	clipped := make([]string, len(lines))
	for i, ln := range lines {
		clipped[i] = clipLine(ln)
	}
	if joinedLen(clipped) <= max {
		return clipped
	}
	b := splitBrief(clipped)
	b.stripBodies(max)
	b.shortenSections(max)
	dropped := b.dropSections(max)
	return b.emit(dropped)
}

func joinedLen(lines []string) int { return len(strings.Join(lines, "\n")) }

// briefSection is one `## ` section of the brief, or the identity banner.
//
// It holds ENTRIES, not lines, because every list renders one record over several lines,
// and a ladder that counted lines cut a section of twenty guardrails down to one of them
// while reporting "+40" for a block that never had forty entries.
type briefSection struct {
	header string
	// entries[i][0] is the entry's own line; the rest is its body.
	entries [][]string
	// keep marks the identity banner: project, clock, paths. It is never shortened and
	// never dropped, because it is the block a session needs even when everything else has
	// been cut. It also gives the budget a floor of roughly this block's size; asking for
	// less yields it anyway.
	keep bool
	// total is how many entries the section had BEFORE any trimming. The "+N" counts come
	// from it rather than from the current length: a section trimmed twice would otherwise
	// report "+2" for a section that started with sixty entries.
	total int
	// stripped records that the section has already lost its bodies, so the marker is
	// written once and the pass does not re-pick a section it cannot shrink further.
	stripped bool
}

func (sec briefSection) lines() []string {
	var out []string
	if sec.header != "" {
		out = append(out, sec.header)
	}
	for _, e := range sec.entries {
		out = append(out, e...)
	}
	return out
}

// budgetedBrief is a brief split for trimming: the preamble, then its sections in the
// order the brief assembled them.
type budgetedBrief struct {
	preamble []string
	sections []briefSection
}

func splitBrief(lines []string) *budgetedBrief {
	b := &budgetedBrief{}
	identityOpen := false
	for _, ln := range lines {
		if strings.HasPrefix(ln, "## ") {
			b.sections = append(b.sections, briefSection{header: ln})
			continue
		}
		// The identity banner starts its own section even though it carries no `## `
		// header. Without this it is swept into whatever section precedes it (the
		// instructions) and then cannot be protected or moved independently of them.
		if strings.HasPrefix(ln, "====") && !identityOpen {
			identityOpen = true
			b.sections = append(b.sections, briefSection{keep: true})
		}
		if len(b.sections) == 0 {
			b.preamble = append(b.preamble, ln)
			continue
		}
		b.addLine(ln)
	}
	for i := range b.sections {
		b.sections[i].total = len(b.sections[i].entries)
	}
	return b
}

// addLine appends ln to the last section, as a new entry or as the body of the last one.
func (b *budgetedBrief) addLine(ln string) {
	sec := &b.sections[len(b.sections)-1]
	if len(sec.entries) > 0 && isContinuation(ln) {
		last := len(sec.entries) - 1
		sec.entries[last] = append(sec.entries[last], ln)
		return
	}
	sec.entries = append(sec.entries, []string{ln})
}

// size is the byte length the brief would render to now.
func (b *budgetedBrief) size() int {
	n := joinedLen(b.preamble)
	for _, sec := range b.sections {
		n += 1 + joinedLen(sec.lines())
	}
	return n
}

// leastImportantFirst returns section indices in the order every trimming pass works in, so
// a mild overrun costs the tail of RECENT ACTIVITY, not the head of GUARDRAILS.
func (b *budgetedBrief) leastImportantFirst() []int {
	idx := make([]int, len(b.sections))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(x, y int) bool {
		return rank(b.sections[idx[x]].header) > rank(b.sections[idx[y]].header)
	})
	return idx
}

// stripBodies reduces sections to their entries' own lines before any entry is dropped. A
// memory's own line carries the fact and its body the elaboration, so twenty titles are
// worth more at session open than one title with its reasoning. Without this rung the
// ladder went straight from full entries to a single entry, and a session that had twenty
// guardrails was receiving exactly one of its own binding rules.
func (b *budgetedBrief) stripBodies(max int) {
	for _, i := range b.leastImportantFirst() {
		if b.size() <= max {
			return
		}
		sec := &b.sections[i]
		if sec.keep || sec.stripped {
			continue
		}
		had := false
		for j, e := range sec.entries {
			if len(e) > 1 {
				sec.entries[j] = e[:1]
				had = true
			}
		}
		if !had {
			continue
		}
		sec.stripped = true
		// Announced, like every other cut here. A section quietly reduced to its titles is
		// the silent truncation this whole file exists to replace, one degree milder.
		if sec.header != "" {
			sec.header += "  (bodies cut)"
		}
	}
}

// shortenSections trims long sections before sacrificing any of them. A project with 68
// pinned memories renders a PINNED block larger than the whole budget, and a drop-only rule
// threw the entire block away, which is the worst answer available: a truncated list of the
// most important facts still carries the most important facts.
//
// Each section is trimmed at most once per level. Appending the "+N more" line leaves a
// section still longer than the threshold, so re-checking within a level would keep
// re-picking it and never reach the others.
func (b *budgetedBrief) shortenSections(max int) {
	for _, keep := range []int{12, 6, 3, 1, 0} {
		for _, i := range b.leastImportantFirst() {
			if b.size() <= max {
				break
			}
			sec := &b.sections[i]
			if sec.keep || len(sec.entries) <= keep+1 {
				continue
			}
			more := sec.total - keep
			if keep == 0 {
				// At the last level a section degrades to its HEADER ALONE rather than
				// being sacrificed. Headers are written to stand by themselves ("THIS
				// PROJECT HAS A BRAIN — os-brain: brief, recall, remember..." carries the
				// contract in one line), so a session under a hard cut still learns that
				// the section exists and what it is for.
				sec.entries = nil
				sec.header = strings.TrimSuffix(sec.header, "  (bodies cut)") +
					fmt.Sprintf("  (+%d trimmed)", more)
				continue
			}
			sec.entries = append([][]string{}, sec.entries[:keep]...)
			sec.entries = append(sec.entries,
				[]string{fmt.Sprintf("  … +%d more, trimmed to fit", more)})
		}
		if b.size() <= max {
			return
		}
	}
}

// dropSections removes whole sections, lowest priority first, until the brief fits or only
// the protected banner is left (the floor, not a failure). It picks the worst survivor each
// time rather than sorting, so the survivors keep their original order. It returns the
// names of the sections it dropped.
func (b *budgetedBrief) dropSections(max int) []string {
	var dropped []string
	for len(b.sections) > 0 && b.size() > max {
		worst, worstRank := -1, -1
		for i, sec := range b.sections {
			if sec.keep {
				continue
			}
			if r := rank(sec.header); r > worstRank {
				worst, worstRank = i, r
			}
		}
		if worst < 0 {
			break
		}
		name := strings.TrimSpace(strings.TrimPrefix(b.sections[worst].header, "##"))
		if i := strings.Index(name, "  ("); i > 0 {
			name = name[:i]
		}
		dropped = append(dropped, name)
		b.sections = append(b.sections[:worst], b.sections[worst+1:]...)
	}
	return dropped
}

// emit renders the trimmed brief in PRIORITY order rather than the order it was assembled
// in. Under a host that cuts at a fixed byte count this decides survival: whatever is
// printed first is what arrives. Guardrails and the preferences reach a session that only
// ever sees two kilobytes; RECENT ACTIVITY does not, and should not.
func (b *budgetedBrief) emit(dropped []string) []string {
	sort.SliceStable(b.sections, func(x, y int) bool {
		if b.sections[x].keep != b.sections[y].keep {
			return b.sections[x].keep
		}
		return rank(b.sections[x].header) < rank(b.sections[y].header)
	})
	out := b.preamble
	for _, sec := range b.sections {
		out = append(out, sec.lines()...)
	}
	if len(dropped) > 0 {
		out = append(out, "", droppedFooter(dropped))
	}
	return out
}

// droppedFooter says what was cut, always: a brief that silently drops the pipeline lies
// about what this project has, and the whole reason this file exists is that something else
// was doing the cutting without telling anybody. Compact on purpose, first word of each
// name only: spelling out nine section names once cost a quarter of a small budget on
// describing what is not in the brief.
func droppedFooter(dropped []string) string {
	short := make([]string, 0, len(dropped))
	for _, d := range dropped {
		if i := strings.IndexAny(d, " ("); i > 0 {
			d = d[:i]
		}
		short = append(short, d)
	}
	return fmt.Sprintf("## dropped: %s · os-brain brief --max-bytes 0", strings.Join(short, ", "))
}
