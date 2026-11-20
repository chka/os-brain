package brain

// Tests for `os-brain open`, the answer to "what is outstanding on this project?".
//
// The failure this command exists to stop: asked that question, a session read the brief,
// distrusted it, and rebuilt the answer from dozens of commit subjects, a TODO file of
// nearly two thousand lines, a listing of every spec and a query against production. So
// `open` answers from tasks and unfinished-work memories, and ends by saying what checking
// the answer would cost.

import (
	"bytes"
	"strings"
	"testing"
)

// openText renders `os-brain open` for s and fails the test if it errors.
func openText(t *testing.T, s *Store, a OpenArgs) string {
	t.Helper()
	var buf bytes.Buffer
	if err := s.Open(&buf, a); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// The answer has to arrive with its cost of verification attached, naming the commands
// that would check or correct it. Without that closing block, the answer is just more
// input to a decision the session already made: to go and re-derive everything.
func TestOpenEndsWithWhatVerifyingItWouldCost(t *testing.T) {
	s := newTestStore(t)
	got := openText(t, s, OpenArgs{})
	for _, want := range []string{
		"THAT IS THE ANSWER",
		"git log",
		"os-brain verify <id>",
		"--supersedes <id>",
		"os-brain task done <id>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the closing budget is missing %q:\n%s", want, got)
		}
	}
}

// An empty task list must not be rendered as "nothing is outstanding". A memory records
// what was BELIEVED when it was written, never whether the work finished, so a brain with
// no tasks genuinely cannot answer the question. Saying so, and naming the command that
// fixes it, is the only thing that gets tasks filed.
func TestOpenSaysWhyItIsThinWhenNoTaskWasEverFiled(t *testing.T) {
	s := newTestStore(t)
	got := openText(t, s, OpenArgs{})
	if !strings.Contains(got, "none filed") {
		t.Errorf("an empty task list must say so:\n%s", got)
	}
	if !strings.Contains(got, "os-brain task add") {
		t.Errorf("it must name the command that fixes it:\n%s", got)
	}
}

// A closed task is not outstanding, however recently it closed.
func TestOpenListsOpenTasksAndNotClosedOnes(t *testing.T) {
	s := newTestStore(t)
	var buf bytes.Buffer
	for _, title := range []string{"still to do", "already shipped"} {
		if err := s.Task(&buf, TaskArgs{Sub: "add", Title: title}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Task(&buf, TaskArgs{Sub: "done", ID: 2}); err != nil {
		t.Fatal(err)
	}
	got := openText(t, s, OpenArgs{})
	// The task section only: the event log below it legitimately records the closing.
	tasks := got[strings.Index(got, "OPEN TASKS"):strings.Index(got, "WHAT ACTUALLY HAPPENED")]
	if !strings.Contains(tasks, "still to do") {
		t.Errorf("the open task is missing:\n%s", got)
	}
	if strings.Contains(tasks, "already shipped") {
		t.Errorf("a closed task is not outstanding:\n%s", got)
	}
}

// state and commitment are the two memory types that describe unfinished business, so
// they are shown and infra is not. Superseded rows are left out for the reason the whole
// command exists: a memory that has been corrected is worse than no memory, and
// republishing it here would commit the exact error `open` was built to end.
func TestOpenCarriesUnfinishedWorkAndNotCorrectedClaims(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "mid-way through the migration", Type: "state",
		Body: "half the tables are moved"})
	remember(t, s, RememberArgs{Title: "promised to rotate the key", Type: "commitment",
		Body: "before the appliance ships"})
	remember(t, s, RememberArgs{Title: "nothing implemented yet", Type: "state",
		Body: "the whole feature is unstarted"})
	remember(t, s, RememberArgs{Title: "all of it shipped on the 31st", Type: "state",
		Body: "every item is out", Supersedes: 3})
	remember(t, s, RememberArgs{Title: "the port is 8443", Type: "infra",
		Body: "on the edge box"})

	got := openText(t, s, OpenArgs{})
	for _, want := range []string{"mid-way through the migration", "promised to rotate the key"} {
		if !strings.Contains(got, want) {
			t.Errorf("unfinished work is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "nothing implemented yet") {
		t.Errorf("a superseded claim was republished as current:\n%s", got)
	}
	if strings.Contains(got, "the port is 8443") {
		t.Errorf("infra answers \"what is true\", not \"what is unfinished\":\n%s", got)
	}
}

// The prompt hook answers these questions with the `open` block. A session that guesses
// the wrong wording must not conclude that the brain has no answer. Widening the list is
// cheap (a false positive costs about fifteen lines of context) and a miss costs the whole
// feature. Ordinary work requests must not trigger it.
func TestTheQuestionsThatMeanWhatIsUnfinished(t *testing.T) {
	for _, q := range []string{
		"what's outstanding?",
		"what is outstanding on this project",
		"whats next",
		"what's next?",
		"what is left to do here",
		"anything still open?",
		"where did we leave off",
		"where do things stand",
		"what should I work on",
		"catch me up",
		"τι εκκρεμεί;",
		"πού είχαμε μείνει",
	} {
		if !IsOpenQuestion(q) {
			t.Errorf("%q asks what is unfinished and was not recognised", q)
		}
	}
	for _, q := range []string{
		"fix the bug in route.go please",
		"what does RenderTask do",
		"open the config file",
		"remember that the port is 8443",
		"why is the test failing",
	} {
		if IsOpenQuestion(q) {
			t.Errorf("%q is ordinary work and must not trigger the block", q)
		}
	}
}

// The hook checks for a standing question BEFORE its minimum-length gate, on purpose:
// "what's next?" is twelve characters and is exactly the question this exists for. The gate
// decides whether a message is worth a retrieval, which has nothing to do with whether it
// asks something the brain already knows.
func TestTheHookAnswersAShortStandingQuestion(t *testing.T) {
	s := newTestStore(t)
	var buf bytes.Buffer
	if err := s.Task(&buf, TaskArgs{Sub: "add", Title: "finish the round-trip audit"}); err != nil {
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

// The default is the CHEAP answer: task bodies are what `task list` is for, and on a real
// project a handful of open tasks can carry kilobytes of body between them. --full is the
// way to get them.
func TestOpenKeepsTaskBodiesOutOfTheDefaultAnswer(t *testing.T) {
	s := newTestStore(t)
	var buf bytes.Buffer
	if err := s.Task(&buf, TaskArgs{Sub: "add", Title: "the audit",
		Body: "a very long rig description that nobody asked for"}); err != nil {
		t.Fatal(err)
	}
	if got := openText(t, s, OpenArgs{}); strings.Contains(got, "nobody asked for") {
		t.Errorf("the default answer carried a task body:\n%s", got)
	}
	if got := openText(t, s, OpenArgs{Full: true}); !strings.Contains(got, "nobody asked for") {
		t.Errorf("--full must print them:\n%s", got)
	}
}
