package brief

// Tests for Budget, which trims the brief to a byte budget.
//
// The brief is the only output guaranteed to be read, and a host that truncates it keeps
// the FIRST n bytes: the preamble, not the binding rules. So the brain does the cutting
// itself, in order of importance, and always says that it cut.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// briefish builds a brief-shaped list of lines: a two-line preamble, then three sections
// in the brief's order of importance, each with thirty long entries. It is far over any
// budget the tests use, so every test exercises real trimming.
func briefish() []string {
	long := strings.Repeat("a fact with a long body that will need clipping. ", 30)
	out := []string{"  now 2026-08-18", "  project demo"}
	for _, h := range []string{"## GUARDRAILS", "## PINNED (read first)", "## RECENT ACTIVITY"} {
		out = append(out, "", h)
		for i := 0; i < 30; i++ {
			out = append(out, fmt.Sprintf("  #%d %s", i, long))
		}
	}
	return out
}

// The result fits the budget, and the two things that must survive any cut do: the binding
// rules, and the preamble that says which project this brief is for.
func TestTheBriefIsTrimmedToTheBudget(t *testing.T) {
	got := strings.Join(Budget(briefish(), 4000), "\n")
	if len(got) > 4000 {
		t.Errorf("budget of 4000 produced %d bytes", len(got))
	}
	if !strings.Contains(got, "GUARDRAILS") {
		t.Error("the binding rules were trimmed away, which is the one thing that must not go")
	}
	if !strings.Contains(got, "now 2026-08-18") || !strings.Contains(got, "project demo") {
		t.Error("the preamble was dropped: a brief that does not say which project it is")
	}
}

// Trimming must be visible. Hosts were already cutting this output silently, which is the
// reason Budget exists at all.
func TestTrimmingSaysThatItHappened(t *testing.T) {
	got := strings.Join(Budget(briefish(), 3000), "\n")
	if !strings.Contains(got, "trimmed to fit") {
		t.Error("sections were shortened or dropped without saying so")
	}
}

// A list is shortened before it is sacrificed: a truncated list of the most important
// facts still carries the most important facts.
func TestALongSectionIsShortenedBeforeAnySectionIsDropped(t *testing.T) {
	got := strings.Join(Budget(briefish(), 4000), "\n")
	for _, want := range []string{"GUARDRAILS", "PINNED"} {
		if !strings.Contains(got, want) {
			t.Errorf("%s was dropped while shortening was still available", want)
		}
	}
}

// Least important first: a mild overrun costs the tail of RECENT ACTIVITY, not PINNED.
func TestTheLeastImportantSectionIsCutFirst(t *testing.T) {
	lines := briefish()
	full := len(strings.Join(lines, "\n"))
	got := strings.Join(Budget(lines, full-1200), "\n")
	iPinned := strings.Index(got, "PINNED")
	iRecent := strings.Index(got, "RECENT ACTIVITY")
	if iRecent < 0 {
		return // dropped entirely, which is still least-important-first
	}
	pinnedEntries := strings.Count(got[iPinned:iRecent], "\n  #")
	recentEntries := strings.Count(got[iRecent:], "\n  #")
	if recentEntries > pinnedEntries {
		t.Errorf("RECENT ACTIVITY kept %d entries and PINNED kept %d: the cut went the "+
			"wrong way round", recentEntries, pinnedEntries)
	}
}

// Zero means no budget: somebody reading in a terminal or a viewer wants the whole thing.
func TestZeroBudgetMeansNoTrimming(t *testing.T) {
	lines := briefish()
	if got := Budget(lines, 0); len(got) != len(lines) {
		t.Errorf("a zero budget trimmed anyway: %d lines became %d", len(lines), len(got))
	}
}

// Clipping counts bytes, and stores hold non-Latin text. A blind byte slice in the middle
// of a rune puts a replacement character into the one output guaranteed to be read.
func TestClippingNeverSplitsAMultibyteCharacter(t *testing.T) {
	greek := "  " + strings.Repeat("η δυσκοιλιότητα κλείνει την εντερική οδό αποβολής καλίου ", 10)
	got := clipLine(greek)
	if strings.ContainsRune(got, '�') {
		t.Error("the clip produced a replacement character")
	}
	if !strings.Contains(got, "chars)") {
		t.Errorf("a long line was not clipped: %d bytes", len(got))
	}
}

// A section of memories loses its BODIES before it loses its entries. Twenty guardrail
// titles carry twenty binding rules; one guardrail with its reasoning carries one. Before
// this ordering, one guardrail with its reasoning was what a session actually received.
func TestBudgetStripsBodiesBeforeDroppingEntries(t *testing.T) {
	var lines []string
	lines = append(lines, "========", "  project  x", "========")
	lines = append(lines, "", "## GUARDRAILS (binding, always shown)")
	for i := 1; i <= 8; i++ {
		lines = append(lines, fmt.Sprintf("  #%d [constraint] rule number %d", i, i))
		lines = append(lines, "     "+strings.Repeat("reasoning ", 20))
	}
	out := strings.Join(Budget(lines, 900), "\n")
	kept := strings.Count(out, "[constraint]")
	if kept < 6 {
		t.Fatalf("bodies should be cut before entries are; kept %d of 8 rules:\n%s", kept, out)
	}
	if strings.Contains(out, "reasoning reasoning") {
		t.Fatalf("bodies should be gone once the block is stripped:\n%s", out)
	}
	if !strings.Contains(out, "(bodies cut)") {
		t.Fatalf("a body cut must be announced, like every other cut here:\n%s", out)
	}
}

// The "+N more" counts report ENTRIES, not rendered lines. A block of twenty two-line
// memories trimmed to one used to report "+40", for a section that never had forty entries.
func TestBudgetCountsEntriesNotLines(t *testing.T) {
	var lines []string
	lines = append(lines, "========", "  project  x", "========")
	lines = append(lines, "", "## RECENT ACTIVITY")
	for i := 1; i <= 10; i++ {
		lines = append(lines, fmt.Sprintf("  #%d [state] entry %d", i, i))
		lines = append(lines, "     "+strings.Repeat("body ", 30))
	}
	out := strings.Join(Budget(lines, 200), "\n")
	if strings.Contains(out, "+20") || strings.Contains(out, "+19") {
		t.Fatalf("counted lines rather than entries:\n%s", out)
	}
	m := regexp.MustCompile(`\+(\d+) more`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("expected a trimmed count:\n%s", out)
	}
	if n, _ := strconv.Atoi(m[1]); n > 10 {
		t.Fatalf("reported +%d for a section that had 10 entries:\n%s", n, out)
	}
}
