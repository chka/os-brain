//go:build !windows

package integration_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/chka/os-brain/internal/dump"
	"github.com/chka/os-brain/internal/fsguard"
	"github.com/chka/os-brain/internal/install"
	"github.com/chka/os-brain/internal/memory"
	"github.com/chka/os-brain/internal/safety"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/tasks"
	"github.com/chka/os-brain/internal/timefmt"
)

// Tests for writing files safely inside a project that may be hostile. A cloned repository
// decides every path init writes, symbolic links included, because git checks a committed
// symlink out as a symlink. Each test below plants the links an attacker would commit and
// runs the real command against them.

// plant creates a symbolic link at link pointing to target, making the parent directories.
func plant(t *testing.T, link, target string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// writeFile writes body to path, making the parent directories.
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// unchanged fails the test if path no longer holds exactly want.
func unchanged(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if string(got) != want {
		t.Errorf("%s was written through a planted link; it now holds:\n%s", path, got)
	}
}

// stillLinkTo fails the test if link is no longer a symbolic link to target.
func stillLinkTo(t *testing.T, link, target string) {
	t.Helper()
	got, err := os.Readlink(link)
	if err != nil || got != target {
		t.Errorf("%s is no longer the link to %s (%q, %v)", link, target, got, err)
	}
}

// scratchHome pins the config dir and the shared store to a fresh temp dir, which stands in
// for the user's home: the files an attacker would aim a link at live there.
func scratchHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "cfg"))
	t.Setenv("OS_BRAIN_GLOBAL_DB", filepath.Join(home, "cfg", "brain", "brain.db"))
	return home
}

// bashrc stands in for a shell startup file: anything written into it runs in the user's
// next interactive shell.
const bashrc = "export PATH=$HOME/bin:$PATH\n"

// --- init ---

// The exploit: `.codex/hooks.json.tmp -> ~/.bashrc` beside a committed hooks.json whose key
// is a command substitution. init wrote the merged JSON through the fixed temporary name
// into ~/.bashrc, and the next interactive shell ran the key. init no longer uses fixed
// temporary names, so the planted links are left alone and so are their targets.
func TestInitNeverWritesThroughAPlantedTmpLink(t *testing.T) {
	home := scratchHome(t)
	root := t.TempDir()
	rc := filepath.Join(home, "bashrc")
	writeFile(t, rc, bashrc)
	writeFile(t, filepath.Join(root, ".codex", "hooks.json"), `{"$(touch PWNED)": 1}`)
	links := map[string]string{}
	for _, rel := range []string{".codex/hooks.json.tmp", ".claude/settings.json.tmp",
		".gemini/settings.json.tmp", ".cursor/hooks.json.tmp", ".qwen/settings.json.tmp",
		"brain/brain.json.tmp", ".mcp.json.tmp"} {
		links[filepath.Join(root, rel)] = rc
		plant(t, filepath.Join(root, rel), rc)
	}
	if err := install.Init(io.Discard, root, install.InitArgs{Agents: "claude,codex,gemini,cursor,qwen",
		Channel: true}); err != nil {
		t.Fatal(err)
	}
	unchanged(t, rc, bashrc)
	for link, target := range links {
		stillLinkTo(t, link, target)
	}
	for _, rel := range []string{".codex/hooks.json", ".claude/settings.json", ".mcp.json",
		"brain/brain.json"} {
		info, err := os.Lstat(filepath.Join(root, rel))
		if err != nil || !info.Mode().IsRegular() {
			t.Errorf("%s is not a regular file after init: %v", rel, err)
		}
	}
}

