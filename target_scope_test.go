package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// twoLocalProjects builds sibling projects pa and pb, each an initialised private brain
// with a memory, pins every machine-level path and runs the test from inside pa.
func twoLocalProjects(t *testing.T) (pa, pb string) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(base, "cfg"))
	t.Setenv("OS_BRAIN_GLOBAL_DB", filepath.Join(base, "cfg", "brain", "brain.db"))
	t.Setenv("OS_BRAIN_HOME", "")
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	for _, name := range []string{"pa", "pb"} {
		root := filepath.Join(base, name)
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Chdir(root)
		if err := run([]string{"init", "--agent", "claude"}, io.Discard); err != nil {
			t.Fatalf("init %s: %v", name, err)
		}
		if err := run([]string{"remember", name + " project decision about the release train",
			"--body", "only copy of " + name}, io.Discard); err != nil {
			t.Fatalf("remember %s: %v", name, err)
		}
	}
	pa, pb = filepath.Join(base, "pa"), filepath.Join(base, "pb")
	t.Chdir(pa)
	return pa, pb
}

// A command must act on the brain it opened and nothing else. uninstall once took its store
// from the working directory and its files from its argument, so `uninstall ../pb` run in
// pa backed up pa's memories and then deleted pb's only brain.db. Naming any other
// project, by path, by --dir or through OS_BRAIN_HOME, is now refused with nothing
// changed and uninstalling the brain actually opened still works and backs up that one.
func TestUninstallOfAnotherProjectIsRefusedAndLeavesItIntact(t *testing.T) {
	pa, pb := twoLocalProjects(t)
	for _, argv := range [][]string{
		{"uninstall", pb, "--purge", "--apply"},
		{"uninstall", "../pb", "--purge", "--apply"},
		{"uninstall", "--dir", "../pb/brain", "--purge", "--apply"},
		{"uninstall", pb},
	} {
		err := run(argv, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "Nothing was changed") {
			t.Errorf("%v was not refused: %v", argv, err)
		}
	}
	for _, p := range []string{filepath.Join(pb, "brain", "brain.db"),
		filepath.Join(pb, "brain", "brain.json"), filepath.Join(pa, "brain", "brain.db")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s is gone: %v", p, err)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(pb, "uninstall-backup-*")); len(m) > 0 {
		t.Errorf("a dump was written into the other project: %v", m)
	}

	// OS_BRAIN_HOME naming pb while the argument is pa is the same ambiguity.
	t.Setenv("OS_BRAIN_HOME", filepath.Join(pb, "brain"))
	if err := run([]string{"uninstall", pa, "--purge", "--apply"}, io.Discard); err == nil {
		t.Error("uninstall pa with OS_BRAIN_HOME on pb was not refused")
	}
	if _, err := os.Stat(filepath.Join(pa, "brain", "brain.db")); err != nil {
		t.Errorf("pa's brain.db is gone: %v", err)
	}

	// The brain this command opened is still uninstallable and the backup is of it.
	t.Setenv("OS_BRAIN_HOME", "")
	var out bytes.Buffer
	if err := run([]string{"uninstall", "--purge", "--apply"}, &out); err != nil {
		t.Fatalf("uninstall of the opened brain: %v\n%s", err, out.String())
	}
	dumps, _ := filepath.Glob(filepath.Join(pa, "uninstall-backup-*.json"))
	if len(dumps) != 1 {
		t.Fatalf("expected one dump in pa: %v\n%s", dumps, out.String())
	}
	raw, _ := os.ReadFile(dumps[0])
	if !strings.Contains(string(raw), "only copy of pa") || strings.Contains(string(raw), "only copy of pb") {
		t.Errorf("the dump is not pa's store:\n%s", raw)
	}
	if _, err := os.Stat(filepath.Join(pb, "brain", "brain.db")); err != nil {
		t.Errorf("pb's brain.db is gone: %v", err)
	}
}

// From a subdirectory, discovery finds the project's brain and uninstall plans to act on it,
// not on a <subdir>/brain that does not exist.
func TestUninstallFromASubdirectoryActsOnTheDiscoveredBrain(t *testing.T) {
	pa, _ := twoLocalProjects(t)
	sub := filepath.Join(pa, "src", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	var out bytes.Buffer
	if err := run([]string{"uninstall"}, &out); err != nil {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(out.String(), filepath.Join(pa, "brain", "brain.json")) {
		t.Errorf("the plan does not name pa's brain:\n%s", out.String())
	}
}

// uninstall once treated every hook containing "os-brain" as this brain's, so removing
// brain/ also stripped the hooks of a second brain in the same project and of another
// tool whose name merely contains the word. Only hooks that name this brain may go.
func TestUninstallRemovesOnlyThisBrainsHooks(t *testing.T) {
	pa, _ := twoLocalProjects(t)
	path := filepath.Join(pa, ".claude", "settings.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	hooks := cfg["hooks"].(map[string]any)
	others := []string{
		`OS_BRAIN_HOME="$CLAUDE_PROJECT_DIR/marketing/brain" os-brain wakeup`,
		`my-os-brain-linter --check brain`,
		`./tools/os-brain-audit.sh brain`,
	}
	for _, c := range others {
		hooks["SessionStart"] = append(hooks["SessionStart"].([]any), map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": c}}})
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"uninstall", "--apply"}, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	after, _ := os.ReadFile(path)
	for _, c := range others {
		q, _ := json.Marshal(c)
		if !bytes.Contains(after, q[1:len(q)-1]) {
			t.Errorf("uninstall removed a hook that is not this brain's: %s\n%s", c, after)
		}
	}
	if strings.Contains(string(after), `$CLAUDE_PROJECT_DIR/brain\" os-brain`) {
		t.Errorf("this brain's hooks survived:\n%s", after)
	}
}

// Over MCP, --body-file once read any file the server process could read and recall then
// handed its contents to the caller: any connected client could read /etc/passwd or a key
// file through the brain. File arguments arriving over MCP must stay inside the project,
// whether the escape is an absolute path, a symlink or a run of "..". A file inside the
// project still works.
func TestMCPRefusesFileArgumentsOutsideTheProject(t *testing.T) {
	mcpEnv(t)
	home := os.Getenv("OS_BRAIN_HOME")
	t.Chdir(filepath.Dir(home))
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("outside the project"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(home), "innocent.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{
		{"title": "host passwd file", "body-file": "/etc/passwd"},
		{"title": "a linked file", "body-file": link},
		{"title": "a relative escape", "body-file": "../../../../../../etc/passwd"},
	} {
		out, isErr := callText(t, s, "brain_remember", args)
		if !isErr || !strings.Contains(out, "outside the project") {
			t.Errorf("brain_remember %v was not refused: %s", args, out)
		}
	}
	for _, tool := range []string{"brain_import", "brain_absorb"} {
		out, isErr := callText(t, s, tool, map[string]any{"args": "/etc/passwd"})
		if !isErr || !strings.Contains(out, "outside the project") {
			t.Errorf("%s /etc/passwd was not refused: %s", tool, out)
		}
	}
	out, _ := callText(t, s, "brain_recall", map[string]any{"args": "passwd", "json": true})
	if strings.Contains(out, "root:") {
		t.Fatalf("/etc/passwd reached the brain: %s", out)
	}

	inside := filepath.Join(filepath.Dir(home), "notes.txt")
	if err := os.WriteFile(inside, []byte("the deploy needs the vault unsealed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, isErr := callText(t, s, "brain_remember", map[string]any{
		"title": "deploy needs the vault unsealed", "body-file": inside}); isErr {
		t.Errorf("a file inside the project was refused: %s", out)
	}
}
