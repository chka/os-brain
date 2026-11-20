package brain

// Tests for wiring the brain into each agent host (autoexec.go and autoexec_hosts.go), and
// for what the wired hooks then hand the host.
//
// The failure all of these guard against is a silent one: a hook written into the wrong
// file, or in a shape the host does not read, is not an error anywhere. The host ignores it,
// the agent never sees the brain, and nobody is told. So every test here pins either the
// exact file and field names a host reads, or the honest report init gives when it cannot
// wire something.

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Choosing which hosts to wire -------------------------------------------------------

// The whole promise of init is that a mechanism reads the brain at session start. With no
// host to wire there is no mechanism, so init must fail and say what to add, rather than
// print a success that nothing backs.
func TestInitRefusesToPretendWhenNoHostCanBeWired(t *testing.T) {
	root := initEnv(t)
	var out strings.Builder
	err := Init(&out, root, InitArgs{})
	if err == nil {
		t.Fatal("init claimed success with nothing wired to read the brain")
	}
	text := out.String()
	if !strings.Contains(text, "NOT WIRED") {
		t.Errorf("the report does not say the brain is unwired:\n%s", text)
	}
	for _, want := range []string{".claude", ".codex", ".cursor", ".gemini"} {
		if !strings.Contains(text, want) {
			t.Errorf("the report does not tell the developer what to add for %s:\n%s", want, text)
		}
	}
	// It refuses to claim the wiring, not to install: the brain itself is still created.
	if _, err := os.Stat(filepath.Join(root, "brain", "brain.db")); err != nil {
		t.Error("the brain was not created")
	}
}

// Without --agent, init wires the hosts the project shows evidence of, and only those.
// Wiring a host nobody uses litters the project with config directories.
func TestInitDetectsAHostFromTheProject(t *testing.T) {
	root := initEnv(t)
	if err := os.MkdirAll(filepath.Join(root, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Init(io.Discard, root, InitArgs{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".cursor", "hooks.json")); err != nil {
		t.Error("a project using Cursor was not wired for Cursor")
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Error("a host with no evidence for it was wired anyway")
	}
}

// A typo in --agent must not quietly wire nothing and report success.
func TestAnUnknownAgentIsRefusedRatherThanIgnored(t *testing.T) {
	root := initEnv(t)
	err := Init(io.Discard, root, InitArgs{Agents: "windsurf"})
	if err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("a name wired to nothing was accepted: %v", err)
	}
}

// `init --manual` is for people who want to wire their hosts themselves: it prints one
// block per host and writes nothing, not even for the host it detected.
func TestAutoexecManualWiresNothing(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{".claude", "brain"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	res, err := AutoexecManual(root, "brain")
	if err != nil {
		t.Fatal(err)
	}
	if res.Wired != 0 || len(res.Did) != 0 {
		t.Fatalf("manual wired something: %+v", res)
	}
	if len(res.Unwired) != len(Hosts()) {
		t.Fatalf("manual printed %d blocks, want one per host (%d)", len(res.Unwired), len(Hosts()))
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "settings.json")); err == nil {
		t.Fatal("manual wrote the detected host's settings anyway")
	}
}

// --- The shape each host reads ----------------------------------------------------------

// Every field name below is the host's own. A hook in the wrong shape is silently ignored,
// so each host's file is read back and checked for the events and the --emit envelope that
// host parses.
func TestEachHostGetsTheShapeItActuallyReads(t *testing.T) {
	root := initEnv(t)
	if err := Init(io.Discard, root, InitArgs{Agents: "cursor,codex,gemini"}); err != nil {
		t.Fatal(err)
	}

	// Cursor: a flat list of {command} under hooks.sessionStart, answered as Cursor JSON.
	cursor := readJSON(t, filepath.Join(root, ".cursor", "hooks.json"))
	hooks := cursor["hooks"].(map[string]any)
	entries, ok := hooks["sessionStart"].([]any)
	if !ok || len(entries) == 0 {
		t.Fatalf("cursor sessionStart missing: %v", hooks)
	}
	cmd := entries[0].(map[string]any)["command"].(string)
	if !strings.Contains(cmd, "wakeup") || !strings.Contains(cmd, "--emit cursor") {
		t.Errorf("cursor runs %q, which injects nothing", cmd)
	}

	// Codex treats stdout that opens with "[" as JSON and drops the hook as failed when it
	// does not parse. Every recall block opens with "[", so Codex must get JSON it parses.
	codex := readJSON(t, filepath.Join(root, ".codex", "hooks.json"))
	if !strings.Contains(nested(t, codex, "SessionStart"), "wakeup") {
		t.Error("codex SessionStart does not run wakeup")
	}
	if !strings.Contains(nested(t, codex, "SessionStart"), "--emit codex") {
		t.Error("codex SessionStart is not answered in the shape codex parses")
	}

	// Gemini reads JSON only, at session start, before each prompt and after each turn.
	gemini := readJSON(t, filepath.Join(root, ".gemini", "settings.json"))
	if !strings.Contains(nested(t, gemini, "SessionStart"), "--emit gemini") {
		t.Error("gemini SessionStart does not produce the field gemini reads")
	}
	if c := nested(t, gemini, "BeforeAgent"); !strings.Contains(c, "hook-recall --emit gemini") {
		t.Errorf("gemini BeforeAgent runs %q", c)
	}
	if c := nested(t, gemini, "AfterAgent"); !strings.Contains(c, "hook-stop --emit gemini") {
		t.Errorf("gemini AfterAgent runs %q", c)
	}
	if c := nested(t, gemini, "SessionEnd"); !strings.Contains(c, "hook-end") {
		t.Errorf("gemini SessionEnd runs %q", c)
	}
}

// Only Claude Code exports the project directory into its hooks. Other hosts run a command
// with no promise about the working directory or shell, so their hooks call a small shim
// that finds the brain from its own location.
func TestTheHookShimResolvesTheBrainFromItsOwnLocation(t *testing.T) {
	root := initEnv(t)
	if err := Init(io.Discard, root, InitArgs{Agents: "codex", Dir: "mem"}); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "mem", "autoexec.sh")
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "OS_BRAIN_HOME") || !strings.Contains(string(body), "dirname") {
		t.Errorf("the shim does not anchor the brain to itself:\n%s", body)
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm()&0o111 == 0 {
		t.Error("the shim is not executable")
	}
	codex := readJSON(t, filepath.Join(root, ".codex", "hooks.json"))
	if !strings.Contains(nested(t, codex, "SessionStart"), "mem/autoexec.sh") {
		t.Error("the hook does not run the shim of the brain it was installed beside")
	}
}