// `.claude/settings.json -> ~/.claude/settings.json`: init read the user's global settings
// through the link, merged its hooks in and renamed a regular file over the link, which put
// the API key from the global env into a file git sees. Now nothing is written at all, and
// the refusal says what to do instead.
func TestInitRefusesALinkedSettingsFileAndWritesNothing(t *testing.T) {
	home := scratchHome(t)
	root := t.TempDir()
	global := filepath.Join(home, "global-settings.json")
	secret := `{"env":{"ANTHROPIC_API_KEY":"sk-USER-SECRET"}}`
	writeFile(t, global, secret)
	link := filepath.Join(root, ".claude", "settings.json")
	plant(t, link, global)
	err := install.Init(io.Discard, root, install.InitArgs{Agents: "claude"})
	if err == nil {
		t.Fatal("init wrote through a settings.json that is a link to the user's global settings")
	}
	if !strings.Contains(err.Error(), "symbolic link") || !strings.Contains(err.Error(), "--no-hook") {
		t.Errorf("the refusal should name the link and the way through: %v", err)
	}
	unchanged(t, global, secret)
	stillLinkTo(t, link, global)
	if _, err := os.Stat(filepath.Join(root, "brain", "brain.json")); err == nil {
		t.Error("the refusal came after brain.json had already been written")
	}
}

// Both attacks above at once, the shape a hostile repository would actually ship.
func TestInitRefusesTheWholePlantedRepository(t *testing.T) {
	home := scratchHome(t)
	root := t.TempDir()
	rc := filepath.Join(home, "bashrc")
	global := filepath.Join(home, "global-settings.json")
	writeFile(t, rc, bashrc)
	writeFile(t, global, `{"env":{"K":"v"}}`)
	writeFile(t, filepath.Join(root, ".codex", "hooks.json"), `{"$(touch PWNED)": 1}`)
	plant(t, filepath.Join(root, ".codex", "hooks.json.tmp"), rc)
	plant(t, filepath.Join(root, ".claude", "settings.json.tmp"), rc)
	plant(t, filepath.Join(root, ".claude", "settings.json"), global)
	if err := install.Init(io.Discard, root, install.InitArgs{Agents: "claude,codex"}); err == nil {
		t.Fatal("init ran in a repository with a linked settings.json")
	}
	unchanged(t, rc, bashrc)
	unchanged(t, global, `{"env":{"K":"v"}}`)
	unchanged(t, filepath.Join(root, ".codex", "hooks.json"), `{"$(touch PWNED)": 1}`)
}

// Every other file init writes (the docs, the slash commands, the shim, .gitignore, the
// agent configs) and the brain directory itself. A link at any of them is refused before
// anything is written.
func TestInitRefusesEveryOtherLinkedFile(t *testing.T) {
	for _, rel := range []string{"brain/HOWTO.md", "brain/BEST-PRACTICES.md",
		"brain/autoexec.sh", ".claude/commands/who.md", ".gitignore", ".codex/hooks.json",
		"brain/brain.json", ".opencode/plugins/brain.js"} {
		t.Run(rel, func(t *testing.T) {
			home := scratchHome(t)
			root := t.TempDir()
			target := filepath.Join(home, "authorized_keys")
			writeFile(t, target, "ssh-ed25519 AAAA me\n")
			plant(t, filepath.Join(root, rel), target)
			if err := install.Init(io.Discard, root, install.InitArgs{Agents: "claude,codex,opencode"}); err == nil {
				t.Fatalf("init ran with %s linked out of the project", rel)
			}
			unchanged(t, target, "ssh-ed25519 AAAA me\n")
		})
	}
	t.Run("the brain directory", func(t *testing.T) {
		home := scratchHome(t)
		root := t.TempDir()
		elsewhere := filepath.Join(home, "dotfiles")
		writeFile(t, filepath.Join(elsewhere, "HOWTO.md"), "mine\n")
		plant(t, filepath.Join(root, "brain"), elsewhere)
		if err := install.Init(io.Discard, root, install.InitArgs{Agents: "claude"}); err == nil {
			t.Fatal("init wrote into a brain directory that is a link out of the project")
		}
		unchanged(t, filepath.Join(elsewhere, "HOWTO.md"), "mine\n")
		if _, err := os.Stat(filepath.Join(elsewhere, "brain.json")); err == nil {
			t.Error("brain.json was written into the link's target")
		}
	})
}

// --- the other commands that rewrite project files ---

