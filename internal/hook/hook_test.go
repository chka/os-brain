package hook

import (
	"bytes"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/memory"
	"github.com/chka/os-brain/internal/tasks"
	"github.com/chka/os-brain/internal/timefmt"
)

// Tests for the UserPromptSubmit hook (hook-recall) and for `os-brain prefer`.
//
// The hook fires on every message the user sends. It leads with the clock, recalls what the
// prompt is about, and nudges the agent to store a standing preference when the user states
// one. Because it runs on every turn, a false positive is noise in every turn and teaches
// people to skip the block, so many of these tests check that it stays quiet.

// hook feeds prompt to the hook as the host would (a JSON object on stdin) and returns
// what the hook printed.
func hook(t *testing.T, s *Store, prompt string, a HookRecallArgs) string {
	t.Helper()
	var buf bytes.Buffer
	in := strings.NewReader(`{"prompt":` + jsonString(prompt) + `}`)
	if err := s.HookRecall(in, &buf, a); err != nil {
		t.Fatalf("hook-recall: %v", err)
	}
	return buf.String()
}

// jsonString quotes s as a JSON string. It escapes only what the test prompts contain and
// leaves non-ASCII text as it is, so Greek prompts reach the hook unchanged.
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// fillerMemories gives the corpus enough documents for bm25 to discriminate. Below about
// ten rows a term in one of two documents has zero IDF and scores exactly like a non-match,
// which is why the hook has a score floor. Below three rows the floor is waived instead
// (see TestABrandNewBrainRecallsOnAPromptAndStaysQuietOffTopic).
func fillerMemories(t *testing.T, s *Store) {
	t.Helper()
	for _, ti := range []string{"the release cadence is fortnightly", "invoices go out on the first",
		"the office wifi drops at noon", "standups are on tuesdays", "the logo font is inter",
		"expenses need a receipt", "the backup window is 0300", "staging mirrors production"} {
		remember(t, s, memory.RememberArgs{Title: ti, Body: "detail about " + ti})
	}
}

// --- The clock ---

// The hook's first line is the wall clock, and it says so when a long gap has passed since
// the last prompt. A session cannot otherwise tell that the user went to bed and came back
// the next morning.
func TestHookLeadsWithTheClockAndSaysWhenTimeHasPassed(t *testing.T) {
	s := newTestStore(t)
	out := hook(t, s, "a prompt long enough to pass the floor", HookRecallArgs{})
	if !strings.HasPrefix(out, "[brain] ") || !strings.Contains(out, ":") {
		t.Errorf("the clock line must lead: %q", out)
	}
	// A second prompt straight away is not an idle gap.
	if strings.Contains(hook(t, s, "another prompt of sufficient length", HookRecallArgs{}),
		"clock has moved") {
		t.Error("no gap should be reported between back-to-back prompts")
	}
	// ...but a stale marker is.
	if _, err := s.DB.Exec("INSERT OR REPLACE INTO meta(k,v) VALUES(?,?)",
		s.ProjectKey(s.SessionKey("last_prompt_at")), timefmt.Now()-7200); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hook(t, s, "back after a long silence, here we go", HookRecallArgs{}),
		"clock has moved") {
		t.Error("a two-hour gap should be called out")
	}
}

// --- Recall on a prompt ---

// "what is the weather like today" once matched on 'the' and 'is' and injected three
// unrelated memories. A prompt about nothing the brain holds gets silence.
func TestHookSaysNothingOnAPromptAboutNothingItHolds(t *testing.T) {
	s := newTestStore(t)
	for _, ti := range []string{"nginx terminates TLS on 8443", "postgres runs on the db host",
		"the deploy script lives in bin/deploy"} {
		remember(t, s, memory.RememberArgs{Type: "infra", Title: ti, Body: "detail"})
	}
	out := hook(t, s, "what is the weather like today", HookRecallArgs{NoClock: true})
	if out != "" {
		t.Errorf("expected silence, got:\n%s", out)
	}
}

