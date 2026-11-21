package cli

import (
	"os"
	"strings"
	"testing"
)

// Each agent host runs the brain's hooks with its own payload shape and its own way of
// reading what a hook prints. These tests pin how the hooks tell the hosts apart and answer
// each one in a shape it actually reads: a hook whose output a host ignores fails silently,
// and the session simply never sees its brief or its recall.

// Claude Code exports CLAUDE_CODE_SESSION_ID; Cursor passes session_id only in the stdin
// payload. Without reading the payload every Cursor hook was a session of its own, keyed by
// its own pid and no session was ever seen to end. When the host does export an id, that
// id wins: the payload must not override it. A payload with no usable id adopts nothing.
func TestHookPayloadSuppliesTheSessionOnlyWhenTheHostDidNot(t *testing.T) {
	cursor := []byte(`{"hook_event_name":"sessionEnd","session_id":"638dc491","cursor_version":"1"}`)

	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("OS_BRAIN_SESSION_ID", "")
	adoptHookSession(cursor)
	if got := os.Getenv("OS_BRAIN_SESSION_ID"); got != "638dc491" {
		t.Fatalf("cursor payload: OS_BRAIN_SESSION_ID=%q", got)
	}

	t.Setenv("OS_BRAIN_SESSION_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "claude-own")
	adoptHookSession(cursor)
	if got := os.Getenv("OS_BRAIN_SESSION_ID"); got != "" {
		t.Fatalf("host exported its id, payload must not override: OS_BRAIN_SESSION_ID=%q", got)
	}

	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	for _, p := range [][]byte{nil, []byte("junk"), []byte(`{"session_id":"  "}`)} {
		adoptHookSession(p)
		if got := os.Getenv("OS_BRAIN_SESSION_ID"); got != "" {
			t.Fatalf("%q adopted %q", p, got)
		}
	}
	if !hookEvent(cursor, "sessionEnd") || hookEvent(cursor, "stop") || hookEvent(nil, "sessionEnd") {
		t.Fatal("hookEvent misread the payload")
	}
}

// --emit picks the JSON envelope a host needs. A typo, or an envelope this hook has no use
// for, would otherwise fall back to plain output that the host never reads, so it is
// refused instead. The hermes envelope is valid only on the hook Hermes runs.
func TestHookEmitRefusesAnEnvelopeNoHostNeeds(t *testing.T) {
	if hookEmitOK("", false) != nil || hookEmitOK("gemini", false) != nil || hookEmitOK("hermes", true) != nil {
		t.Fatal("plain and gemini must be accepted and so must hermes on the hook Hermes runs")
	}
	if hookEmitOK("cursor", true) == nil || hookEmitOK("gemni", false) == nil || hookEmitOK("hermes", false) == nil {
		t.Fatal("a typo or a host without this hook must be refused, not silently plain")
	}
}

// Copilot CLI runs .claude/settings.json with Claude Code's payloads but reads context only
// from a JSON additionalContext field, so plain output reached nothing. COPILOT_CLI alone is
// not proof: a Claude Code started from a Copilot shell inherits it. Claude Code sends
// transcript_path on every event and Copilot does not, so the variable and the payload must
// agree. An empty hook prints nothing at all rather than an empty object.
func TestCopilotIsToldApartFromClaudeCodeByEnvAndPayloadTogether(t *testing.T) {
	copilot := []byte(`{"hook_event_name":"SessionStart","session_id":"bd95","source":"new","initial_prompt":"hi"}`)
	claude := []byte(`{"hook_event_name":"SessionStart","session_id":"x","transcript_path":"/t.jsonl","source":"startup"}`)
	t.Setenv("COPILOT_CLI", "")
	if copilotHook(copilot) {
		t.Fatal("no COPILOT_CLI, so not Copilot")
	}
	t.Setenv("COPILOT_CLI", "1")
	if !copilotHook(copilot) {
		t.Fatal("Copilot's session-start payload was not recognised")
	}
	if copilotHook(claude) {
		t.Fatal("a Claude Code payload under an inherited COPILOT_CLI was taken for Copilot")
	}
	var buf strings.Builder
	copilotEnvelope(&buf, "the brief\n")
	if got := strings.TrimSpace(buf.String()); got != `{"additionalContext":"the brief"}` {
		t.Fatalf("envelope %s", got)
	}
	buf.Reset()
	copilotEnvelope(&buf, "  \n")
	if buf.Len() != 0 {
		t.Fatal("an empty hook must print nothing, not an empty object")
	}
}

// Hermes puts the prompt in extra.user_message and flags the first turn itself. The payload
// is rewritten into the prompt and session_id shape hook-recall reads and only the first
// turn is reported as first, so the brief is given once per session and not on every turn.
func TestHermesPayloadBecomesTheShapeHookRecallReads(t *testing.T) {
	in := []byte(`{"hook_event_name":"pre_llm_call","session_id":"local-a2","cwd":"/home/lab/.hermes/hermes-agent",` +
		`"extra":{"user_message":"what colour is the mascot?","is_first_turn":true}}`)
	out, first := hermesPayload(in)
	if !first {
		t.Fatal("the first turn, which carries the brief, was not seen")
	}
	if got := string(out); !strings.Contains(got, `"prompt":"what colour is the mascot?"`) ||
		!strings.Contains(got, `"session_id":"local-a2"`) {
		t.Fatalf("rewritten payload %s", got)
	}
	if _, first := hermesPayload([]byte(`{"extra":{"user_message":"again","is_first_turn":false}}`)); first {
		t.Fatal("a later turn would be briefed again")
	}
}

// Devin CLI also runs .claude/settings.json, sets DEVIN_PROJECT_DIR and reads context only
// from Claude's nested hookSpecificOutput object: plain stdout and Copilot's flat
// additionalContext both reached nothing. It is told apart from Claude Code by the same
// variable-plus-payload check as Copilot.
func TestDevinIsToldApartFromClaudeCodeAndAnsweredInNestedJSON(t *testing.T) {
	devin := []byte(`{"hook_event_name":"UserPromptSubmit","prompt":"hi","session_id":"cyan-hugger","prompt_id":"1"}`)
	claude := []byte(`{"hook_event_name":"UserPromptSubmit","prompt":"hi","session_id":"x","transcript_path":"/t"}`)
	t.Setenv("DEVIN_PROJECT_DIR", "")
	if devinHook(devin) {
		t.Fatal("no DEVIN_PROJECT_DIR, so not Devin")
	}
	t.Setenv("DEVIN_PROJECT_DIR", "/p")
	if !devinHook(devin) || devinHook(claude) {
		t.Fatal("Devin and Claude Code payloads were not told apart")
	}
	var buf strings.Builder
	devinEnvelope(&buf, "UserPromptSubmit", "recall\n")
	if got := strings.TrimSpace(buf.String()); got !=
		`{"hookSpecificOutput":{"additionalContext":"recall","hookEventName":"UserPromptSubmit"}}` {
		t.Fatalf("envelope %s", got)
	}
}