// Every `scope add` and `context set` rewrites brain.json, and used to do it through a fixed
// brain.json.tmp. A planted temporary link is now ignored, and a brain.json that is itself
// a link is refused.
func TestSaveConfigNeverWritesThroughALink(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	here := filepath.Join(root, "brain")
	target := filepath.Join(home, "profile")
	writeFile(t, target, bashrc)
	writeFile(t, filepath.Join(here, "brain.json"), `{"project":"p"}`)
	plant(t, filepath.Join(here, "brain.json.tmp"), target)
	if err := store.SaveConfig(here, store.Config{Project: "p"}); err != nil {
		t.Fatal(err)
	}
	unchanged(t, target, bashrc)
	stillLinkTo(t, filepath.Join(here, "brain.json.tmp"), target)

	os.Remove(filepath.Join(here, "brain.json"))
	plant(t, filepath.Join(here, "brain.json"), target)
	if err := store.SaveConfig(here, store.Config{Project: "p"}); err == nil {
		t.Error("SaveConfig replaced a brain.json that is a link")
	}
	unchanged(t, target, bashrc)
}

// uninstall edits settings.json, CLAUDE.md and .gitignore. A link put there after install is
// refused up front, dry run included, rather than half way through.
func TestUninstallRefusesALinkedFileItWouldEdit(t *testing.T) {
	root, st := installedProject(t)
	defer st.Close()
	settings := filepath.Join(root, ".claude", "settings.json")
	body, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(t.TempDir(), "global-settings.json")
	writeFile(t, global, string(body))
	os.Remove(settings)
	plant(t, settings, global)
	for _, apply := range []bool{false, true} {
		if err := install.Uninstall(io.Discard, st, root, install.UninstallArgs{Dir: "brain", Apply: apply}); err == nil {
			t.Fatalf("uninstall (apply=%v) went ahead with settings.json linked out", apply)
		}
	}
	unchanged(t, global, string(body))
	if _, err := os.Stat(filepath.Join(root, "brain", "brain.json")); err != nil {
		t.Error("the refused uninstall still removed brain.json")
	}
}

// --- dump destinations ---
//
// Every command that dumps the brain before a destructive step (uninstall, the wipe, prune)
// takes a --backup path. The dump is the whole brain in cleartext, so each destination gets
// the same refusals: never over a file the command did not write, never outside the project
// unless asked.

// `uninstall --apply --backup ~/notes.txt` from a shared project replaced notes.txt, outside
// the project, with the dump.
func TestUninstallDumpTakesTheDumpRefusals(t *testing.T) {
	root, st := sharedInstall(t)
	defer st.Close()
	notes := filepath.Join(t.TempDir(), "notes.txt")
	writeFile(t, notes, "my notes\n")
	err := install.Uninstall(io.Discard, st, root, install.UninstallArgs{Dir: "brain", Apply: true, Backup: notes})
	if err == nil {
		t.Fatal("uninstall wrote its dump over a file outside the project")
	}
	unchanged(t, notes, "my notes\n")

	inside := filepath.Join(root, "notes.txt")
	writeFile(t, inside, "my notes\n")
	err = install.Uninstall(io.Discard, st, root, install.UninstallArgs{Dir: "brain", Apply: true, Backup: inside})
	if err == nil {
		t.Fatal("uninstall wrote its dump over a file it did not generate")
	}
	unchanged(t, inside, "my notes\n")
}

// `forget --all --apply --i-mean-it --backup .env` overwrote a gitignored .env.
func TestWipeDumpRefusesAFileItDidNotWrite(t *testing.T) {
	s := wipeFixture(t)
	env := filepath.Join(s.Env.ProjectRoot(), ".env")
	writeFile(t, env, "DB_PASSWORD=keepme\n")
	err := memory.New(s).Forget(io.Discard, memory.ForgetArgs{All: true, Apply: true, IMeanIt: true, Backup: env})
	if err == nil {
		t.Fatal("the wipe dump overwrote .env")
	}
	unchanged(t, env, "DB_PASSWORD=keepme\n")
	if got := countIn(t, s, "memories"); got != 3 {
		t.Errorf("the refused wipe still deleted: %d left", got)
	}
}

// agedStore is a store with one tier-D memory old enough for `prune --older-than 30`.
func agedStore(t *testing.T) *store.Store {
	t.Helper()
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Title: "the box is rebooting", Type: "state"})
	if _, err := s.DB.Exec("UPDATE memories SET updated_at=?", timefmt.Now()-40*86400); err != nil {
		t.Fatal(err)
	}
	return s
}