// The matching memory surfaces and an unrelated one that shares only filler words does not.
func TestHookSurfacesWhatThePromptIsActuallyAbout(t *testing.T) {
	s := newTestStore(t)
	fillerMemories(t, s)
	remember(t, s, memory.RememberArgs{Type: "infra", Title: "nginx terminates TLS on 8443",
		Body: "certs renew via acme"})
	remember(t, s, memory.RememberArgs{Type: "infra", Title: "postgres runs on the db host", Body: "x"})
	out := hook(t, s, "why is nginx refusing connections today", HookRecallArgs{NoClock: true})
	if !strings.Contains(out, "nginx terminates TLS") {
		t.Errorf("the matching memory was not surfaced:\n%s", out)
	}
	if strings.Contains(out, "postgres") {
		t.Errorf("an unrelated memory was injected:\n%s", out)
	}
}

// Recall reinforces what it returns (use_count feeds ranking). The hook must not: counting
// an injection as a use is a feedback loop that promotes whatever the hook guessed, rather
// than what anyone asked for.
func TestHookNeverReinforces(t *testing.T) {
	s := newTestStore(t)
	fillerMemories(t, s)
	remember(t, s, memory.RememberArgs{Type: "infra", Title: "nginx terminates TLS on 8443", Body: "x"})
	out := hook(t, s, "why is nginx refusing connections today", HookRecallArgs{NoClock: true})
	if !strings.Contains(out, "nginx") {
		t.Fatalf("nothing was surfaced, so this proves nothing:\n%s", out)
	}
	var used int
	if err := s.DB.QueryRow("SELECT use_count FROM memories WHERE title LIKE 'nginx%'").Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != 0 {
		t.Errorf("the hook reinforced a memory: use_count=%d", used)
	}
}

// Private and lead: scopes are shown only when asked for by name. The hook asks for
// nothing, so it must never put them into the model's context on its own initiative.
func TestHookNeverVolunteersAPrivateScope(t *testing.T) {
	s := newTestStore(t)
	fillerMemories(t, s)
	remember(t, s, memory.RememberArgs{Scope: "private", Type: "infra",
		Title: "the vault unseal ritual for nginx", Body: "x"})
	remember(t, s, memory.RememberArgs{Scope: "lead:acme", Type: "note",
		Title: "acme wants nginx tuning", Body: "x"})
	remember(t, s, memory.RememberArgs{Type: "infra", Title: "nginx terminates TLS on 8443", Body: "x"})
	out := hook(t, s, "tell me about the nginx setup here", HookRecallArgs{NoClock: true})
	if strings.Contains(out, "unseal") || strings.Contains(out, "acme") {
		t.Errorf("a private scope reached the model's context:\n%s", out)
	}
	if !strings.Contains(out, "terminates TLS") {
		t.Errorf("the public memory should still surface:\n%s", out)
	}
}

// A very short prompt ("nginx?", "ok", "yes") is not worth a retrieval.
func TestHookIgnoresAVeryShortPrompt(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Type: "infra", Title: "nginx on 8443", Body: "x"})
	out := hook(t, s, "nginx?", HookRecallArgs{NoClock: true})
	if out != "" {
		t.Errorf("expected silence under min-chars, got %q", out)
	}
}

// A small brain of terse technical prose contains "project" far more often than "what" or
// "does", so a document-frequency gate on its own dropped the only topical term and kept
// the filler. Question filler words are removed first.
func TestDiscriminatingQueryDropsFillerBeforeFrequency(t *testing.T) {
	s := newTestStore(t)
	for _, ti := range []string{"the project ships on friday", "the project uses postgres",
		"the project has a viewer", "honeypot banning is fleet-wide"} {
		remember(t, s, memory.RememberArgs{Title: ti, Body: "body", Importance: 3})
	}
	q, err := s.discriminatingQuery("what does this project do")
	if err != nil {
		t.Fatal(err)
	}
	for _, filler := range []string{"what", "does", "this"} {
		if strings.Contains(q, filler) {
			t.Errorf("filler %q survived into %q", filler, q)
		}
	}
	q2, err := s.discriminatingQuery("is honeypot banning working")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q2, "honeypot") {
		t.Errorf("the topical term was dropped: %q", q2)
	}
}

