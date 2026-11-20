package brain

// Tests for omissions: the parts of the brain a project has never used.
//
// Every other check in the brain reports excess (duplicates, stale rows, undeclared
// scopes). None of them could see absence: a project that never declared a scope, never
// logged, never filed a task or stored its rules as a type that does not bind. Omissions
// reports those, in `review` and as one line in the brief, and only once the brain is big
// enough for the absence to mean something.

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// baseFacts describes a brain above the floor that uses tasks and the log and keeps every
// memory in the default scope with no scope table declared.
func baseFacts() OmissionFacts {
	return OmissionFacts{Live: 9, InDefaultScope: 9, DeclaredScopes: 0, Events: 3,
		Tasks: 2, About: "a project"}
}

// keys lists the omission keys reported, for failure messages.
func keys(oms []Omission) []string {
	out := make([]string, 0, len(oms))
	for _, o := range oms {
		out = append(out, o.Key)
	}
	return out
}

// has reports whether the omission with this key was reported.
func has(oms []Omission, key string) bool {
	for _, o := range oms {
		if o.Key == key {
			return true
		}
	}
	return false
}

// titleN returns a distinct, realistic memory title for index i.
func titleN(i int) string {
	return []string{"the deploy runbook", "how billing is wired", "why postgres here",
		"the nightly job", "what the api returns", "where the certs live",
		"the rate limiter"}[i%7] + " " + string(rune('a'+i))
}

// --- The rules ---

// A brain with four memories has not omitted a scope table; it has not needed one yet. A
// nudge that fires on correct usage teaches people to ignore nudges.
func TestNothingIsReportedBelowTheFloor(t *testing.T) {
	f := baseFacts()
	f.Live, f.InDefaultScope = OmissionFloor-1, OmissionFloor-1
	if oms := Omissions(f); len(oms) != 0 {
		t.Errorf("a young brain was nagged: %v", keys(oms))
	}
}

// The declared-scopes check and review's UNDECLARED SCOPES section both only run once a
// scope table exists, so the project that never declared one was the one shape that heard
// nothing about scopes. A project that declared scopes, or already uses a second scope,
// is not told.
func TestTheProjectThatNeverScopedIsTheOneToldAboutScopes(t *testing.T) {
	f := baseFacts()
	oms := Omissions(f)
	if !has(oms, "unscoped") {
		t.Fatalf("a brain with no scope table and everything in global was not told: %v",
			keys(oms))
	}
	f.DeclaredScopes = 2
	if has(Omissions(f), "unscoped") {
		t.Error("a project that declared its scopes was nagged about scopes")
	}
	f.DeclaredScopes = 0
	f.InDefaultScope = f.Live - 1
	if has(Omissions(f), "unscoped") {
		t.Error("a project already using a second scope was nagged about scopes")
	}
}

// policy, constraint, preference and relationship reach every brief whether pinned or not.
// A rule stored as any other type stops being read the moment it scrolls away, so it is
// reported, by id.
func TestARuleStoredAsSomethingThatDoesNotBindIsReported(t *testing.T) {
	f := baseFacts()
	f.UnboundRules = []OmissionRow{{5, "decision", "This brain stays local - never sync " +
		"it anywhere", ""}}
	oms := Omissions(f)
	if !has(oms, "unbound_rules") {
		t.Fatalf("a rule stored as a decision was not reported: %v", keys(oms))
	}
	for _, o := range oms {
		if o.Key == "unbound_rules" && !strings.Contains(strings.Join(o.Lines, "\n"), "#5") {
			t.Error("the report does not name the memory it is about")
		}
	}
}

// RuleShaped decides whether a title reads as a standing rule, from the title alone.
func TestRuleShapedReadsTheTitleOnly(t *testing.T) {
	for _, s := range []string{"never deploy on a Friday", "Hard rules: do not publish it",
		"Always take a backup first", "this is forbidden"} {
		if !RuleShaped(s) {
			t.Errorf("%q is a standing rule and was not read as one", s)
		}
	}
	for _, s := range []string{"the deploy runbook", "postgres beats sqlite here",
		"the 2024 total of 662 was silently dropped"} {
		if RuleShaped(s) {
			t.Errorf("%q is ordinary prose and was read as a rule", s)
		}
	}
	// The accepted false positive, recorded rather than hidden: past-tense prose carrying
	// a rule word. It costs one warning line on a write that is then made anyway, which is
	// why this warns and never refuses.
	if !RuleShaped("why we never got round to it in 2024") {
		t.Error("the heuristic changed; check the false-positive cost before narrowing it")
	}
}

