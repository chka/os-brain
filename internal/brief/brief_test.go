package brief

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/curate"
	"github.com/chka/os-brain/internal/memory"
	"github.com/chka/os-brain/internal/spine"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/textguard"
	"github.com/chka/os-brain/internal/timefmt"
)

// Tests for the session brief and the global spine it reads its axioms from.
//
// The brief is the one output every session is guaranteed to read, so most of these tests
// protect against the same failure: something important silently missing from it, with
// nothing to tell the reader that it is gone.

// brief renders the human brief for s and fails the test if rendering errors.
func brief(t *testing.T, s *Store, a BriefArgs) string {
	t.Helper()
	var buf bytes.Buffer
	if err := s.Brief(&buf, a); err != nil {
		t.Fatalf("brief: %v", err)
	}
	return buf.String()
}

// --- The spine and its axioms ---

// The DDL is split on ';' and executed statement by statement, and one of the schema
// comments uses a semicolon. Splitting naively cut that CREATE TABLE in half, the global
// schema failed to build, globalConnect returned nil, and the brief fell back to the
// built-in axioms with no error anywhere. Silent degradation is treated as worse than a
// crash, so it gets a test.
func TestSchemaCommentsDoNotSplitStatements(t *testing.T) {
	for _, ddl := range []string{store.SchemaDDL, store.GlobalSchemaDDL} {
		for _, stmt := range store.SplitStatements(ddl) {
			if strings.Contains(stmt, "--") {
				t.Errorf("a comment survived into an executable statement: %q", textguard.Clip(stmt, 80))
			}
		}
	}
	s := newTestStore(t)
	g := s.GlobalConnect(true)
	if g == nil {
		t.Fatal("the global schema did not build")
	}
	defer s.CloseGlobal(g)
	for _, table := range []string{"axioms", "projects", "standards", "notes", "lessons"} {
		var n int
		if err := g.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Errorf("table %q missing from the spine: %v", table, err)
		}
	}
}

// The default axioms are real rows in the spine. If they were a fallback that applied only
// while the table was empty, adding the first custom axiom would silently remove all of the
// core ones.
func TestAxiomsAreSeededAsRealRowsNotAFallback(t *testing.T) {
	s := newTestStore(t)
	g := s.GlobalConnect(true)
	if g == nil {
		t.Fatal("no spine")
	}
	var n int
	if err := g.QueryRow("SELECT COUNT(*) FROM axioms").Scan(&n); err != nil {
		t.Fatal(err)
	}
	s.CloseGlobal(g)
	if n != len(store.DefaultAxioms) {
		t.Errorf("expected %d seeded axioms, got %d", len(store.DefaultAxioms), n)
	}
	if got := s.Axioms(); len(got) != len(store.DefaultAxioms) {
		t.Errorf("Axioms() returned %d", len(got))
	}
}

// An empty axioms table means somebody removed them on purpose. Quietly putting them back
// would undo that decision.
func TestDeletedAxiomsAreNotResurrected(t *testing.T) {
	s := newTestStore(t)
	g := s.GlobalConnect(true)
	if g == nil {
		t.Fatal("no spine")
	}
	if _, err := g.Exec("DELETE FROM axioms"); err != nil {
		t.Fatal(err)
	}
	s.CloseGlobal(g)
	if got := s.Axioms(); len(got) != 0 {
		t.Errorf("deleted axioms came back: %d", len(got))
	}
}

// A project that opts out of the spine with "global": false still gets the built-in
// axioms: a session must never be left without them. Opting out must also mean the spine
// is never opened.
func TestAxiomsFallBackWhenTheSpineIsOptedOut(t *testing.T) {
	s := newTestStore(t)
	off := false
	s.Env.Config.Global = &off
	if got := s.Axioms(); len(got) != len(store.DefaultAxioms) {
		t.Errorf("expected the built-in set, got %d", len(got))
	}
	if s.GlobalConnect(true) != nil {
		t.Error(`"global": false must not open the spine`)
	}
}

// --- What the brief shows ---

// The wall clock comes first, with the time of day, and the project right after it. A
// session that knows the date but not the hour greets people at midnight as if it were
// morning. Lines 0 to 2 are the title banner.
func TestBriefLeadsWithTheWallClockAndTheProject(t *testing.T) {
	s := newTestStore(t)
	out := brief(t, s, BriefArgs{})
	lines := strings.Split(out, "\n")
	if len(lines) < 5 {
		t.Fatalf("brief is too short:\n%s", out)
	}
	if !strings.HasPrefix(lines[3], "  now       ") {
		t.Errorf("the wall clock must lead, got %q", lines[3])
	}
	if !strings.Contains(lines[3], ":") {
		t.Errorf("the time of day is missing from %q", lines[3])
	}
	if !strings.Contains(lines[4], "testproj") {
		t.Errorf("the project must be named, got %q", lines[4])
	}
}