// With one memory bm25 has nothing to compare against and every score sat under the floor,
// so the first fact anybody stored never came back on a prompt: the check the setup guide
// tells a new user to run failed on every host. Waiving the floor must not make the hook
// inject on an off-topic prompt.
func TestABrandNewBrainRecallsOnAPromptAndStaysQuietOffTopic(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Type: "knowledge", Title: "Mascot colour for the release banner",
		Body: "The release banner mascot is painted magenta.", Importance: 1})
	out := hook(t, s, "what colour is the release banner mascot painted?", HookRecallArgs{NoClock: true})
	if !strings.Contains(out, "magenta") {
		t.Errorf("a one-memory brain did not recall its only matching memory:\n%s", out)
	}
	if out := hook(t, s, "what is the weather like today", HookRecallArgs{NoClock: true}); out != "" {
		t.Errorf("an off-topic prompt injected into a one-memory brain:\n%s", out)
	}
}

// --- The preference nudge ---

// The nudge fires on stated rules in English and Greek. Go's \b is ASCII-only, so a literal
// translation of a cue like `\b(?:μη|μην)` never fires: the boundary is tested against μ,
// which RE2 does not treat as a word character. The Greek half of the feature was silently
// dead until this test existed.
func TestPreferenceNudgeFiresInEnglishAndGreek(t *testing.T) {
	for _, prompt := range []string{
		"from now on always run the tests before committing",
		"please don't add comments explaining what the code does",
		"μην μου γράφεις em dashes στα κείμενα",
		"πάντα να τρέχεις τα τεστ πριν το commit",
		"προτιμώ Go για νέα εργαλεία υποδομής",
	} {
		s := newTestStore(t)
		out := hook(t, s, prompt, HookRecallArgs{NoClock: true})
		if !strings.Contains(out, "standing preference") {
			t.Errorf("no nudge for %q", prompt)
		}
	}
}

// A nudge that fires on ordinary technical prose trains everyone to ignore it, and then the
// real ones get ignored with it.
func TestPreferenceNudgeStaysQuietOnOrdinaryProse(t *testing.T) {
	for _, prompt := range []string{
		"the deploy failed again on the staging box",
		"can you check why the migration is slow",
		"τι ώρα είναι τώρα στην Αθήνα",
	} {
		s := newTestStore(t)
		if strings.Contains(hook(t, s, prompt, HookRecallArgs{NoClock: true}), "standing preference") {
			t.Errorf("false nudge on %q", prompt)
		}
	}
}

