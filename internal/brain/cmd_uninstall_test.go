package brain

// Tests for `os-brain uninstall`. A session once asked to remove a brain improvised the
// removal and deleted the whole .claude/settings.json instead of the hooks the brain had put
// there. The command exists so nobody has to improvise: it removes only what init added,
// shows a dry run first, and keeps the database unless told otherwise.

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installedProject builds a project that has its own CLAUDE.md, .gitignore and Claude Code
// settings (a permission and a hook of its own), installs a brain into it and stores one
// memory. Everything the project owned before init must survive uninstall.
func installedProject(t *testing.T) (root string, st *Store) {
	t.Helper()
	root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"),
		[]byte("# Mine\n\nKeep this.\n\n## Another section\n\nAnd this.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"),
		[]byte("node_modules/\n*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude", "settings.json"),
		[]byte(`{"permissions":{"allow":["Bash(ls:*)"]},"hooks":{"PreToolUse":[{"hooks":`+
			`[{"type":"command","command":"my-own-guard.sh"}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "cfg"))
	t.Setenv("OS_BRAIN_GLOBAL_DB", filepath.Join(root, "cfg", "g.db"))
	if err := Init(io.Discard, root, InitArgs{Dir: "brain", Agents: "claude"}); err != nil {
		t.Fatal(err)
	}
	st, err := Open(NewEnv(filepath.Join(root, "brain")))
	if err != nil {
		t.Fatal(err)
	}
	remember(t, st, RememberArgs{Scope: "global", Type: "infra",
		Title: "nginx holds the wildcard certificate", Body: "x"})
	return root, st
}

// Removing things is the dangerous direction, so the default is to print the plan, say the
// database will be kept and name the flags that act.
func TestUninstallIsADryRunByDefault(t *testing.T) {
	root, st := installedProject(t)
	var buf strings.Builder
	if err := Uninstall(&buf, st, root, UninstallArgs{Dir: "brain"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"DRY RUN", "KEEP the database", "--apply", "--purge"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("plan does not mention %q:\n%s", want, buf.String())
		}
	}
	if _, err := os.Stat(filepath.Join(root, "brain", "brain.json")); err != nil {
		t.Fatal("the dry run removed something")
	}
}

// settings.json, CLAUDE.md and .gitignore belong to the project. Uninstall takes out the
// brain's hooks and ignore lines and leaves every other key, hook, section and line.
func TestUninstallRemovesOnlyWhatInitAdded(t *testing.T) {
	root, st := installedProject(t)
	if err := Uninstall(io.Discard, st, root, UninstallArgs{Dir: "brain", Apply: true}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if err != nil {
		t.Fatal("settings.json was deleted; it belongs to the project, not the brain")
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg["permissions"]; !ok {
		t.Error("the project's permissions block was removed")
	}
	hooks, _ := cfg["hooks"].(map[string]interface{})
	if _, ok := hooks["PreToolUse"]; !ok {
		t.Error("the project's own PreToolUse hook was removed")
	}
	if _, ok := hooks["SessionStart"]; ok {
		t.Error("the brain's SessionStart hook survived")
	}
	claude, _ := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	for _, want := range []string{"Keep this.", "## Another section", "And this."} {
		if !strings.Contains(string(claude), want) {
			t.Errorf("CLAUDE.md lost %q:\n%s", want, claude)
		}
	}
	if strings.Contains(string(claude), "brain/HOWTO.md") {
		t.Error("the brain's CLAUDE.md section survived")
	}
	ignore, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	if !strings.Contains(string(ignore), "node_modules/") {
		t.Error(".gitignore lost the project's own entries")
	}
	if strings.Contains(string(ignore), "brain/brain.db") {
		t.Error("the brain's .gitignore entries survived")
	}
}

// The database is the only thing here that cannot be recreated by running one command.
// Without --purge it stays, and running init again brings the brain back around it.
func TestUninstallKeepsTheDatabaseUnlessPurgeIsGiven(t *testing.T) {
	root, st := installedProject(t)
	db := filepath.Join(root, "brain", "brain.db")
	if err := Uninstall(io.Discard, st, root, UninstallArgs{Dir: "brain", Apply: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(db); err != nil {
		t.Fatal("uninstall deleted the database without --purge")
	}
	if err := Init(io.Discard, root, InitArgs{Dir: "brain", Agents: "claude"}); err != nil {
		t.Fatal(err)
	}
	st2, err := Open(NewEnv(filepath.Join(root, "brain")))
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	var n int
	if err := st2.DB.QueryRow("SELECT COUNT(*) FROM memories").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reinstalling lost memories: %d", n)
	}
}

// --purge deletes the brain directory, so the safety dump it writes first has to land
// outside that directory, or it would be deleted with it.
func TestUninstallPurgeDumpsFirstAndKeepsTheDumpOutsideTheDirectory(t *testing.T) {
	root, st := installedProject(t)
	if err := Uninstall(io.Discard, st, root, UninstallArgs{Dir: "brain", Apply: true, Purge: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "brain", "brain.db")); err == nil {
		t.Fatal("--purge did not delete the database")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var dump string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "uninstall-backup-") {
			dump = e.Name()
		}
	}
	if dump == "" {
		t.Fatal("no dump was left outside the deleted directory")
	}
	body, err := os.ReadFile(filepath.Join(root, dump))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "nginx holds the wildcard certificate") {
		t.Fatal("the dump does not contain the memories it was protecting")
	}
}

// A mistyped --dir must not be treated as a brain to remove.
func TestUninstallRefusesWhereThereIsNoBrain(t *testing.T) {
	root, st := installedProject(t)
	err := Uninstall(io.Discard, st, root, UninstallArgs{Dir: "not-a-brain", Apply: true})
	if err == nil || !strings.Contains(err.Error(), "nothing to uninstall") {
		t.Fatalf("expected a refusal, got %v", err)
	}
}

// --- Uninstalling from a shared store ---------------------------------------------------
//
// On a shared store there is no file to delete: this project's memories are rows in one
// database that other projects use too. The plan used to name brain/brain.db either way, a
// path that does not exist in this case, and --purge reported N memories deleted while
// deleting none of them. They stayed in the shared file, and the index row that pointed at
// them was gone.

// sharedInstall turns installedProject's brain into one on the shared store and gives it
// two memories.
func sharedInstall(t *testing.T) (string, *Store) {
	t.Helper()
	root, st := installedProject(t)
	st.Close()
	cfgPath := filepath.Join(root, "brain", "brain.json")
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg["shared"] = true
	out, _ := json.Marshal(cfg)
	if err := os.WriteFile(cfgPath, out, 0o600); err != nil {
		t.Fatal(err)
	}
	st2, err := Open(NewEnv(filepath.Join(root, "brain")))
	if err != nil {
		t.Fatal(err)
	}
	if !st2.Env.Shared {
		t.Fatal("the fixture did not end up on the shared store")
	}
	remember(t, st2, RememberArgs{Scope: "global", Type: "infra",
		Title: "nginx holds the wildcard certificate", Body: "x"})
	remember(t, st2, RememberArgs{Scope: "global", Type: "knowledge",
		Title: "the shared one", Body: "y"})
	return root, st2
}

// The plan must name the shared database it will touch, say it is shared, and not name a
// local file that does not exist.
func TestUninstallOnASharedStoreTalksAboutRowsNotAFile(t *testing.T) {
	root, st := sharedInstall(t)
	defer st.Close()
	var buf strings.Builder
	if err := Uninstall(&buf, st, root, UninstallArgs{Dir: "brain"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), st.Env.DBPath) {
		t.Errorf("the plan does not name the database it would touch:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), filepath.Join(root, "brain", "brain.db")) {
		t.Errorf("the plan names a local database that does not exist:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "shared") {
		t.Errorf("the plan does not say the store is shared:\n%s", buf.String())
	}
}

// --purge on a shared store deletes this project's rows and their search index entries, and
// nothing that belongs to another project.
func TestUninstallPurgeOnASharedStoreDeletesRowsAndSparesEveryoneElse(t *testing.T) {
	root, st := sharedInstall(t)
	neighbour := *st
	neighbour.Env.Project = "someone-else"
	remember(t, &neighbour, RememberArgs{Scope: "global", Type: "knowledge",
		Title: "not yours to delete", Body: "z"})

	sharedDB, project := st.Env.DBPath, st.Env.Project
	var buf strings.Builder
	if err := Uninstall(&buf, st, root, UninstallArgs{Dir: "brain", Apply: true,
		Purge: true}); err != nil {
		t.Fatal(err)
	}
	// Reopen the shared file by path: brain.json is gone, so resolving the environment
	// from the directory would silently create a fresh local database and read that.
	t.Setenv("OS_BRAIN_DB", sharedDB)
	check, err := Open(NewEnv(filepath.Join(root, "brain")))
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var mine, theirs, fts int
	if err := check.DB.QueryRow("SELECT COUNT(*) FROM memories WHERE project=?",
		project).Scan(&mine); err != nil {
		t.Fatal(err)
	}
	if err := check.DB.QueryRow("SELECT COUNT(*) FROM memories WHERE project='someone-else'").
		Scan(&theirs); err != nil {
		t.Fatal(err)
	}
	if err := check.DB.QueryRow("SELECT COUNT(*) FROM mem_fts").Scan(&fts); err != nil {
		t.Fatal(err)
	}
	if mine != 0 {
		t.Errorf("--purge left %d of this project's memories in the shared store", mine)
	}
	if theirs != 1 {
		t.Errorf("--purge took a neighbouring project's memories: %d left of 1", theirs)
	}
	if fts != 1 {
		t.Errorf("the shared search index holds %d rows, expected the neighbour's 1", fts)
	}
	if !strings.Contains(buf.String(), "no other project touched") {
		t.Errorf("the report does not say whose memories went:\n%s", buf.String())
	}
}

// The dump lands in the project directory. On a shared store a whole-file backup would
// copy every other project's memories into it, so the dump holds this project's alone.
func TestASharedUninstallDumpsOnlyThisProject(t *testing.T) {
	root, st := sharedInstall(t)
	neighbour := *st
	neighbour.Env.Project = "someone-else"
	remember(t, &neighbour, RememberArgs{Scope: "global", Type: "knowledge",
		Title: "a neighbours secret", Body: "z"})
	if err := Uninstall(io.Discard, st, root, UninstallArgs{Dir: "brain", Apply: true,
		Purge: true}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var dump string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "uninstall-backup-") {
			dump = filepath.Join(root, e.Name())
		}
	}
	if dump == "" {
		t.Fatal("no dump was written")
	}
	body, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "the shared one") {
		t.Error("the dump is missing this project's memories")
	}
	if strings.Contains(string(body), "a neighbours secret") {
		t.Error("the dump copied another project's memories into this directory")
	}
}

// --- Hooks and plugins in every host ----------------------------------------------------

// A brain removed from one agent and left wired in another leaves a hook running a command
// whose brain has just been deleted. Uninstall must name and unwire every host init wired,
// and remove the hook shim.
func TestUninstallUnwiresEveryHostItWired(t *testing.T) {
	root := initEnv(t)
	if err := Init(io.Discard, root, InitArgs{Dir: "brain", Agents: "claude,cursor,codex,gemini"}); err != nil {
		t.Fatal(err)
	}
	st, err := Open(NewEnv(filepath.Join(root, "brain")))
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	if err := Uninstall(&plan, st, root, UninstallArgs{Dir: "brain"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{".claude", ".codex", ".cursor", ".gemini", "autoexec.sh"} {
		if !strings.Contains(plan.String(), want) {
			t.Errorf("the plan does not name %s:\n%s", want, plan.String())
		}
	}
	if err := Uninstall(io.Discard, st, root, UninstallArgs{Dir: "brain", Apply: true}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".claude/settings.json", ".codex/hooks.json",
		".cursor/hooks.json", ".gemini/settings.json"} {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			continue
		}
		if strings.Contains(string(body), "wakeup") {
			t.Errorf("%s still runs the brain:\n%s", p, body)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "brain", "autoexec.sh")); !os.IsNotExist(err) {
		t.Error("the hook shim was left behind")
	}
}

// The OpenCode plugin is a file init wrote, so uninstall lists it in the plan and removes it.
func TestUninstallRemovesItsOwnOpenCodePluginAndNobodyElses(t *testing.T) {
	root, st := installedProject(t)
	if _, err := Autoexec(root, "brain", "opencode", true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".opencode", "plugins", "brain.js")
	var plan strings.Builder
	if err := Uninstall(&plan, st, root, UninstallArgs{Dir: "brain"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.String(), "agent plugin") {
		t.Errorf("the dry run does not mention the plugin:\n%s", plan.String())
	}
	if err := Uninstall(io.Discard, st, root, UninstallArgs{Dir: "brain", Apply: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the plugin init wrote survived uninstall")
	}
}
