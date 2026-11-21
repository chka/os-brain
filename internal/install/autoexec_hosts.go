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

// Each host's wiring, one section per agent. The version numbers in the comments are the
// releases each behaviour was observed on, because these agents change their hook contracts
// often and the reason for a choice here is only as good as the version it was checked on.

// --- Claude Code, Copilot CLI, Devin CLI --------------------------------------------

// wireClaude writes the hooks into .claude/settings.json (claude_hooks.go) and the slash
// commands beside them. The slash commands are Claude Code's, so they are written here,
// where .claude is already owned, rather than beside the database.
func wireClaude(root, dir, script string, autoRecall bool) ([]string, error) {
	did, err := wireHooks(root, dir, autoRecall)
	if err != nil {
		return did, err
	}
	cmds, err := writeSlashCommands(root)
	if err != nil {
		return did, err
	}
	return append(did, cmds...), nil
}

func wireCopilot(root, dir, script string, autoRecall bool) ([]string, error) {
	return wireHooksFor(root, dir, autoRecall, "Copilot CLI")
}

func wireDevin(root, dir, script string, autoRecall bool) ([]string, error) {
	return wireHooksFor(root, dir, autoRecall, "Devin CLI")
}

// wireHooksFor wires .claude/settings.json for another agent that runs that file, and says
// so on every line it reports.
func wireHooksFor(root, dir string, autoRecall bool, label string) ([]string, error) {
	did, err := wireHooks(root, dir, autoRecall)
	for i := range did {
		did[i] += " (" + label + " reads this file)"
	}
	return did, err
}

func manualClaude(root, dir, script string) (string, string) {
	home := "\"$CLAUDE_PROJECT_DIR/" + dir + "\""
	obj := map[string]interface{}{"hooks": map[string]interface{}{
		"SessionStart": []interface{}{map[string]interface{}{
			"matcher": "startup|resume|clear|compact",
			"hooks": []interface{}{map[string]interface{}{
				"type": "command", "command": "OS_BRAIN_HOME=" + home + " os-brain wakeup"}},
		}},
	}}
	return filepath.Join(".claude", "settings.json"), pretty(obj)
}

// --- Qwen Code ---------------------------------------------------------------------

// Qwen Code takes Claude's hook shape and payloads (transcript_path included) in its own
// .qwen/settings.json and injects plain stdout, on SessionStart and UserPromptSubmit alike
// (Qwen Code 0.25.2). It does not read .claude/settings.json.
func wireQwen(root, dir, script string, autoRecall bool) ([]string, error) {
	return wireNestedHooks(root, filepath.Join(root, ".qwen", "settings.json"), "Qwen Code",
		qwenHooks(script, autoRecall))
}

func qwenHooks(script string, autoRecall bool) [][2]string {
	pairs := [][2]string{{"SessionStart", wakeupCmd(script, Host{})}}
	if autoRecall {
		pairs = append(pairs, [2]string{"UserPromptSubmit", hookCmd(script, "hook-recall")})
	}
	return append(pairs, [2]string{"Stop", hookCmd(script, "hook-stop")},
		[2]string{"SessionEnd", hookCmd(script, "hook-end")})
}

func manualQwen(root, dir, script string) (string, string) {
	hooks := map[string]interface{}{}
	for _, pair := range qwenHooks(script, true) {
		hooks[pair[0]] = []interface{}{nestedHookEntry(pair[1])}
	}
	return filepath.Join(".qwen", "settings.json"), pretty(map[string]interface{}{"hooks": hooks})
}

// --- Hermes Agent ------------------------------------------------------------------

// Hermes keeps its hooks in ~/.hermes/config.yaml: one file for the user, outside every
// project, in YAML this binary has no parser for. Rewriting somebody's whole agent config to
// wire one project is not init's to do, so it prints the block instead (Hermes Agent
// 0.22.1). pre_llm_call is the only hook whose output reaches the model, so it carries the
// brief on a session's first turn as well as recall; the commands name the brain through
// the shim because Hermes runs them from its own install directory.
func wireHermes(root, dir, script string, autoRecall bool) ([]string, error) {
	return nil, fmt.Errorf("Hermes keeps its hooks in ~/.hermes/config.yaml, outside this project, " +
		"so init does not edit it. Add the block below under hooks:, then approve the hooks " +
		"when Hermes asks (or run it once with --accept-hooks)")
}