// The nudge fires once per cooldown, not on every message, and not at all for a rule that
// is already stored.
func TestPreferenceNudgeIsRateLimitedAndSilentWhenAlreadyStored(t *testing.T) {
	s := newTestStore(t)
	prompt := "from now on always run the tests before committing"
	if !strings.Contains(hook(t, s, prompt, HookRecallArgs{NoClock: true}), "standing preference") {
		t.Fatal("the first nudge should fire")
	}
	if strings.Contains(hook(t, s, prompt, HookRecallArgs{NoClock: true}), "standing preference") {
		t.Error("a reminder on every message is nagging, not a mechanism")
	}

	// Once it is stored, a rewording of the same rule must stay silent even after the
	// cooldown: an AND over every content word would never match a rewording, and an OR
	// would let one stopword silence the nudge forever.
	s2 := newTestStore(t)
	if err := s2.Prefer(bytes.NewBuffer(nil), PreferArgs{
		Text: "always run the tests before committing", Why: "broke main twice"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hook(t, s2, "from now on always run the tests before committing anything",
		HookRecallArgs{NoClock: true}), "standing preference") {
		t.Error("nudged for a preference that is already stored")
	}
}

// The nudge once hung off the recall path, so a prompt that matched no memory was also a
// preference that went uncaptured. An empty brain must still capture the rule.
func TestPreferenceCaptureIsIndependentOfWhetherAnythingMatched(t *testing.T) {
	s := newTestStore(t)
	out := hook(t, s, "from now on never use em dashes in anything you write",
		HookRecallArgs{NoClock: true})
	if !strings.Contains(out, "standing preference") {
		t.Errorf("an empty brain still has to capture the rule:\n%s", out)
	}
}

// --- os-brain prefer ---

// `prefer` stores a rule as a dated preference with its reason, at an importance above a
// neutral fact. --about turns it into a fact about a person (a relationship memory) rather
// than a rule for the assistant.
func TestPreferStoresARuleWithItsReason(t *testing.T) {
	s := newTestStore(t)
	var buf bytes.Buffer
	if err := s.Prefer(&buf, PreferArgs{Text: "never ask whether to continue",
		Why: "asking wastes the user's time", Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	m, err := s.Own(1, "read")
	if err != nil {
		t.Fatal(err)
	}
	if m.MemType != "preference" {
		t.Errorf("type is %q", m.MemType)
	}
	if m.Confidence != "confirmed" {
		t.Errorf("--confirmed did not stick: %q", m.Confidence)
	}
	if m.Importance != 4 {
		t.Errorf("a stated rule should outrank a neutral fact, got importance %d", m.Importance)
	}
	if m.Body != "asking wastes the user's time" {
		t.Errorf("the reason was dropped: %q", m.Body)
	}
	if m.SourceDate == "" {
		t.Error("a preference must be dated, or nothing can age it")
	}
	if err := s.Prefer(&buf, PreferArgs{Text: "prefers morning calls", About: "Alice"}); err != nil {
		t.Fatal(err)
	}
	m2, err := s.Own(2, "read")
	if err != nil {
		t.Fatal(err)
	}
	if m2.MemType != "relationship" {
		t.Errorf("--about should file it as a relationship, got %q", m2.MemType)
	}
}

// Gemini folds earlier hook output into the prompt it passes the next hook, wrapped in
// <hook_context>. Recall must search for what the user typed, not for the brain's own brief.
func TestHookPromptTextDropsWhatGeminiFoldedIntoThePrompt(t *testing.T) {
	in := "<hook_context>## THIS PROJECT HAS A BRAIN\nAsked what is outstanding</hook_context>\nwhat colour is the mascot?"
	if got := HookPromptText(in); got != "what colour is the mascot?" {
		t.Fatalf("got %q", got)
	}
	if got := HookPromptText("  plain prompt "); got != "plain prompt" {
		t.Fatalf("got %q", got)
	}
}

// The hook checks for a standing question BEFORE its minimum-length gate, on purpose:
// "what's next?" is twelve characters and is exactly the question this exists for. The gate
// decides whether a message is worth a retrieval, which has nothing to do with whether it
// asks something the brain already knows.
func TestTheHookAnswersAShortStandingQuestion(t *testing.T) {
	s := newTestStore(t)
	var buf bytes.Buffer
	if err := s.Task(&buf, tasks.TaskArgs{Sub: "add", Title: "finish the round-trip audit"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := s.HookRecall(strings.NewReader(`{"prompt":"what's next?"}`), &out,
		HookRecallArgs{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "finish the round-trip audit") {
		t.Errorf("the hook did not answer a standing question:\n%s", out.String())
	}

	var quiet bytes.Buffer
	if err := s.HookRecall(strings.NewReader(`{"prompt":"fix the bug in route.go please"}`),
		&quiet, HookRecallArgs{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(quiet.String(), "WHAT IS OPEN") {
		t.Errorf("ordinary work triggered the block:\n%s", quiet.String())
	}
}
