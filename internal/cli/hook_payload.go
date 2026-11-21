package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/chka/os-brain/internal/brief"
	"github.com/chka/os-brain/internal/store"
)

// hookStdin is the payload a host hands its hook, read only when one can be there: never
// over MCP, never from a terminal or /dev/null, and never for longer than a hook can wait,
// so `os-brain wakeup` typed by hand or piped from a stream that never closes still runs.
func hookStdin() []byte {
	if stdinIsProtocol {
		return nil
	}
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice != 0 {
		return nil
	}
	got := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
		got <- b
	}()
	select {
	case b := <-got:
		return b
	case <-time.After(2 * time.Second):
		return nil
	}
}

// adoptHookSession takes the session id from a hook payload when the host does not export
// one. Claude Code sets CLAUDE_CODE_SESSION_ID; Cursor passes session_id on stdin only, so
// without this every Cursor hook was its own session, keyed by its own pid, and no session
// was ever seen to end.
func adoptHookSession(payload []byte) {
	if store.SessionFromEnv() {
		return
	}
	var p struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(payload, &p) == nil && strings.TrimSpace(p.SessionID) != "" {
		_ = os.Setenv("OS_BRAIN_SESSION_ID", strings.TrimSpace(p.SessionID))
	}
}

// hookEvent reports whether the payload is for the named host event.
func hookEvent(payload []byte, event string) bool {
	var p struct {
		Event string `json:"hook_event_name"`
	}
	return json.Unmarshal(payload, &p) == nil && p.Event == event
}

// copilotHook reports whether Copilot CLI is the host running a Claude-format hook. It runs
// .claude/settings.json with Claude's payloads but reads context only from a JSON
// additionalContext field, so plain output reached nothing. COPILOT_CLI alone could be
// inherited by a Claude Code started from a Copilot shell; Claude Code sends transcript_path
// on every event and Copilot's session-start and prompt payloads carry none, so both must
// agree.
func copilotHook(payload []byte) bool {
	return os.Getenv("COPILOT_CLI") == "1" && !fromClaudeCode(payload)
}

// devinHook is copilotHook for Devin CLI, which runs .claude/settings.json too, sets
// DEVIN_PROJECT_DIR and reads context only from Claude's nested hookSpecificOutput: plain
// stdout and the flat additionalContext Copilot takes both reached nothing. The payload
// check is the same one.
func devinHook(payload []byte) bool {
	return os.Getenv("DEVIN_PROJECT_DIR") != "" && !fromClaudeCode(payload)
}

// fromClaudeCode reports whether a parseable payload carries transcript_path, which Claude
// Code sends on every event. A payload that does not parse counts as Claude Code's, so the
// hook keeps its plain output.
func fromClaudeCode(payload []byte) bool {
	var p map[string]json.RawMessage
	if json.Unmarshal(payload, &p) != nil {
		return true
	}
	_, claude := p["transcript_path"]
	return claude
}

// hostEnvelope is where a plain-text hook writes when its host is Copilot CLI or Devin CLI,
// which run Claude's hooks but read only JSON, and the function that hands what was written
// to stdout in that host's envelope. For every other host it is stdout and a no-op.
func hostEnvelope(stdout io.Writer, payload []byte, event string) (io.Writer, func()) {
	switch {
	case copilotHook(payload):
		var buf bytes.Buffer
		return &buf, func() { copilotEnvelope(stdout, buf.String()) }
	case devinHook(payload):
		var buf bytes.Buffer
		return &buf, func() { devinEnvelope(stdout, event, buf.String()) }
	}
	return stdout, func() {}
}

func devinEnvelope(w io.Writer, event, body string) {
	if body = strings.TrimSpace(body); body != "" {
		_ = brief.EmitEventEnvelope(w, "codex", event, body)
	}
}

func copilotEnvelope(w io.Writer, body string) {
	if body = strings.TrimSpace(body); body == "" {
		return
	}
	data, err := json.Marshal(map[string]string{"additionalContext": body})
	if err == nil {
		fmt.Fprintln(w, string(data))
	}
}

// hookEmitOK refuses an --emit envelope the hook does not speak. hermes says whether this
// hook is one Hermes runs (only hook-recall is).
func hookEmitOK(emit string, hermes bool) error {
	if emit == "" || emit == "gemini" || emit == "codex" || (hermes && emit == "hermes") {
		return nil
	}
	if hermes {
		return fmt.Errorf("unknown envelope %q for this hook. Only gemini, codex and hermes need one "+
			"here: Cursor has no per-prompt hook and every other host reads plain stdout", emit)
	}
	return fmt.Errorf("unknown envelope %q for this hook. Only gemini and codex need one here: every "+
		"other host reads plain stdout or ignores what this hook prints", emit)
}

// hermesPayload rewrites Hermes's pre_llm_call payload into the shape hook-recall reads, and
// says whether this is a session's first turn. Hermes puts the prompt in extra.user_message
// and runs hooks from its own install directory, not the project's, which is why the hook
// names the brain through autoexec.sh.
func hermesPayload(raw []byte) ([]byte, bool) {
	var p struct {
		SessionID string `json:"session_id"`
		Extra     struct {
			UserMessage string `json:"user_message"`
			FirstTurn   bool   `json:"is_first_turn"`
		} `json:"extra"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return raw, false
	}
	out, _ := json.Marshal(map[string]string{"session_id": p.SessionID, "prompt": p.Extra.UserMessage})
	return out, p.Extra.FirstTurn
}