func manualHermes(root, dir, script string) (string, string) {
	return "~/.hermes/config.yaml", "hooks:\n" +
		"  pre_llm_call:\n" +
		"    - {command: \"" + hookCmd(script, "hook-recall --emit hermes") + "\", timeout: 20}\n" +
		"  on_session_end:\n" +
		"    - {command: \"" + hookCmd(script, "hook-stop") + "\", timeout: 15}\n"
}

// --- Continue CLI ------------------------------------------------------------------

// Continue's CLI loads hooks from .claude/ and .continue/settings.json and never fires them
// (cn 1.5.47: the hooks service is registered and nothing calls it), so the brain reaches it
// through MCP alone. The server goes in the YAML config cn is run with: `cn -p` ignores
// .continue/mcpServers/ in the workspace. It starts in the project, so one entry serves
// every project and needs no path.
func wireContinue(root, dir, script string, autoRecall bool) ([]string, error) {
	return nil, fmt.Errorf("Continue's CLI never runs hooks, so the brain reaches it through MCP " +
		"only, and init does not edit your Continue config. Add this to ~/.continue/config.yaml")
}

func manualContinue(root, dir, script string) (string, string) {
	return "~/.continue/config.yaml", "mcpServers:\n" +
		"  - name: brain\n" +
		"    type: stdio\n" +
		"    command: os-brain\n" +
		"    args: [mcp]\n"
}

// --- Codex CLI ---------------------------------------------------------------------

// Codex reads project hooks from .codex/hooks.json. It runs them only when the hooks feature
// is on and each hook has been reviewed (the TUI asks; `codex exec` needs --enable hooks and
// --dangerously-bypass-hook-trust), and it reads stdout opening with "[" as JSON, dropping
// the hook as failed when it does not parse, which every recall block did (Codex 0.163). So
// the brain answers in the JSON shape Codex parses, and a re-run replaces this brain's older
// entries instead of adding a second set beside them, which would brief twice.
func wireCodex(root, dir, script string, autoRecall bool) ([]string, error) {
	path := filepath.Join(root, ".codex", "hooks.json")
	cfg, err := readJSONFile(root, path)
	if err != nil {
		return nil, err
	}
	hooks := hooksBlock(cfg)
	want := map[string]string{
		"SessionStart": wakeupCmd(script, Host{Emit: "codex"}),
		"Stop":         hookCmd(script, "hook-stop --emit codex"),
	}
	if autoRecall {
		want["UserPromptSubmit"] = hookCmd(script, "hook-recall --emit codex")
	}
	var did []string
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop"} {
		cmd, ok := want[event]
		if !ok || nestedHookPresent(hooks, event, cmd) {
			continue
		}
		list, _ := hooks[event].([]interface{})
		kept := list[:0:0]
		for _, entry := range list {
			if !nestedEntryRuns(entry, script) {
				kept = append(kept, entry)
			}
		}
		if len(kept) != len(list) {
			did = append(did, "Codex "+event+": replaced this brain's older hook")
		}
		entry := nestedHookEntry(cmd)
		if event == "SessionStart" {
			entry["matcher"] = "startup|resume"
		}
		hooks[event] = append(kept, entry)
		did = append(did, "Codex "+event+" hook -> "+cmd)
	}
	if len(did) == 0 {
		return nil, nil
	}
	cfg["hooks"] = hooks
	return did, writeJSONFile(root, path, cfg)
}

// nestedEntryRuns reports whether a nested hook entry runs this brain's shim, whatever its
// arguments: those are the entries a rewiring may replace, and nobody else's.
func nestedEntryRuns(entry interface{}, script string) bool {
	e, _ := entry.(map[string]interface{})
	inner, _ := e["hooks"].([]interface{})
	for _, h := range inner {
		hm, _ := h.(map[string]interface{})
		if c, _ := hm["command"].(string); strings.HasPrefix(strings.TrimSpace(c), invoke(script)+" ") {
			return true
		}
	}
	return false
}

func manualCodex(root, dir, script string) (string, string) {
	entry := nestedHookEntry(wakeupCmd(script, Host{Emit: "codex"}))
	entry["matcher"] = "startup|resume"
	obj := map[string]interface{}{"hooks": map[string]interface{}{
		"SessionStart": []interface{}{entry},
	}}
	return filepath.Join(".codex", "hooks.json"), pretty(obj)
}

// --- Cursor ------------------------------------------------------------------------

