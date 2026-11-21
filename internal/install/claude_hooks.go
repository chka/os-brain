package install

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chka/os-brain/internal/fsguard"
	"github.com/chka/os-brain/internal/pyfmt"
)

// CheckBrainDir is the rule for --dir. The value is written into every hook command init
// wires, inside double quotes after $CLAUDE_PROJECT_DIR, and the shell expands $(...),
// backticks and a closing quote there on every session open. So it is a plain relative path
// of letters, digits, '.', '_', '-' and '/', with no empty or '..' segment and nothing that
// reads as a flag; anything else is refused before a byte is written.
func CheckBrainDir(dir string) error {
	bad := dir == "" || filepath.IsAbs(dir) || strings.HasPrefix(dir, "-")
	for _, r := range dir {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '.' || r == '_' || r == '-' || r == '/') {
			bad = true
		}
	}
	for _, seg := range strings.Split(dir, "/") {
		if seg == "" || seg == "." || seg == ".." {
			bad = true
		}
	}
	if bad {
		return fmt.Errorf("--dir %s is refused: it is written into every hook command, so it "+
			"must be a relative path inside the project made of letters, digits, '.', '_', "+
			"'-' and '/', with no '..' segment (for example marketing/brain). Nothing was "+
			"written", pyfmt.Repr(dir))
	}
	return nil
}

// claudeHookCmds are the commands init wires into .claude/settings.json for one brain.
type claudeHookCmds struct {
	// wake runs at SessionStart. It is `wakeup`, not `brief`: wakeup is the brief plus the
	// working tree plus whatever this project declared worth knowing at session open.
	wake string
	// brief is what installs made before wakeup existed ran at SessionStart, and is
	// rewritten to wake.
	brief  string
	recall string
	// stop records what the session did when it ends a turn. Reads were a mechanism and
	// writes were a wish, so a session that learned five things and stored none looked
	// exactly like one that learned nothing; this cannot force a write, but it lets the next
	// brief say so.
	stop string
	// tool and end deliver direct messages to a running session on its next tool call and
	// when it ends, not only on the user's next prompt: a session twenty tool calls into a
	// task is exactly the one a message is sent to redirect.
	tool string
	end  string
}

// claudeHooksFor builds the commands for the brain at dir. The binary is on PATH, not in the
// project, so each command names the brain through OS_BRAIN_HOME; without it the hook would
// read (or create) a brain next to the executable.
func claudeHooksFor(dir string) claudeHookCmds {
	prefix := "OS_BRAIN_HOME=" + fmt.Sprintf("\"$CLAUDE_PROJECT_DIR/%s\"", dir) + " os-brain "
	return claudeHookCmds{
		wake:   prefix + "wakeup",
		brief:  prefix + "brief",
		recall: prefix + "hook-recall",
		stop:   prefix + "hook-stop",
		tool:   prefix + "hook-tool",
		end:    prefix + "hook-end",
	}
}

// oldSessionStartMatcher is the matcher earlier versions wrote on the SessionStart entry. It
// named every source Claude Code has, so it filtered nothing there, and Devin, which also
// runs this file, reports a source outside it and skipped the brief.
const oldSessionStartMatcher = "startup|resume|clear|compact"

