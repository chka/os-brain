package hook

import (
	"io"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/memory"
)

// Capture records what the user typed, one row per utterance, with a class that says what kind
// of sentence it was. The class decides what may later be promoted into a lasting rule, so a
// misclassification is not cosmetic: a pasted example read as a directive would put words the
// user never said into their own preferences.

// --- the classifier -----------------------------------------------------------------

// A bare imperative is a one-off request for this turn, not a rule for every turn.
func TestABareImperativeClassifiesAsACommand(t *testing.T) {
	c := Classify("push the code")
	if c.Class != "command" {
		t.Fatalf("want command, got %+v", c)
	}
}

// A statement of liking is taste: worth knowing about the person, never binding.
func TestALikingClassifiesAsTaste(t *testing.T) {
	c := Classify("I love golang")
	if c.Class != "taste" {
		t.Fatalf("want taste, got %+v", c)
	}
}

// "From now on always..." is the shape of a standing rule, the one class that may harden into
// a preference.
func TestAStandingRuleClassifiesAsADirective(t *testing.T) {
	c := Classify("from now on always push after each change")
	if c.Class != "directive" {
		t.Fatalf("want directive, got %+v", c)
	}
}

// "For example, here is..." introduces material the user is showing, not asserting.
func TestAnExampleClassifiesAsAnExhibit(t *testing.T) {
	c := Classify("for example, here is what session 1 did")
	if c.Class != "exhibit" {
		t.Fatalf("want exhibit, got %+v", c)
	}
}

// The asymmetry that makes the classifier worth having: the same subject in command form and
// in directive form must land on opposite sides, or every request would become a rule.
func TestTheSameSubjectAsCommandAndAsDirectiveLandsOnOppositeSides(t *testing.T) {
	cmd := Classify("push the code")
	if cmd.Class != "command" {
		t.Fatalf("want command, got %+v", cmd)
	}
	dir := Classify("always push after each change")
	if dir.Class != "directive" {
		t.Fatalf("want directive, got %+v", dir)
	}
}

// The important case: a pasted transcript containing a directive-shaped line must not become
// the user's own rule. Exhibit is checked before directive for exactly this.
func TestAFencedPasteIsAnExhibitEvenWithADirectiveInside(t *testing.T) {
	pasted := "```\nsomeone wrote: from now on always push after each change\n```"
	c := Classify(pasted)
	if c.Class != "exhibit" {
		t.Fatalf("a fenced paste containing a directive cue must classify as exhibit, got %+v", c)
	}
}

// A quote-prefixed line with a directive cue inside it is still an exhibit: the user is
// showing something, not asserting it.
func TestAQuotedDirectiveIsAnExhibit(t *testing.T) {
	quoted := "> always push after each change, said another agent"
	c := Classify(quoted)
	if c.Class != "exhibit" {
		t.Fatalf("want exhibit, got %+v", c)
	}
}

// The cues are English only. The same directive shape in another language must not be
// recognised as a rule by accident, and the language is reported as "other" so the miss can
// be counted rather than hidden. Such text degrades to the harmless class, command.
func TestNonEnglishTextIsNeverReadAsADirective(t *testing.T) {
	greek := "Πάντα κάνε push τον κώδικα μετά από κάθε αλλαγή"
	c := Classify(greek)
	if c.Lang != "other" {
		t.Fatalf("want lang=other, got %q", c.Lang)
	}
	if c.Class == "directive" {
		t.Fatalf("Greek text must not be classified as a directive, got %+v", c)
	}
	if c.Class != "command" {
		t.Fatalf("non-English text degrades to command, got %+v", c)
	}
}

// --- storing utterances -------------------------------------------------------------

// The stored row carries the class, the session and the project, which is everything a later
// pass needs to decide whether the sentence was repeated across sessions.
func TestCaptureStoresTheUtteranceWithItsClassSessionAndProject(t *testing.T) {
	s := newTestStore(t)
	res, err := s.Capture("always push after each change", "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Stored {
		t.Fatalf("expected stored, got %+v", res)
	}
	if res.Class != "directive" {
		t.Fatalf("want directive, got %+v", res)
	}
	if res.ID == 0 {
		t.Fatalf("expected a nonzero id, got %+v", res)
	}

	var class, session, project string
	err = s.DB.QueryRow("SELECT class, session, project FROM utterances WHERE id=?", res.ID).
		Scan(&class, &session, &project)
	if err != nil {
		t.Fatal(err)
	}
	if class != "directive" || session != "sess-1" || project != "testproj" {
		t.Fatalf("unexpected row: class=%q session=%q project=%q", class, session, project)
	}
}

// Capture runs on every prompt, so whitespace-only input is common and must leave no row.
func TestCaptureOfBlankTextStoresNothing(t *testing.T) {
	s := newTestStore(t)
	res, err := s.Capture("   ", "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Stored {
		t.Fatalf("expected nothing stored, got %+v", res)
	}
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM utterances").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected zero rows, got %d", n)
	}
}

// People paste keys into prompts. A credential is refused and nothing is stored, but the turn
// is never failed: Capture runs from a hook, and an error there would block the user's prompt
// over a store it did not ask to write.
func TestCaptureRefusesACredentialWithoutFailingTheTurn(t *testing.T) {
	s := newTestStore(t)
	res, err := s.Capture("here's the key: AKIAABCDEFGHIJKLMNOP", "sess-1")
	if err != nil {
		t.Fatalf("capture must never error the turn, got %v", err)
	}
	if res.Stored {
		t.Fatalf("expected refusal, got stored: %+v", res)
	}
	if res.Refused == "" {
		t.Fatalf("expected a refusal reason, got %+v", res)
	}
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM utterances").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected zero rows stored on refusal, got %d", n)
	}
}

// Utterances are never deduplicated: the same sentence captured twice is two rows, because
// the repetition itself is the signal the ladder reads when it decides a rule is settled.
func TestCaptureNeverDeduplicates(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Capture("always push after each change", "sess-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Capture("always push after each change", "sess-2"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRow(
		"SELECT COUNT(*) FROM utterances WHERE text=?", "always push after each change",
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected two rows for the repeated utterance, got %d", n)
	}
}

// An empty session argument falls back to the environment's session id rather than storing an
// empty string, so every row can be attributed to a session.
func TestCaptureFallsBackToTheEnvironmentsSession(t *testing.T) {
	s := newTestStore(t)
	res, err := s.Capture("push the code", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Stored {
		t.Fatalf("expected stored, got %+v", res)
	}
	var session string
	if err := s.DB.QueryRow("SELECT session FROM utterances WHERE id=?", res.ID).
		Scan(&session); err != nil {
		t.Fatal(err)
	}
	if session != s.Env.SessionID() {
		t.Fatalf("want fallback session %q, got %q", s.Env.SessionID(), session)
	}
}

// --everywhere asks for a memory that reaches every project through an organisation above
// them, which only the team edition has. Storing it in this project and reporting success
// would tell the caller a memory reaches every project when it reaches one, so a local brain
// refuses the flag and says what is missing.
func TestEverywhereRefusesOnALocalBrain(t *testing.T) {
	s := newTestStore(t)
	err := s.Remember(io.Discard, memory.RememberArgs{
		Title: "one legal entity sits behind every project", Body: "b",
		Type: "infra", Scope: "global", Source: "test", Everywhere: true,
	})
	if err == nil {
		t.Fatal("--everywhere was accepted on a local brain")
	}
	if !strings.Contains(err.Error(), "no org above it") {
		t.Errorf("the refusal does not explain what is missing: %v", err)
	}
}