// Cursor's sessionStart takes no plain stdout: context arrives only in the JSON field
// additional_context, which is what `wakeup --emit cursor` produces. beforeSubmitPrompt is
// deliberately not wired: it can block a prompt but cannot add to the conversation, so an
// auto-recall hook there would run on every message and inject nothing.
func wireCursor(root, dir, script string, autoRecall bool) ([]string, error) {
	path := filepath.Join(root, ".cursor", "hooks.json")
	cfg, err := readJSONFile(root, path)
	if err != nil {
		return nil, err
	}
	if _, ok := cfg["version"]; !ok {
		cfg["version"] = 1
	}
	hooks := hooksBlock(cfg)
	var did []string
	for _, pair := range [][2]string{
		{"sessionStart", wakeupCmd(script, Host{Emit: "cursor"})},
		{"stop", hookCmd(script, "hook-stop")},
		// stop never fires under `agent -p` and sessionEnd always does, so the footprint and
		// the session's end are recorded there too. hook-stop holds messages back at
		// sessionEnd, where nobody would read them.
		{"sessionEnd", hookCmd(script, "hook-stop")},
		{"sessionEnd", hookCmd(script, "hook-end")},
	} {
		event, cmd := pair[0], pair[1]
		if flatHookPresent(hooks, event, cmd) {
			continue
		}
		list, _ := hooks[event].([]interface{})
		hooks[event] = append(list, map[string]interface{}{"command": cmd})
		did = append(did, "Cursor "+event+" hook -> "+cmd)
	}
	if len(did) == 0 {
		return nil, nil
	}
	cfg["hooks"] = hooks
	return did, writeJSONFile(root, path, cfg)
}

func manualCursor(root, dir, script string) (string, string) {
	obj := map[string]interface{}{
		"version": 1,
		"hooks": map[string]interface{}{
			"sessionStart": []interface{}{map[string]interface{}{
				"command": wakeupCmd(script, Host{Emit: "cursor"})}},
		},
	}
	return filepath.Join(".cursor", "hooks.json"), pretty(obj)
}

// CursorDeliversBrief reports whether a plain-text session-start hook should stay silent
// because Cursor is the host and Cursor's own hook already carries the brief. Cursor runs
// .claude/settings.json beside .cursor/hooks.json, so a project wired for both was briefed
// twice per session. The payload decides it, not CURSOR_VERSION: that variable is inherited
// by a Claude Code started from Cursor's terminal, where the Claude hook is the only one that
// runs. Anything unparseable, or a Cursor project without our sessionStart hook, keeps the
// brief: silence is the costly mistake here, a duplicate is only noise.
func CursorDeliversBrief(payload []byte) bool {
	var p struct {
		CursorVersion  string   `json:"cursor_version"`
		Event          string   `json:"hook_event_name"`
		WorkspaceRoots []string `json:"workspace_roots"`
	}
	if json.Unmarshal(payload, &p) != nil || p.CursorVersion == "" || p.Event != "sessionStart" {
		return false
	}
	for _, root := range p.WorkspaceRoots {
		cfg, err := readJSONFile(root, filepath.Join(root, ".cursor", "hooks.json"))
		if err != nil {
			continue
		}
		hooks, _ := cfg["hooks"].(map[string]interface{})
		list, _ := hooks["sessionStart"].([]interface{})
		for _, entry := range list {
			e, _ := entry.(map[string]interface{})
			cmd, _ := e["command"].(string)
			if strings.Contains(cmd, " wakeup") && strings.Contains(cmd, "--emit cursor") {
				return true
			}
		}
	}
	return false
}

// --- Gemini CLI --------------------------------------------------------------------

// Gemini injects only hookSpecificOutput.additionalContext, which `--emit gemini` produces.
// BeforeAgent runs on every prompt and AfterAgent at the end of every turn, the shapes of
// Claude's UserPromptSubmit and Stop (Gemini CLI 0.64). Both read stdout as JSON, hence
// --emit. SessionEnd is not awaited, so it gets only the cheap bookkeeping of hook-end.
func wireGemini(root, dir, script string, autoRecall bool) ([]string, error) {
	pairs := [][2]string{{"SessionStart", wakeupCmd(script, Host{Emit: "gemini"})}}
	if autoRecall {
		pairs = append(pairs, [2]string{"BeforeAgent", hookCmd(script, "hook-recall --emit gemini")})
	}
	pairs = append(pairs,
		[2]string{"AfterAgent", hookCmd(script, "hook-stop --emit gemini")},
		[2]string{"SessionEnd", hookCmd(script, "hook-end")})
	return wireNestedHooks(root, filepath.Join(root, ".gemini", "settings.json"), "Gemini", pairs)
}

