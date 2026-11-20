package brain

// Tests for what `os-brain init` creates in a project and what it must leave alone. The
// guards against a second brain and the .gitignore coverage are in cmd_init_guard_test.go,
// the warnings about other memory stores in cmd_init_competing_test.go, and the per-host
// hook wiring in autoexec_test.go.

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// initEnv returns a fresh project directory with every path os-brain resolves pinned
// inside it, so a test can never read or write the developer's real stores. Host detection
// reads $HOME as well as the project, so a developer's own ~/.claude would otherwise decide
// what these tests wire; $HOME points somewhere empty and each test says what is installed.
func initEnv(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "cfg"))
	t.Setenv("OS_BRAIN_GLOBAL_DB", filepath.Join(root, "cfg", "brain", "brain.db"))
	t.Setenv("OS_BRAIN_HOME", "")
	t.Setenv("OS_BRAIN_PROJECT", "")
	t.Setenv("OS_BRAIN_DB", "")
	return root
}

// The baseline: one command gives a project a brain, keeps the store out of git and wires
// Claude Code's session-start and per-prompt hooks.
func TestInitCreatesABrainAndWiresBothHooks(t *testing.T) {
	root := initEnv(t)
	if err := Init(io.Discard, root, InitArgs{Agents: "claude"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"brain/brain.json", "brain/brain.db", ".gitignore",
		".claude/settings.json"} {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Errorf("%s was not created", p)
		}
	}
	body, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v", err)
	}
	hooks := cfg["hooks"].(map[string]any)
	for _, ev := range []string{"SessionStart", "UserPromptSubmit"} {
		if _, ok := hooks[ev]; !ok {
			t.Errorf("%s was not wired", ev)
		}
	}
}