// Constraints and policies are binding, so they appear in GUARDRAILS whether or not they
// are pinned. A binding rule must never lose its slot to something more topical.
func TestGuardrailsAppearWhetherPinnedOrNot(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Type: "constraint", Title: "never wipe the prod db",
		Body: "there is no way to rebuild it"})
	remember(t, s, memory.RememberArgs{Type: "policy", Title: "back up before any migration", Body: "x"})
	remember(t, s, memory.RememberArgs{Type: "note", Title: "ordinary note", Body: "y"})

	out := brief(t, s, BriefArgs{})
	if !strings.Contains(out, "## GUARDRAILS") {
		t.Fatalf("no guardrail section:\n%s", out)
	}
	guards := out[strings.Index(out, "## GUARDRAILS"):]
	for _, want := range []string{"never wipe the prod db", "back up before any migration"} {
		if !strings.Contains(guards, want) {
			t.Errorf("guardrail missing: %q", want)
		}
	}
}

// Pinning is the user's way of saying "every session reads this".
func TestPinnedMemoriesAlwaysAppear(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Title: "the pinned one", Body: "x", Pin: true})
	out := brief(t, s, BriefArgs{})
	if !strings.Contains(out, "## PINNED") || !strings.Contains(out, "the pinned one") {
		t.Errorf("a pinned memory must be in every brief:\n%s", out)
	}
}

// Snoozing parks a memory until a date, even a pinned one, without hiding the rest.
func TestSnoozedMemoriesStayOutOfTheBrief(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Title: "parked until later", Body: "x", Pin: true,
		SnoozeUntil: "2099-01-01"})
	remember(t, s, memory.RememberArgs{Title: "current", Body: "x", Pin: true})
	out := brief(t, s, BriefArgs{})
	if strings.Contains(out, "parked until later") {
		t.Error("a snoozed memory must not be shown")
	}
	if !strings.Contains(out, "current") {
		t.Error("an awake memory must still be shown")
	}
}

// A load-bearing fact (tier A, such as infra) that nobody has verified says so, and the
// flag clears once someone verifies it. Old truth reads exactly like current truth unless
// the brief points out the difference.
func TestTrustFlagsSayWhenNobodyHasCheckedALoadBearingFact(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Type: "infra", Title: "the deploy host is alpha", Body: "x",
		Pin: true, Confidence: "confirmed", Source: "stated by the user"})
	out := brief(t, s, BriefArgs{})
	if !strings.Contains(out, "never verified") {
		t.Errorf("an unverified tier-A fact must say so:\n%s", out)
	}
	if err := s.Verify(bytes.NewBuffer(nil), memory.VerifyArgs{ID: 1}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(brief(t, s, BriefArgs{}), "never verified") {
		t.Error("verifying should clear the flag")
	}
}

// A store that holds rows, none of them visible to this project, must say so and name the
// label they are under. Silently rendering an empty brief is the dangerous failure: it
// looks like a working brain with no guardrails, and every rule the project has vanishes
// without an error. The usual cause is a renamed project.
func TestBriefSaysSoWhenNothingIsVisibleToThisProject(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.DB.Exec("INSERT INTO memories(project,scope,mem_type,title,body,source," +
		"importance,pinned,confidence,source_date,happened_on,hash,created_at,updated_at) " +
		"VALUES('someoneelse','global','note','theirs','x','session',3,0,'reported','',''," +
		"'h',1,1)"); err != nil {
		t.Fatal(err)
	}
	out := brief(t, s, BriefArgs{})
	if !strings.Contains(out, "NOTHING IS VISIBLE TO THIS PROJECT") {
		t.Errorf("an empty-looking brief over a non-empty store must say so:\n%s", out)
	}
	if !strings.Contains(out, "someoneelse") {
		t.Error("it must name the label the rows are actually under")
	}
}

// The counterpart of the test above: a store with no rows at all is a new brain, and the
// brief says that rather than raising the alarm.
func TestBriefOnATrulyEmptyBrainSaysThatInstead(t *testing.T) {
	s := newTestStore(t)
	out := brief(t, s, BriefArgs{})
	if !strings.Contains(out, "empty brain") {
		t.Errorf("expected the empty-brain line:\n%s", out)
	}
}