// Running init twice must not wire every hook twice: the agent would get the brief twice
// per session, and each re-run would add another copy.
func TestWiringIsIdempotentAcrossHosts(t *testing.T) {
	root := initEnv(t)
	for i := 0; i < 2; i++ {
		if err := Init(io.Discard, root, InitArgs{Agents: "cursor,codex,gemini"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{".cursor/hooks.json", ".codex/hooks.json", ".gemini/settings.json"} {
		body, _ := os.ReadFile(filepath.Join(root, p))
		if n := strings.Count(string(body), "wakeup"); n != 1 {
			t.Errorf("%s wired wakeup %d times", p, n)
		}
	}
}

// OpenCode loads JavaScript plugins rather than hook commands. The plugin has to carry the
// session id (so recall and the stop hook know which session they serve), and a brain.js the
// user wrote is theirs: init reports it as unwired instead of overwriting it.
func TestOpenCodeGetsAPluginThatCarriesItsSessionAndIsNeverForcedOverAnother(t *testing.T) {
	root := initEnv(t)
	if err := Init(io.Discard, root, InitArgs{Agents: "opencode"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".opencode", "plugins", "brain.js")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("init --agent opencode wrote no plugin")
	}
	for _, want := range []string{pluginMarker, filepath.Join(root, "brain", "autoexec.sh"),
		"OS_BRAIN_SESSION_ID", `"hook-recall"`, `"wakeup"`, `"hook-stop"`, "session.idle"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("plugin lacks %q", want)
		}
	}

	res, err := Autoexec(root, "brain", "opencode", true)
	if err != nil || len(res.Did) != 0 {
		t.Fatalf("second wiring was not a no-op: %v %v", res.Did, err)
	}

	if err := os.WriteFile(path, []byte("export const Mine = async () => ({})\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = Autoexec(root, "brain", "opencode", true)
	if err != nil || len(res.Unwired) == 0 {
		t.Fatalf("a foreign brain.js was not reported as unwired: %v %v", res.Unwired, err)
	}
	if got, _ := os.ReadFile(path); !strings.HasPrefix(string(got), "export const Mine") {
		t.Fatal("a plugin init did not write was overwritten")
	}
}

// Kilo takes the same kind of plugin as OpenCode in its own directory, and it keeps its user
// config in ~/.config/kilo rather than ~/.kilo. That directory alone must be enough to detect it, and
// a Kilo user must not also get an OpenCode plugin.
func TestKiloGetsItsOwnPluginAndIsDetectedUnderDotConfig(t *testing.T) {
	root := initEnv(t)
	if err := os.MkdirAll(filepath.Join(os.Getenv("HOME"), ".config", "kilo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Init(io.Discard, root, InitArgs{}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, ".kilo", "plugin", "brain.js"))
	if err != nil {
		t.Fatal("a Kilo user got no plugin")
	}
	if !strings.Contains(string(body), "for Kilo.") {
		t.Errorf("the plugin does not say which agent it is for:\n%s", string(body)[:120])
	}
	if _, err := os.Stat(filepath.Join(root, ".opencode")); err == nil {
		t.Error("an OpenCode plugin was written for a Kilo-only user")
	}
}

// Qwen Code reads Claude-style hooks but from its own .qwen/settings.json, and it reads
// plain stdout, so its commands carry no --emit. Uninstall must find these hooks too, or it
// leaves Qwen running a brain that has been deleted.
func TestQwenIsWiredInItsOwnSettingsAndUninstallFindsIt(t *testing.T) {
	root := initEnv(t)
	if err := Init(io.Discard, root, InitArgs{Agents: "qwen"}); err != nil {
		t.Fatal(err)
	}
	qwen := readJSON(t, filepath.Join(root, ".qwen", "settings.json"))
	for ev, want := range map[string]string{"SessionStart": "wakeup", "UserPromptSubmit": "hook-recall",
		"Stop": "hook-stop", "SessionEnd": "hook-end"} {
		if c := nested(t, qwen, ev); !strings.Contains(c, want) || strings.Contains(c, "--emit") {
			t.Errorf("qwen %s runs %q; it reads plain stdout", ev, c)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "settings.json")); err == nil {
		t.Error("qwen was wired through Claude's file, which Qwen does not read")
	}
	found := false
	for _, f := range hookFiles() {
		if _, entries, _ := brainHookEntries(root, "brain", f); len(entries) > 0 && strings.Contains(f.rel, ".qwen") {
			found = true
		}
	}
	if !found {
		t.Error("uninstall would leave Qwen's hooks running a deleted brain")
	}
}

// --- Hosts whose config is not ours to edit ---------------------------------------------

// Hermes and Continue keep their hooks in the user's own YAML, outside the project. init
// prints exactly what to add and must never claim it wired them, nor count them as failed.
func TestAgentsWhoseConfigIsNotOursArePrintedNeverEdited(t *testing.T) {
	for agent, want := range map[string]string{
		"hermes":   "hook-recall --emit hermes",
		"continue": "command: os-brain",
	} {
		root := initEnv(t)
		if err := os.MkdirAll(filepath.Join(root, "brain"), 0o755); err != nil {
			t.Fatal(err)
		}
		res, err := Autoexec(root, "brain", agent, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Did) != 0 || res.Wired != 0 || len(res.Unwired) != 0 {
			t.Errorf("%s reported as wired or failed: %+v", agent, res)
		}
		if len(res.Paste) != 1 || !strings.Contains(res.Paste[0], want) {
			t.Errorf("%s: no block to paste: %v", agent, res.Paste)
		}
	}
}

// A Claude Code user who also had Continue installed was told the install had failed,
// because Continue's paste-in block was counted as a host init could not wire. A paste-only
// host must never fail an install another host reads; it fails only when it is the sole
// host, because then nothing reads the brain until the block is added.
func TestAPasteOnlyAgentNeverFailsAnInstallSomethingElseReads(t *testing.T) {
	root := initEnv(t)
	if err := os.MkdirAll(filepath.Join(os.Getenv("HOME"), ".continue"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := Init(&out, root, InitArgs{}); err != nil {
		t.Fatalf("init failed for a project Claude Code reads: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "ADD BY HAND") || !strings.Contains(out.String(), "Continue CLI") {
		t.Errorf("the Continue block was not printed:\n%s", out.String())
	}
	// On a re-run Claude Code needs nothing written, and that must still not read as failure.
	if err := Init(io.Discard, root, InitArgs{}); err != nil {
		t.Fatalf("a re-run failed: %v", err)
	}

	alone := initEnv(t)
	if err := Init(io.Discard, alone, InitArgs{Agents: "continue"}); err == nil {
		t.Fatal("an install nothing reads was reported as finished")
	}
}

// --- Upgrading hooks an earlier version wrote -------------------------------------------

// Earlier versions wired Codex's prompt hook without --emit codex, which Codex then dropped
// as failed. Re-wiring must replace that old entry exactly once and leave the project's own
// hooks in the same event alone.
func TestRewiringCodexReplacesThisBrainsOlderHooksAndNobodyElses(t *testing.T) {
	root := initEnv(t)
	script := filepath.Join(root, "brain", "autoexec.sh")
	for _, d := range []string{".codex", "brain"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := `{"hooks":{"UserPromptSubmit":[` +
		`{"hooks":[{"type":"command","command":"` + script + ` hook-recall"}]},` +
		`{"hooks":[{"type":"command","command":"./scripts/lint-prompt.sh"}]}]}}`
	if err := os.WriteFile(filepath.Join(root, ".codex", "hooks.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Autoexec(root, "brain", "codex", true); err != nil {
		t.Fatal(err)
	}
	cfg := readJSON(t, filepath.Join(root, ".codex", "hooks.json"))
	list := cfg["hooks"].(map[string]any)["UserPromptSubmit"].([]any)
	var cmds []string
	for _, e := range list {
		for _, h := range e.(map[string]any)["hooks"].([]any) {
			cmds = append(cmds, h.(map[string]any)["command"].(string))
		}
	}
	all := strings.Join(cmds, "|")
	if len(cmds) != 2 || !strings.Contains(all, "lint-prompt.sh") ||
		!strings.Contains(all, "hook-recall --emit codex") {
		t.Fatalf("expected the project's hook kept and ours replaced once, got %v", cmds)
	}
}

// Earlier versions put a matcher listing every Claude Code session source on the brain's
// SessionStart entry. It filtered nothing in Claude Code, but Devin CLI reads the same file
// and matches none of those names, so Devin never ran the brief. Re-wiring drops that matcher
// from the brain's own entry and leaves other people's matchers alone.
func TestAnOldSessionStartMatcherIsRemovedSoDevinRunsTheBrief(t *testing.T) {
	root := initEnv(t)
	if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	wake := `OS_BRAIN_HOME="$CLAUDE_PROJECT_DIR/brain" os-brain wakeup`
	old := `{"hooks":{"SessionStart":[{"matcher":"startup|resume|clear|compact","hooks":[{"type":"command","command":` +
		strconvQuote(wake) + `}]},{"matcher":"startup","hooks":[{"type":"command","command":"./mine.sh"}]}]}}`
	if err := os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wireHooks(root, "brain", true); err != nil {
		t.Fatal(err)
	}
	cfg := readJSON(t, filepath.Join(root, ".claude", "settings.json"))
	for _, e := range cfg["hooks"].(map[string]any)["SessionStart"].([]any) {
		m := e.(map[string]any)
		cmd := m["hooks"].([]any)[0].(map[string]any)["command"].(string)
		_, has := m["matcher"]
		if cmd == wake && has {
			t.Error("the brain's own SessionStart kept the matcher Devin does not match")
		}
		if cmd == "./mine.sh" && !has {
			t.Error("somebody else's matcher was removed")
		}
	}
}

// --- What the wired hooks deliver -------------------------------------------------------

// The session-start brief is now the only place an agent learns how to use the brain, so
// every host's envelope must carry the same body, in the field that host reads.
func TestWakeupEnvelopesCarryTheSameBody(t *testing.T) {
	s := newTestStore(t)
	plain := &strings.Builder{}
	if err := s.Wakeup(plain, WakeupArgs{NoTree: true, NoProbes: true, NoCursor: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain.String(), "THIS PROJECT HAS A BRAIN") {
		t.Error("wakeup no longer says how to use the brain, which is now nowhere else")
	}

	for _, host := range []string{"cursor", "gemini"} {
		out := &strings.Builder{}
		if err := s.Wakeup(out, WakeupArgs{Emit: host, NoTree: true, NoProbes: true, NoCursor: true}); err != nil {
			t.Fatal(err)
		}
		var env map[string]any
		if err := json.Unmarshal([]byte(out.String()), &env); err != nil {
			t.Fatalf("%s envelope is not JSON: %v\n%s", host, err, out.String())
		}
		var body string
		switch host {
		case "cursor":
			body, _ = env["additional_context"].(string)
		case "gemini":
			inner, _ := env["hookSpecificOutput"].(map[string]any)
			if inner["hookEventName"] != "SessionStart" {
				t.Errorf("gemini envelope does not name the event: %v", inner)
			}
			body, _ = inner["additionalContext"].(string)
		}
		if !strings.Contains(body, "THIS PROJECT HAS A BRAIN") {
			t.Errorf("%s envelope carries no brief:\n%s", host, out.String())
		}
	}
}

// An envelope no host reads would be printed and ignored, so it is refused by name.
func TestWakeupRefusesAnEnvelopeNoHostReads(t *testing.T) {
	s := newTestStore(t)
	err := s.Wakeup(io.Discard, WakeupArgs{Emit: "copilot"})
	if err == nil || !strings.Contains(err.Error(), "unknown envelope") {
		t.Fatalf("an envelope nothing reads was accepted: %v", err)
	}
}

// The block a session reads before its first turn used to file everything outside the core
// commands under "maintenance". A session asked for a weekly reminder read that, reached for
// the host's own scheduler, and had to be stopped and told the brain has tasks. The working
// domains have to be named where that decision is made.
func TestSessionOpenNamesTheDomainsThatAreNotMaintenance(t *testing.T) {
	block := strings.Join(usingThisBrain(), "\n")
	for _, want := range []string{"task", "recurrence", "measure", "os-brain help"} {
		if !strings.Contains(block, want) {
			t.Errorf("the session-open block never mentions %q:\n%s", want, block)
		}
	}
	if strings.Contains(block, "Everything else is maintenance") {
		t.Error("back to filing tasks and measures under maintenance")
	}
}

// Cursor runs .claude/settings.json as well as .cursor/hooks.json, so a project wired for
// both was briefed twice per session. The plain hook stays silent only when the payload says
// Cursor is the host and Cursor's own hook is wired. Every other case keeps the brief: going
// silent by mistake costs the session its brain, a duplicate is only noise.
func TestCursorDeliversBriefOnlyWhenCursorsOwnHookWill(t *testing.T) {
	wired := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wired, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wired, ".cursor", "hooks.json"), []byte(`{"version":1,"hooks":{
		"sessionStart":[{"command":"`+wired+`/brain/autoexec.sh wakeup --emit cursor"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	bare := t.TempDir()
	cursor := func(root string) []byte {
		return []byte(`{"hook_event_name":"sessionStart","cursor_version":"2026.09.23-86fc751","workspace_roots":["` + root + `"]}`)
	}
	for _, c := range []struct {
		name    string
		payload []byte
		want    bool
	}{
		{"cursor, its own hook wired", cursor(wired), true},
		{"cursor, no brain hook in .cursor", cursor(bare), false},
		{"claude code", []byte(`{"hook_event_name":"SessionStart","session_id":"x","cwd":"` + wired + `"}`), false},
		{"no payload", nil, false},
		{"garbage", []byte("not json"), false},
		{"cursor, another event", []byte(`{"hook_event_name":"stop","cursor_version":"1","workspace_roots":["` + wired + `"]}`), false},
	} {
		if got := CursorDeliversBrief(c.payload); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
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

// --- Helpers ----------------------------------------------------------------------------

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("%s is not valid JSON: %v", path, err)
	}
	return out
}

// nested returns the first command wired for event in the Claude-style shape that Codex,
// Gemini and Qwen share: hooks -> event -> [{hooks: [{command}]}].
func nested(t *testing.T, cfg map[string]any, event string) string {
	t.Helper()
	hooks, _ := cfg["hooks"].(map[string]any)
	list, _ := hooks[event].([]any)
	if len(list) == 0 {
		t.Fatalf("%s is not wired: %v", event, hooks)
	}
	inner, _ := list[0].(map[string]any)["hooks"].([]any)
	if len(inner) == 0 {
		t.Fatalf("%s has no command: %v", event, list[0])
	}
	cmd, _ := inner[0].(map[string]any)["command"].(string)
	return cmd
}

// strconvQuote quotes s as a JSON string, for building settings files by hand.
func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }
