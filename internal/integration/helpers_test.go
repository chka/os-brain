package integration_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/chka/os-brain/internal/install"
	"github.com/chka/os-brain/internal/memory"
	"github.com/chka/os-brain/internal/recall"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/testutil"
)

// Helpers shared by many test files in this package. Their names are used across the
// package, so they are kept stable.

// newTestStore is testutil.NewStore under the name the tests use: a brain whose every path
// points into a throwaway directory.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	return testutil.NewStore(t)
}

// TestARunLeavesTheRealStoreUntouched guards newTestStore itself: if it ever stopped
// pinning the paths, every test that calls it would write into a real brain. Both the
// os-brain default and the default of the team edition, which may be installed on the same
// machine, are checked.
func TestARunLeavesTheRealStoreUntouched(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Title: "scratch", Body: "x"})
	home := os.Getenv("HOME")
	for _, real := range []string{
		filepath.Join(home, ".claude", "brain", "brain.db"),
		filepath.Join(home, ".claude", "os-brain", "brain.db"),
	} {
		if s.Env.DBPath == real || s.Env.GlobalDB == real {
			t.Fatalf("the test store points at the real one: %s / %s", s.Env.DBPath, s.Env.GlobalDB)
		}
	}
}

// remember stores a memory through the ordinary command, filling in the defaults the CLI
// would supply, and returns what the command printed.
func remember(t *testing.T, s *store.Store, a memory.RememberArgs) string {
	t.Helper()
	var buf bytes.Buffer
	if a.Type == "" {
		a.Type = "note"
	}
	if a.Scope == "" {
		a.Scope = "global"
	}
	if a.Importance == 0 {
		a.Importance = 3
	}
	if a.Source == "" {
		a.Source = "session"
	}
	if err := memory.New(s).Remember(&buf, a); err != nil {
		t.Fatalf("remember %q: %v", a.Title, err)
	}
	return buf.String()
}

// recallJSON runs recall with --json and returns the decoded envelope.
func recallJSON(t *testing.T, s *store.Store, a recall.RecallArgs) map[string]interface{} {
	t.Helper()
	a.JSON = true
	var buf bytes.Buffer
	if err := recall.New(s).Recall(&buf, a); err != nil {
		t.Fatalf("recall %q: %v", a.Query, err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("recall json: %v (%s)", err, buf.String())
	}
	return out
}

// titles lists the titles of a recall envelope's results, in rank order.
func titles(res map[string]interface{}) []string {
	var out []string
	for _, r := range res["results"].([]interface{}) {
		out = append(out, r.(map[string]interface{})["title"].(string))
	}
	return out
}

// idOf returns the id of the memory with exactly this title.
func idOf(t *testing.T, s *store.Store, title string) int64 {
	t.Helper()
	return testutil.IDOf(t, s, title)
}

// installedProject builds a project that has its own CLAUDE.md, .gitignore and Claude Code
// settings (a permission and a hook of its own), installs a brain into it and stores one
// memory. Everything the project owned before init must survive uninstall.
func installedProject(t *testing.T) (root string, st *store.Store) {
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
	if err := install.Init(io.Discard, root, install.InitArgs{Dir: "brain", Agents: "claude"}); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(store.NewEnv(filepath.Join(root, "brain")))
	if err != nil {
		t.Fatal(err)
	}
	remember(t, st, memory.RememberArgs{Scope: "global", Type: "infra",
		Title: "nginx holds the wildcard certificate", Body: "x"})
	return root, st
}

// sharedInstall turns installedProject's brain into one on the shared store and gives it
// two memories.
func sharedInstall(t *testing.T) (string, *store.Store) {
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
	st2, err := store.Open(store.NewEnv(filepath.Join(root, "brain")))
	if err != nil {
		t.Fatal(err)
	}
	if !st2.Env.Shared {
		t.Fatal("the fixture did not end up on the shared store")
	}
	remember(t, st2, memory.RememberArgs{Scope: "global", Type: "infra",
		Title: "nginx holds the wildcard certificate", Body: "x"})
	remember(t, st2, memory.RememberArgs{Scope: "global", Type: "knowledge",
		Title: "the shared one", Body: "y"})
	return root, st2
}

// yes stands in for a person answering "yes" at the terminal, for every command that needs
// a person's confirmation (wakeup trust, axiom and standard changes).
func yes(string) (bool, error) { return true, nil }