// A note from another project is shown with a marker saying it is information, not
// instruction. The spine is writable by every session on the machine, so whatever arrives
// through it is untrusted input.
func TestNotesFromOtherProjectsCarryATrustMarker(t *testing.T) {
	s := newTestStore(t)
	g := s.GlobalConnect(true)
	if g == nil {
		t.Fatal("no spine")
	}
	if _, err := g.Exec("INSERT INTO notes(to_project,from_project,body,ref,kind,created_at) "+
		"VALUES(?,?,?,'','note',?)", "testproj", "otherproj", "your backup drops measures", timefmt.Now()); err != nil {
		t.Fatal(err)
	}
	s.CloseGlobal(g)
	out := brief(t, s, BriefArgs{})
	if !strings.Contains(out, "your backup drops measures") {
		t.Fatalf("the note is missing:\n%s", out)
	}
	if !strings.Contains(out, "information, not") || !strings.Contains(out, "instruction") {
		t.Error("a note must be marked as information rather than instruction")
	}
}

// The brief cursor marks what this session has already seen, so the next brief can report
// what changed since. --no-cursor is for looking without moving it.
func TestBriefCursorAdvancesUnlessToldNotTo(t *testing.T) {
	s := newTestStore(t)
	brief(t, s, BriefArgs{NoCursor: true})
	if s.SessionCursor("last_brief_at") != 0 {
		t.Error("--no-cursor must not move the marker")
	}
	brief(t, s, BriefArgs{})
	if s.SessionCursor("last_brief_at") == 0 {
		t.Error("the marker should advance on a normal brief")
	}
}

// Tools that read the brief as JSON must get the same guarantees as a person reading text:
// every section is present, and pinned memories and guardrails survive the encoding.
func TestBriefJSONCarriesTheSameSections(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Type: "constraint", Title: "a binding rule", Body: "x"})
	remember(t, s, memory.RememberArgs{Title: "a pinned fact", Body: "x", Pin: true})
	var buf bytes.Buffer
	if err := s.Brief(&buf, BriefArgs{JSON: true}); err != nil {
		t.Fatal(err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("%v\n%s", err, buf.String())
	}
	for _, key := range []string{"project", "context", "version", "axioms", "pinned",
		"guardrails", "scopes", "recent"} {
		if _, ok := out[key]; !ok {
			t.Errorf("brief json is missing %q", key)
		}
	}
	if len(out["pinned"].([]interface{})) != 1 || len(out["guardrails"].([]interface{})) != 1 {
		t.Error("pinned and guardrails did not survive into json")
	}
}

// --- Versions and the project index ---

// Versions are yymmddNN and compare as integers. An unreadable version sorts as 0, so an
// unversioned copy looks behind rather than accidentally looking current.
func TestVersionComparisonTreatsUnreadableAsBehind(t *testing.T) {
	if spine.VersionInt("") != 0 || spine.VersionInt("nonsense") != 0 {
		t.Error("unreadable versions must sort as 0")
	}
	if !(spine.VersionInt("26072901") > spine.VersionInt("26072809")) {
		t.Error("yymmddNN must sort as an integer")
	}
	if !strings.Contains(spine.VersionHuman("26072901"), "2026-07-29") {
		t.Errorf("got %q", spine.VersionHuman("26072901"))
	}
}

// Every project registers itself in the spine's index so other projects on the machine can
// see that it exists and how big it is, and nothing about what it holds.
func TestProjectRegistersItselfInTheIndexWithNoMemoryContents(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Title: "a secret-ish scope name", Body: "x", Scope: "global"})
	s.RegisterProject()

	g := s.GlobalConnect(false)
	if g == nil {
		t.Fatal("no spine")
	}
	defer s.CloseGlobal(g)
	var name, scopes, description string
	var memories int
	if err := g.QueryRow("SELECT name, scopes, description, memories FROM projects WHERE name=?",
		"testproj").Scan(&name, &scopes, &description, &memories); err != nil {
		t.Fatal(err)
	}
	if memories != 1 {
		t.Errorf("the index should carry the count, got %d", memories)
	}
	// Scope names are a project's own taxonomy and are often sensitive (a person's name, a
	// medical condition, a client). They are never written to the index, because a value
	// withheld only at render time still sits in a shared file anyone can open.
	if scopes != "" {
		t.Errorf("scope names must never reach the shared index, got %q", scopes)
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

// The full report needs a command that nobody runs in a project whose subject is not the
// brain, and "nobody runs it" is exactly the condition under which an omission survives.
// So the brief carries a one-line summary.
func TestTheBriefCarriesTheOneLineSummary(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < curate.OmissionFloor+1; i++ {
		remember(t, s, memory.RememberArgs{Title: titleN(i), Body: "a body long enough to be a " +
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

// titleN returns a distinct, realistic memory title for index i.
func titleN(i int) string {
	return []string{"the deploy runbook", "how billing is wired", "why postgres here",
		"the nightly job", "what the api returns", "where the certs live",
		"the rate limiter"}[i%7] + " " + string(rune('a'+i))
}