// `prune --backup NOTES.md` replaced a hand-written NOTES.md with the dump, and any path
// outside the project was written without a word. A refused dump must also stop the prune.
func TestPruneBackupTakesTheDumpRefusals(t *testing.T) {
	s := agedStore(t)
	notes := filepath.Join(s.Env.ProjectRoot(), "NOTES.md")
	writeFile(t, notes, "notes\n")
	if err := dump.New(s).Prune(io.Discard, dump.PruneArgs{OlderThan: 30, Tier: "D", Apply: true,
		Backup: notes}); err == nil {
		t.Fatal("prune's dump overwrote NOTES.md")
	}
	unchanged(t, notes, "notes\n")
	outside := filepath.Join(t.TempDir(), "dump.json")
	if err := dump.New(s).Prune(io.Discard, dump.PruneArgs{OlderThan: 30, Tier: "D", Apply: true,
		Backup: outside}); err == nil {
		t.Fatal("prune wrote the brain in cleartext outside the project")
	}
	if n, _ := s.Count("SELECT COUNT(*) FROM memories"); n != 1 {
		t.Error("a refused dump still let the prune delete")
	}
	if err := dump.New(s).Prune(io.Discard, dump.PruneArgs{OlderThan: 30, Tier: "D", Apply: true}); err != nil {
		t.Fatalf("the default dump path was refused: %v", err)
	}
}

// --- other generated files ---

// `task todo` followed a dangling TODO.md link and created the file it pointed at, and wrote
// outside the project on request without asking for --allow-outside.
func TestTodoRefusesALinkAndAPathOutsideTheProject(t *testing.T) {
	s := newTestStore(t)
	t.Chdir(s.Env.ProjectRoot())
	target := filepath.Join(t.TempDir(), "new-outside.md")
	plant(t, filepath.Join(s.Env.ProjectRoot(), "TODO.md"), target)
	if err := tasks.New(s).Task(io.Discard, tasks.TaskArgs{Sub: "todo"}); err == nil {
		t.Fatal("task todo wrote through a dangling link")
	}
	if _, err := os.Lstat(target); err == nil {
		t.Error("the link's target was created")
	}
	if err := tasks.New(s).Task(io.Discard, tasks.TaskArgs{Sub: "todo",
		Out: filepath.Join(t.TempDir(), "TODO.md")}); err == nil {
		t.Error("task todo wrote outside the project without --allow-outside")
	}
	if err := tasks.New(s).Task(io.Discard, tasks.TaskArgs{Sub: "todo", Out: "LIST.md"}); err != nil {
		t.Fatalf("an ordinary todo inside the project was refused: %v", err)
	}
	if err := tasks.New(s).Task(io.Discard, tasks.TaskArgs{Sub: "todo", Out: "LIST.md"}); err != nil {
		t.Fatalf("todo refused to replace its own output: %v", err)
	}
}

// export's marker was the bare key "schema_version", so any JSON config that happened to
// carry that key was treated as an old export and overwritten; and the write followed a link
// to wherever it pointed, --force or not.
func TestExportMarkerIsThisToolsAndTheWriteNeverFollowsALink(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Title: "nginx holds the wildcard certificate"})
	cfg := filepath.Join(s.Env.ProjectRoot(), "app.json")
	writeFile(t, cfg, `{"schema_version": 3, "db": "prod"}`)
	if err := dump.New(s).Export(io.Discard, dump.ExportArgs{Out: cfg}); err == nil {
		t.Fatal("export overwrote a JSON config that merely has a schema_version key")
	}
	unchanged(t, cfg, `{"schema_version": 3, "db": "prod"}`)

	out := filepath.Join(s.Env.ProjectRoot(), "brain", "dump.json")
	for i := 0; i < 2; i++ {
		if err := dump.New(s).Export(io.Discard, dump.ExportArgs{Out: out}); err != nil {
			t.Fatalf("export %d to its own file: %v", i+1, err)
		}
	}
	link := filepath.Join(s.Env.ProjectRoot(), "brain", "via-link.json")
	plant(t, link, out)
	before, _ := os.ReadFile(out)
	remember(t, s, memory.RememberArgs{Title: "a second fact that changes the dump"})
	if err := dump.New(s).Export(io.Discard, dump.ExportArgs{Out: link, Force: true}); err == nil {
		t.Fatal("export wrote through a link, --force or not")
	}
	unchanged(t, out, string(before))
}