// wireHooks adds SessionStart, UserPromptSubmit, Stop, PostToolUse and SessionEnd to
// .claude/settings.json without disturbing anything already there, and reports one line per
// change. A settings file that exists but does not parse is left completely alone: silently
// rewriting somebody's config is worse than not wiring.
func wireHooks(root, dir string, autoRecall bool) ([]string, error) {
	if err := CheckBrainDir(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(root, ".claude", "settings.json")
	if err := fsguard.CheckNoLinks(root, path); err != nil {
		return nil, err
	}
	cfg := map[string]interface{}{}
	if body, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(body, &cfg) != nil {
			// Reported as unwired rather than warned about and passed over: a hook that was
			// not written is the one fact the caller has to act on, and a warning in the
			// middle of a successful-looking install is where it goes to die.
			return nil, fmt.Errorf("%s exists but is not readable JSON, so it was left untouched", path)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	c := claudeHooksFor(dir)
	hooks := hooksBlock(cfg)
	did := wireSessionStart(hooks, dir, c)
	did = append(did, dropOldMatcher(hooks, c.wake)...)
	for _, h := range []struct{ event, cmd string }{
		{"Stop", c.stop}, {"PostToolUse", c.tool}, {"SessionEnd", c.end},
	} {
		if !nestedHookPresent(hooks, h.event, h.cmd) {
			addNestedHook(hooks, h.event, h.cmd)
			did = append(did, h.event+" hook -> "+h.cmd)
		}
	}
	if autoRecall {
		if migrateLegacyHook(hooks, dir, "UserPromptSubmit", "hook-recall", c.recall) {
			did = append(did, "UserPromptSubmit hook: legacy wrapper replaced by -> "+c.recall)
		} else if !nestedHookPresent(hooks, "UserPromptSubmit", c.recall) {
			addNestedHook(hooks, "UserPromptSubmit", c.recall)
			did = append(did, "UserPromptSubmit hook -> "+c.recall)
		}
	}
	if len(did) == 0 {
		return nil, nil
	}
	cfg["hooks"] = hooks
	data, err := pyfmt.MarshalPy(cfg, "  ")
	if err != nil {
		return nil, err
	}
	if err := fsguard.WriteFileSafe(path, append(data, '\n'), fsguard.WriteOpts{Root: root, Mode: 0o644,
		KeepMode: true, MkdirMode: 0o755}); err != nil {
		return nil, err
	}
	return did, nil
}

// wireSessionStart puts wakeup on SessionStart: rewriting a legacy wrapper's brief, then an
// older install's `os-brain brief`, and adding it only when neither is there. Appending
// beside an older hook would give a session two briefs.
func wireSessionStart(hooks map[string]interface{}, dir string, c claudeHookCmds) []string {
	switch {
	case migrateLegacyHook(hooks, dir, "SessionStart", "brief", c.wake):
		return []string{"SessionStart hook: legacy wrapper replaced by -> " + c.wake}
	case replaceHookCommand(hooks, "SessionStart", c.brief, c.wake):
		return []string{"SessionStart hook: brief -> " + c.wake +
			" (the brief, plus the working tree and this project's probes)"}
	case !nestedHookPresent(hooks, "SessionStart", c.wake):
		addNestedHook(hooks, "SessionStart", c.wake)
		return []string{"SessionStart hook -> " + c.wake}
	}
	return nil
}

// dropOldMatcher removes oldSessionStartMatcher from the SessionStart entries that run this
// brain's wakeup, so every agent that reads the file runs it. Entries running anything else
// keep their matcher.
func dropOldMatcher(hooks map[string]interface{}, wake string) []string {
	var did []string
	list, _ := hooks["SessionStart"].([]interface{})
	for _, entry := range list {
		e, _ := entry.(map[string]interface{})
		if m, _ := e["matcher"].(string); m != oldSessionStartMatcher {
			continue
		}
		inner, _ := e["hooks"].([]interface{})
		for _, h := range inner {
			hm, _ := h.(map[string]interface{})
			if c, _ := hm["command"].(string); strings.TrimSpace(c) == wake {
				delete(e, "matcher")
				did = append(did, "SessionStart hook: matcher removed, so every agent that "+
					"reads this file runs it")
				break
			}
		}
	}
	return did
}

// migrateLegacyHook rewrites, in place, a hook that runs the retired wrapper script at
// <dir>/brain with subcommand sub, so it runs want instead. Appending would be wrong: the
// project would end up with the wrapper AND the binary on the same event, so a session gets
// two briefs, every prompt runs recall twice, and both clients bump use_count on the same
// memories. The old client also refuses a database whose schema is newer than it
// understands, so every session would open with a migration error in its context.
//
// Narrow on purpose: it only claims a command that names THIS brain directory's wrapper and
// runs the subcommand being wired, so an unrelated hook is never touched. A command that
// already runs os-brain is not a legacy hook.
func migrateLegacyHook(hooks map[string]interface{}, dir, event, sub, want string) bool {
	legacy := dir + "/brain"
	done := false
	list, _ := hooks[event].([]interface{})
	for _, entry := range list {
		e, _ := entry.(map[string]interface{})
		inner, _ := e["hooks"].([]interface{})
		for _, h := range inner {
			hm, _ := h.(map[string]interface{})
			s, _ := hm["command"].(string)
			s = strings.TrimSpace(s)
			if strings.Contains(s, "os-brain") || !strings.Contains(s, legacy) {
				continue
			}
			if !mentionsSubcommand(s, legacy, sub) {
				continue
			}
			hm["command"] = want
			done = true
		}
	}
	if done {
		dedupe(hooks, event, want)
	}
	return done
}

// replaceHookCommand rewrites every hook on event whose command is exactly from to run to,
// and reports whether any was.
func replaceHookCommand(hooks map[string]interface{}, event, from, to string) bool {
	done := false
	list, _ := hooks[event].([]interface{})
	for _, entry := range list {
		e, _ := entry.(map[string]interface{})
		inner, _ := e["hooks"].([]interface{})
		for _, h := range inner {
			hm, _ := h.(map[string]interface{})
			if c, _ := hm["command"].(string); strings.TrimSpace(c) == from {
				hm["command"], done = to, true
			}
		}
	}
	if done {
		dedupe(hooks, event, to)
	}
	return done
}

// mentionsSubcommand reports whether a hook command invokes the wrapper at legacy with the
// given subcommand, whatever surrounds it. Older installers wrote forms like
// `cd "$CLAUDE_PROJECT_DIR" && ./brain/brain brief 2>/dev/null || true`, so a suffix test
// misses them. It is anchored on the wrapper path so a command that merely contains the word
// "brief" is never claimed.
func mentionsSubcommand(cmd, legacy, sub string) bool {
	for i := 0; i < len(cmd); {
		j := strings.Index(cmd[i:], legacy)
		if j < 0 {
			return false
		}
		rest := cmd[i+j+len(legacy):]
		// Step over a closing quote, then require the subcommand as the next word.
		rest = strings.TrimLeft(rest, "\"'")
		if f := strings.Fields(rest); len(f) > 0 && f[0] == sub {
			return true
		}
		i += j + len(legacy)
	}
	return false
}

// dedupe keeps the first hook running want and drops the rest, along with any entry left
// holding nothing. A project with two legacy hooks on one event (an old
// `cd && ./brain/brain brief || true` beside a newer wrapper call) would otherwise have both
// rewritten to the same command and run the new client twice, which is the duplicate the
// migration exists to remove.
func dedupe(hooks map[string]interface{}, event, want string) {
	list, _ := hooks[event].([]interface{})
	seen := false
	kept := []interface{}{}
	for _, entry := range list {
		e, _ := entry.(map[string]interface{})
		inner, _ := e["hooks"].([]interface{})
		keptInner := []interface{}{}
		for _, h := range inner {
			hm, _ := h.(map[string]interface{})
			if c, _ := hm["command"].(string); strings.TrimSpace(c) == want {
				if seen {
					continue
				}
				seen = true
			}
			keptInner = append(keptInner, h)
		}
		if len(keptInner) == 0 {
			continue
		}
		e["hooks"] = keptInner
		kept = append(kept, e)
	}
	hooks[event] = kept
}
