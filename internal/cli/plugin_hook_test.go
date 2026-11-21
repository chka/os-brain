package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plugin-hook is what an agent's plugin runs. A plugin is installed once per person and
// fires in every project that person opens, so unlike init's per-project hooks it has to
// find the brain itself, stay quiet in a project with no brain and step aside in a project
// whose own hooks already brief the session (or the session is briefed twice).

// writeFile creates path and any missing parent directories, holding body.
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The plugin steps aside only for the agent whose project config already runs os-brain,
// in either shape init writes (os-brain directly, or the autoexec.sh shim) and from the
// project root or any subdirectory. An agent whose config exists but runs nothing of ours,
// or that has no config at all, still needs the plugin.
func TestPluginHookStepsAsideOnlyForTheAgentInitWired(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "brain")
	writeFile(t, filepath.Join(home, "brain.json"), `{}`)
	writeFile(t, filepath.Join(root, ".claude", "settings.json"),
		`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"OS_BRAIN_HOME=\"$CLAUDE_PROJECT_DIR/brain\" os-brain wakeup"}]}]}}`)
	writeFile(t, filepath.Join(root, ".codex", "hooks.json"),
		`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"'/x/brain/autoexec.sh' wakeup --emit codex"}]}]}}`)
	writeFile(t, filepath.Join(root, ".gemini", "settings.json"), `{"theme":"dark"}`)
	sub := filepath.Join(root, "src", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	for host, want := range map[string]bool{
		"claude": true, "copilot": true, "devin": true, // all three run .claude/settings.json
		"codex":  true,
		"gemini": false, // its settings exist but run nothing of ours
		"cursor": false, "qwen": false,
	} {
		for _, from := range []string{root, sub} {
			if got := pluginProjectWired(from, home, host); got != want {
				t.Errorf("%s from %s: wired=%v, want %v", host, from, got, want)
			}
		}
	}
}

// The search for init's hooks stops at the directory holding the brain. A parent
// directory's config belongs to some other project and counting it made the plugin go
// quiet in a project nobody had wired.
func TestPluginHookDoesNotLookAboveTheBrainsProject(t *testing.T) {
	outer := t.TempDir()
	writeFile(t, filepath.Join(outer, ".claude", "settings.json"), `{"hooks":"os-brain wakeup"}`)
	root := filepath.Join(outer, "proj")
	home := filepath.Join(root, "brain")
	writeFile(t, filepath.Join(home, "brain.json"), `{}`)
	if pluginProjectWired(root, home, "claude") {
		t.Fatal("a parent directory's hooks are not this project's: the plugin went quiet " +
			"in a project nobody wired")
	}
}

// The project root comes from the host's variable first, because it names the root even
// when the agent's shell has wandered into a subdirectory; then from the payload (cwd, or
// Cursor's workspace_roots); and last from the process's own directory, which is also
// where a variable naming a missing directory or an unreadable payload ends up.
func TestPluginHookProjectRootPrefersTheHostsWord(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	t.Setenv("GEMINI_PROJECT_DIR", "")
	t.Setenv("CURSOR_PROJECT_DIR", "")
	t.Setenv("CLAUDE_PROJECT_DIR", a)
	if got := pluginProjectRoot([]byte(`{"cwd":"` + b + `"}`)); got != a {
		t.Fatalf("CLAUDE_PROJECT_DIR set: got %s", got)
	}
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	if got := pluginProjectRoot([]byte(`{"cwd":"` + b + `"}`)); got != b {
		t.Fatalf("payload cwd: got %s", got)
	}
	if got := pluginProjectRoot([]byte(`{"workspace_roots":["` + a + `"]}`)); got != a {
		t.Fatalf("Cursor's workspace_roots: got %s", got)
	}
	t.Setenv("CLAUDE_PROJECT_DIR", filepath.Join(a, "gone"))
	cwd, _ := os.Getwd()
	if got := pluginProjectRoot([]byte(`junk`)); got != cwd {
		t.Fatalf("a variable naming nothing and an unreadable payload must fall to cwd, got %s", got)
	}
}

// In a project with no brain, discovery's usual error would open every session in every
// repository on the machine with a failure. Instead session start says once, in plain text
// or in the host's envelope under --emit, how to make a brain; every other hook prints
// nothing; and the project is left exactly as it was.
func TestPluginHookWithNoBrainSpeaksOnceAndStoresNothing(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_PROJECT_DIR", root)
	t.Setenv("OS_BRAIN_HOME", "")

	var out bytes.Buffer
	if err := runPluginHook(&out, []string{"--host", "claude", "wakeup"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "os-brain init") || strings.HasPrefix(out.String(), "null") {
		t.Fatalf("session start must say how to make a brain, in plain text: %q", out.String())
	}

	out.Reset()
	if err := runPluginHook(&out, []string{"--host", "gemini", "wakeup", "--emit", "gemini"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"additionalContext"`) {
		t.Fatalf("under --emit the hint must arrive in the host's envelope: %q", out.String())
	}

	for _, sub := range []string{"hook-recall", "hook-stop", "hook-tool", "hook-end"} {
		out.Reset()
		if err := runPluginHook(&out, []string{"--host", "claude", sub}); err != nil || out.Len() != 0 {
			t.Fatalf("%s with no brain must say nothing: err=%v out=%q", sub, err, out.String())
		}
	}
	ents, _ := os.ReadDir(root)
	if len(ents) != 0 {
		t.Fatalf("a project with no brain must be left as it was, found %d entries", len(ents))
	}
}

// A plugin hook runs unattended in every project, so it accepts a known host and one of
// the hook subcommands and nothing that writes on a model's say-so.
func TestPluginHookRefusesWhatIsNotAHook(t *testing.T) {
	var out bytes.Buffer
	for _, argv := range [][]string{
		{"wakeup"},                                     // no host
		{"--host", "claud", "wakeup"},                  // a typo is not a host
		{"--host", "claude"},                           // nothing to run
		{"--host", "claude", "remember", "x"},          // a write on the plugin's say-so
		{"--host", "claude", "forget", "1", "--force"}, // and the worst of them
	} {
		if err := runPluginHook(&out, argv); err == nil {
			t.Fatalf("%v was accepted", argv)
		}
	}
}

// plugin-hook reads --emit out of argv before the subcommand parses it, so it must see the
// flag however the host spelled it and read a bare --emit as no value.
func TestFlagValueReadsBothSpellings(t *testing.T) {
	if flagValue([]string{"--emit", "codex"}, "emit") != "codex" ||
		flagValue([]string{"-emit=gemini"}, "emit") != "gemini" ||
		flagValue([]string{"--emit"}, "emit") != "" {
		t.Fatal("flagValue misread --emit")
	}
}