// --- file modes ---

// The database was once created with the umask's mode and chmod'ed afterwards, and the WAL
// and shared-memory files, which SQLite creates with the main file's mode, stayed 0644:
// readable by every user on the machine. All three, and the safety copies, must be 0600
// from creation.
func TestTheStoreAndItsSidecarsAreOwnerOnlyFromCreation(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Title: "a write so the WAL exists"})
	for _, f := range []string{s.Env.DBPath, s.Env.DBPath + "-wal", s.Env.DBPath + "-shm"} {
		info, err := os.Stat(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s is %v, readable by others", filepath.Base(f), info.Mode().Perm())
		}
	}
	c, err := safety.New(s).MakeSafetyCopy()
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(c.Path); err != nil || info.Mode().Perm() != 0o600 || c.Memories != 1 {
		t.Errorf("safety copy: %v %v memories=%d problem=%q", info.Mode().Perm(), err,
			c.Memories, c.Problem)
	}
}

// --- WriteFileSafe itself ---

// WriteFileSafe is the one write path the commands above share. Each option is a guard:
// Root refuses linked parent directories and paths outside the root, Mode and MkdirMode set
// permissions at creation, Exclusive refuses any existing file, Marker refuses a file the
// tool did not generate unless forced. A write goes through a temporary file and a rename,
// and no temporary file may be left behind.
func TestWriteFileSafeKeepsEveryGuardAndLeavesNoTemporaryFile(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	plant(t, filepath.Join(root, "sub"), elsewhere)
	if err := fsguard.WriteFileSafe(filepath.Join(root, "sub", "f"), []byte("x"),
		fsguard.WriteOpts{Root: root, MkdirMode: 0o755}); err == nil {
		t.Error("wrote through a linked parent directory")
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Errorf("something landed in the link's target: %v", entries)
	}
	if err := fsguard.WriteFileSafe(filepath.Join(elsewhere, "f"), []byte("x"), fsguard.WriteOpts{Root: root}); err == nil {
		t.Error("wrote outside its root")
	}
	p := filepath.Join(root, "a", "b", "f")
	if err := fsguard.WriteFileSafe(p, []byte("one"), fsguard.WriteOpts{Root: root, MkdirMode: 0o700, Mode: 0o640}); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(p); info.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
	if err := fsguard.WriteFileSafe(p, []byte("two"), fsguard.WriteOpts{Root: root, Exclusive: true}); err == nil {
		t.Error("Exclusive replaced a file")
	}
	if err := fsguard.WriteFileSafe(p, []byte("three"), fsguard.WriteOpts{Root: root, Marker: "MINE"}); err == nil {
		t.Error("a file without the marker was replaced")
	}
	if err := fsguard.WriteFileSafe(p, []byte("four"), fsguard.WriteOpts{Root: root, Marker: "MINE", Force: true}); err != nil {
		t.Error(err)
	}
	unchanged(t, p, "four")
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Errorf("temporary files left behind: %v", entries)
	}
}

// wipeFixture is a store with three memories (one pinned and one a decision, so the
// load-bearing kinds are present) and one event.
func wipeFixture(t *testing.T) *store.Store {
	t.Helper()
	s := newTestStore(t)
	for _, m := range []memory.RememberArgs{
		{Title: "ordinary fact", Body: "a", Type: "knowledge"},
		{Title: "load bearing host", Body: "b", Type: "infra", Pin: true},
		{Title: "a decision that was made", Body: "c", Type: "decision"},
	} {
		m.Scope = "global"
		m.Source = "stated by the user"
		if err := memory.New(s).Remember(&bytes.Buffer{}, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := memory.New(s).Log(&bytes.Buffer{}, memory.LogArgs{Scope: "global", Kind: "note",
		Summary: "something happened"}); err != nil {
		t.Fatal(err)
	}
	return s
}

// countIn counts this project's rows in one table of the project database.
func countIn(t *testing.T, s *store.Store, table string) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE project=?",
		s.Env.Project).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