// The slash commands are thin wrappers around os-brain commands. Each is checked to exist,
// and two are checked to wrap the command they are named for.
func TestInitWritesSlashCommandsForClaude(t *testing.T) {
	root := initEnv(t)
	if err := Init(io.Discard, root, InitArgs{Agents: "claude"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"who.md", "msg.md", "rdo.md", "dnd.md", "inbox.md", "accept.md", "reject.md"} {
		if _, err := os.Stat(filepath.Join(root, ".claude", "commands", name)); err != nil {
			t.Errorf(".claude/commands/%s not written: %v", name, err)
		}
	}
	who, err := os.ReadFile(filepath.Join(root, ".claude", "commands", "who.md"))
	if err != nil {
		t.Fatalf(".claude/commands/who.md not written: %v", err)
	}
	if !strings.Contains(string(who), "os-brain sessions") {
		t.Errorf("who.md does not wrap `os-brain sessions`:\n%s", who)
	}
	rdo, err := os.ReadFile(filepath.Join(root, ".claude", "commands", "rdo.md"))
	if err != nil {
		t.Fatalf(".claude/commands/rdo.md not written: %v", err)
	}
	if !strings.Contains(string(rdo), "--rdo") {
		t.Errorf("rdo.md does not wrap `os-brain dm --rdo`:\n%s", rdo)
	}
}

// .claude/commands only means something to Claude Code. Writing it for a Gemini-only
// project would leave a directory nobody reads and make the project look Claude-wired.
func TestInitSkipsSlashCommandsWithoutClaude(t *testing.T) {
	root := initEnv(t)
	if err := Init(io.Discard, root, InitArgs{Agents: "gemini"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "commands")); !os.IsNotExist(err) {
		t.Errorf(".claude/commands must not exist when Claude Code is not a wired host")
	}
}

// os-brain lives on PATH, so a bare `os-brain brief` could resolve a brain next to the
// executable in /usr/local/bin. The hook has to say which brain it means.
func TestTheHookNamesTheBrainRatherThanTheBinarysNeighbour(t *testing.T) {
	root := initEnv(t)
	if err := Init(io.Discard, root, InitArgs{Agents: "claude"}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	text := string(body)
	if !strings.Contains(text, "OS_BRAIN_HOME") || !strings.Contains(text, "CLAUDE_PROJECT_DIR") {
		t.Errorf("the hook does not anchor the brain to the project:\n%s", text)
	}
}

// People re-run init to repair a setup. Re-running on a live brain must never cost a
// memory, must say there was nothing to do, and must not wire the hooks a second time.
func TestInitIsIdempotentAndAdditive(t *testing.T) {
	root := initEnv(t)
	if err := Init(io.Discard, root, InitArgs{Agents: "claude"}); err != nil {
		t.Fatal(err)
	}
	here := filepath.Join(root, "brain")
	st, err := Open(NewEnv(here))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Remember(io.Discard, RememberArgs{Title: "a fact that must survive init",
		Type: "infra", Scope: "global", Importance: 3, Source: "session"}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	var out strings.Builder
	if err := Init(&out, root, InitArgs{Agents: "claude"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "nothing to do") {
		t.Errorf("a second init reported work it did not do:\n%s", out.String())
	}
	st, err = Open(NewEnv(here))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if n, _ := st.count("SELECT COUNT(*) FROM memories"); n != 1 {
		t.Errorf("init disturbed an existing brain: %d memories", n)
	}
	body, _ := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if strings.Count(string(body), "os-brain wakeup") != 1 {
		t.Error("the SessionStart hook was added twice")
	}
}

// The database holds everything the brain remembers. It must be ignored by git, and the
// project's own .gitignore entries must survive the edit.
func TestInitKeepsTheStoreOutOfGit(t *testing.T) {
	root := initEnv(t)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"),
		[]byte("node_modules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Init(io.Discard, root, InitArgs{Agents: "claude"}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	text := string(body)
	if !strings.Contains(text, "brain/brain.db") {
		t.Errorf(".gitignore does not cover the store:\n%s", text)
	}
	if !strings.Contains(text, "node_modules") {
		t.Error("init clobbered the project's own .gitignore")
	}
}

// Silently rewriting somebody's config is worse than not wiring the hook. init leaves a
// settings file it cannot parse byte for byte as it was, and fails, because the only host it
// was asked to wire is now unwired.
func TestInitLeavesAnUnparseableSettingsFileAlone(t *testing.T) {
	root := initEnv(t)
	if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	junk := "{ this is not json"
	p := filepath.Join(root, ".claude", "settings.json")
	if err := os.WriteFile(p, []byte(junk), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Init(io.Discard, root, InitArgs{Agents: "claude"})
	if err == nil {
		t.Fatal("init reported success while the only host it was asked to wire went unwired")
	}
	body, _ := os.ReadFile(p)
	if string(body) != junk {
		t.Errorf("init rewrote a settings file it could not parse:\n%s", body)
	}
}

// --no-hook, --no-gitignore and --project each turn off or replace one thing init does.
func TestInitRespectsItsOptOuts(t *testing.T) {
	root := initEnv(t)
	if err := Init(io.Discard, root, InitArgs{NoHook: true, NoGitgnore: true,
		Project: "named-by-hand"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Error("--no-hook still touched settings.json")
	}
	if _, err := os.Stat(filepath.Join(root, ".gitignore")); !os.IsNotExist(err) {
		t.Error("--no-gitignore still wrote .gitignore")
	}
	cfg := mustConfig(t, filepath.Join(root, "brain", "brain.json"))
	if cfg.Project != "named-by-hand" {
		t.Errorf("project is %q", cfg.Project)
	}
}

// CLAUDE.md is the project's own file. init used to append a section to it, which was the
// last place the brain was activated by an instruction instead of a hook. init never writes
// to it now.
func TestInitNeverWritesToTheProjectsClaudeMD(t *testing.T) {
	root := initEnv(t)
	own := "# My project\n\nDo not touch this.\n"
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Init(io.Discard, root, InitArgs{Agents: "claude"}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if string(body) != own {
		t.Errorf("init edited CLAUDE.md:\n%s", body)
	}
}

// A project initialised by an older version still carries the old section. It is now
// redundant, so init reports it, but removing text from the project's own file is the
// developer's call.
func TestInitReportsTheOldClaudeMDSectionWithoutRemovingIt(t *testing.T) {
	root := initEnv(t)
	old := "# My project\n\n## Project memory\n\nSee `brain/HOWTO.md` for the mechanics.\n"
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := Init(&out, root, InitArgs{Agents: "claude"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "CLAUDE.md still carries") {
		t.Errorf("the redundant section went unreported:\n%s", out.String())
	}
	body, _ := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if string(body) != old {
		t.Error("init deleted the section instead of reporting it")
	}
}