func manualGemini(root, dir, script string) (string, string) {
	obj := map[string]interface{}{"hooks": map[string]interface{}{
		"SessionStart": []interface{}{nestedHookEntry(wakeupCmd(script, Host{Emit: "gemini"}))},
	}}
	return filepath.Join(".gemini", "settings.json"), pretty(obj)
}

// --- shared file handling ----------------------------------------------------------

// wireNestedHooks appends each (event, command) pair that is not already there to the
// nested hooks block of the JSON file at path, and reports one line per hook added, each
// starting with label. It writes nothing when every hook is already in place.
func wireNestedHooks(root, path, label string, pairs [][2]string) ([]string, error) {
	cfg, err := readJSONFile(root, path)
	if err != nil {
		return nil, err
	}
	hooks := hooksBlock(cfg)
	var did []string
	for _, pair := range pairs {
		event, cmd := pair[0], pair[1]
		if nestedHookPresent(hooks, event, cmd) {
			continue
		}
		addNestedHook(hooks, event, cmd)
		did = append(did, label+" "+event+" hook -> "+cmd)
	}
	if len(did) == 0 {
		return nil, nil
	}
	cfg["hooks"] = hooks
	return did, writeJSONFile(root, path, cfg)
}

// hooksBlock is cfg's "hooks" object, or a new empty one when it is missing or not an
// object. The caller stores it back into cfg before writing.
func hooksBlock(cfg map[string]interface{}) map[string]interface{} {
	hooks, _ := cfg["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = map[string]interface{}{}
	}
	return hooks
}

// nestedHookEntry is one entry of the Claude/Codex/Gemini/Qwen shape: {hooks:[{type,command}]}.
func nestedHookEntry(cmd string) map[string]interface{} {
	return map[string]interface{}{
		"hooks": []interface{}{map[string]interface{}{"type": "command", "command": cmd}},
	}
}

func addNestedHook(hooks map[string]interface{}, event, cmd string) {
	list, _ := hooks[event].([]interface{})
	hooks[event] = append(list, nestedHookEntry(cmd))
}

// readJSONFile refuses an unparseable file rather than replacing it. Every one of these
// belongs to the host agent, not to this tool, and overwriting a config somebody hand-wrote
// is worse than not wiring the hook: the second is reported and fixable.
func readJSONFile(root, path string) (map[string]interface{}, error) {
	if err := fsguard.CheckNoLinks(root, path); err != nil {
		return nil, err
	}
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]interface{}{}, nil
	}
	if err != nil {
		return nil, err
	}
	cfg := map[string]interface{}{}
	if json.Unmarshal(body, &cfg) != nil {
		return nil, fmt.Errorf("%s exists but is not readable JSON, so it was left untouched", path)
	}
	return cfg, nil
}

func writeJSONFile(root, path string, cfg map[string]interface{}) error {
	data, err := pyfmt.MarshalPy(cfg, "  ")
	if err != nil {
		return err
	}
	return fsguard.WriteFileSafe(path, append(data, '\n'), fsguard.WriteOpts{Root: root, Mode: 0o644,
		KeepMode: true, MkdirMode: 0o755})
}

// nestedHookPresent covers the Claude/Codex/Gemini shape: event -> [{hooks:[{command}]}].
func nestedHookPresent(hooks map[string]interface{}, event, cmd string) bool {
	list, _ := hooks[event].([]interface{})
	for _, entry := range list {
		e, _ := entry.(map[string]interface{})
		inner, _ := e["hooks"].([]interface{})
		for _, h := range inner {
			hm, _ := h.(map[string]interface{})
			if s, _ := hm["command"].(string); strings.TrimSpace(s) == cmd {
				return true
			}
		}
	}
	return false
}

// flatHookPresent covers Cursor's shape: event -> [{command}].
func flatHookPresent(hooks map[string]interface{}, event, cmd string) bool {
	list, _ := hooks[event].([]interface{})
	for _, entry := range list {
		e, _ := entry.(map[string]interface{})
		if s, _ := e["command"].(string); strings.TrimSpace(s) == cmd {
			return true
		}
	}
	return false
}

// pretty renders a config block for a human to paste, in the same format init writes.
func pretty(v interface{}) string {
	data, err := pyfmt.MarshalPy(v, "  ")
	if err != nil {
		return ""
	}
	return string(data)
}