// The brain parses schedules and computes lateness, and looks at none of it unless somebody
// types `task`, so a project full of deadlines written in prose reported "no tasks".
// FutureDates finds the earliest future date in a text (day/month/year or ISO), ignoring
// past and impossible dates; a dated memory in a brain with no tasks is then reported.
func TestADateInProseWithNoTaskIsReported(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	if got := FutureDates("the joint Form 11, due 31/10/2026 or 18/11/2026", now); got != "31/10/2026" {
		t.Errorf("the earliest future date is 31/10/2026, got %q", got)
	}
	if got := FutureDates("filed on 31/10/2019 and closed", now); got != "" {
		t.Errorf("a past date is not a deadline: %q", got)
	}
	if got := FutureDates("deploy window 2027-01-04", now); got != "2027-01-04" {
		t.Errorf("iso dates count too: %q", got)
	}
	if got := FutureDates("version 99/99/2099 is nonsense", now); got != "" {
		t.Errorf("an impossible date is not a date: %q", got)
	}

	f := baseFacts()
	f.Tasks = 0
	f.Dated = []OmissionRow{{1, "state", "Form 11 due 31/10/2026", "31/10/2026"}}
	if !has(Omissions(f), "undated") {
		t.Error("a dated memory in a brain with no tasks was not reported")
	}
	f.Tasks = 1
	if has(Omissions(f), "undated") {
		t.Error("a project that already uses tasks was nagged about tasks")
	}
}

// The silent-session check goes quiet the moment EITHER a memory or an event exists, so a
// project could store steadily and log nothing for its whole life and never be told.
func TestAProjectThatOnlyEverRemembersIsTold(t *testing.T) {
	f := baseFacts()
	f.Events = 0
	if !has(Omissions(f), "never_logged") {
		t.Error("a brain with memories and no events was not told")
	}
	f.Events = 1
	if has(Omissions(f), "never_logged") {
		t.Error("a project that logs was nagged about logging")
	}
}

// The same split at the end of a session: storing memories makes a session not silent, but
// it is still unlogged if it wrote no event. A session too short to have done anything is
// never labelled either way.
func TestTheStopHookSeparatesRememberingFromLogging(t *testing.T) {
	work := SessionReport{Seconds: silentSessionMinSeconds, Memories: 9, Events: 0}
	if work.Silent() {
		t.Error("a session that stored nine memories is not silent")
	}
	if !work.Unlogged() {
		t.Error("nine memories and no log went unreported, which is the whole defect")
	}
	both := SessionReport{Seconds: silentSessionMinSeconds, Memories: 9, Events: 2}
	if both.Unlogged() {
		t.Error("a session that logged was told it had not")
	}
	idle := SessionReport{Seconds: 5, Memories: 1}
	if idle.Unlogged() {
		t.Error("the label must be impossible to earn by doing nothing")
	}
}

// --- Where omissions are shown ---

// review once answered "nothing to clean up" to a brain that had never used half the
// product. It reports absence as well as excess.
func TestReviewReportsAbsenceAndNotOnlyExcess(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < OmissionFloor+1; i++ {
		remember(t, s, RememberArgs{Title: titleN(i), Body: "a body long enough to be a " +
			"real memory about subject " + titleN(i)})
	}
	var buf bytes.Buffer
	if err := s.Review(&buf, ReviewArgs{}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"NEVER SCOPED", "NEVER LOGGED", "NOT IN THE SHARED INDEX"} {
		if !strings.Contains(out, want) {
			t.Errorf("review does not report %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "nothing to clean up.") {
		t.Error("review called a brain that has never used scopes or the log clean")
	}
}

// The full report needs a command that nobody runs in a project whose subject is not the
// brain, and "nobody runs it" is exactly the condition under which an omission survives.
// So the brief carries a one-line summary.
func TestTheBriefCarriesTheOneLineSummary(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < OmissionFloor+1; i++ {
		remember(t, s, RememberArgs{Title: titleN(i), Body: "a body long enough to be a " +
			"real memory about subject " + titleN(i)})
	}
	var buf bytes.Buffer
	if err := s.Brief(&buf, BriefArgs{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "THIS BRAIN HAS NEVER USED") {
		t.Errorf("the brief says nothing about the dimensions never used:\n%s", buf.String())
	}
}
