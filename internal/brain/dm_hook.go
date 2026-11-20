package brain

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Hook delivery. Each Claude Code event takes its context in a different envelope, and the
// wrong one is silent: Stop ignores plain stdout, PostToolUse ignores anything but its own
// JSON. So the envelope is chosen here, per event, and nowhere else.
const (
	HookPrompt = "UserPromptSubmit"
	HookTool   = "PostToolUse"
	HookStop   = "Stop"
)

// HookEnvelope wraps delivered text for the event that delivers it. UserPromptSubmit takes
// plain stdout as context, which is how hook-recall already speaks, so it is left plain.
func HookEnvelope(event, text string) string {
	switch event {
	case HookStop:
		// Blocking the stop is the only way a Stop hook puts text in front of the model; the
		// session then carries on with the message and stops again when it has dealt with it.
		b, _ := json.Marshal(map[string]interface{}{"decision": "block", "reason": text})
		return string(b)
	case HookTool:
		b, _ := json.Marshal(map[string]interface{}{"hookSpecificOutput": map[string]interface{}{
			"hookEventName": HookTool, "additionalContext": text}})
		return string(b)
	default:
		return text
	}
}

// ThisBeat is the beat a hook or listener in this process sends.
func ThisBeat(listening bool) Beat {
	pid := claudePID()
	if listening {
		// The listener is spawned by Claude Code directly, so its parent IS the process; hooks
		// run through a shell and read CLAUDE_PID instead.
		pid = int64(os.Getppid())
	}
	return Beat{Listening: listening, PID: pid, Machine: MachineName()}
}

// DeliverHook registers this session as alive and hands it anything waiting. It returns
// whether it wrote anything, so the Stop hook can tell whether it spoke. A hook never fails
// the turn: every error here is swallowed, because a message delayed to the next event is
// better than a session that cannot stop.
func DeliverHook(w io.Writer, m Messenger, event string) bool {
	if !ClaudeSession(SessionID()) {
		return false
	}
	ms, err := m.ClaimDMs(strings.ToLower(event), ThisBeat(false))
	if err != nil || len(ms) == 0 {
		return false
	}
	fmt.Fprintln(w, HookEnvelope(event, RenderDMs(ms, Now())))
	return true
}

// ToolPollDue throttles the PostToolUse hook per session with a stamp file beside the spine,
// so a scratch install pinned elsewhere never shares a stamp with the real one. A missing or
// unwritable stamp answers yes: failing open here costs one extra poll, failing closed would
// cost the message.
func ToolPollDue(dir string) bool {
	id := SessionID()
	if !ClaudeSession(id) {
		return false
	}
	path := toolStamp(dir, id)
	if st, err := os.Stat(path); err == nil && time.Since(st.ModTime()) < ToolPollEvery {
		return false
	}
	_ = WriteFileSafe(path, nil, WriteOpts{MkdirMode: 0o700})
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	return true
}

// ClearToolPoll removes this session's stamp when it ends, so they do not pile up.
func ClearToolPoll(dir string) {
	if id := SessionID(); ClaudeSession(id) {
		_ = os.Remove(toolStamp(dir, id))
	}
}

func toolStamp(dir, id string) string {
	return filepath.Join(dir, "dm-poll", sanitizeStamp(id))
}

// sanitizeStamp keeps a session id from naming a path outside the stamp directory.
func sanitizeStamp(id string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') {
			return r
		}
		return '_'
	}, id)
}
