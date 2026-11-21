package project

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/chka/os-brain/internal/dump"
	"github.com/chka/os-brain/internal/install"
	"github.com/chka/os-brain/internal/memory"
	"github.com/chka/os-brain/internal/recall"
	"github.com/chka/os-brain/internal/spine"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/testguard"
	"github.com/chka/os-brain/internal/testutil"
)

// Every package with tests runs under the same floor: paths pinned to a sandbox, and a
// check afterwards that no real spine gained a project.
func TestMain(m *testing.M) { testguard.Run(m) }

// The tests were written inside one package, against one Store with every command on it.
// The names below give them the same vocabulary here, so the test bodies did not change
// when the package was split. The methods exist only in the test build: none of memory,
// recall, spine or dump imports project, so the tests may use them.

type (
	RecallArgs = recall.RecallArgs
	BackupArgs = dump.BackupArgs
)

func (s *Store) Log(w io.Writer, a memory.LogArgs) error { return memory.New(s.Store).Log(w, a) }

func (s *Store) Update(w io.Writer, a memory.UpdateArgs) error {
	return memory.New(s.Store).Update(w, a)
}

func (s *Store) Recall(w io.Writer, a RecallArgs) error { return recall.New(s.Store).Recall(w, a) }

func (s *Store) Axiom(w io.Writer, a spine.AxiomArgs) error { return spine.New(s.Store).Axiom(w, a) }

func (s *Store) Note(w io.Writer, a spine.NoteArgs) error { return spine.New(s.Store).Note(w, a) }

func (s *Store) Backup(w io.Writer, a BackupArgs) error { return dump.New(s.Store).Backup(w, a) }

func (s *Store) ProjectTables() ([]string, error) { return dump.New(s.Store).ProjectTables() }

// newTestStore is testutil.NewStore with the project commands on top.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	return New(testutil.NewStore(t))
}

// clone is s as a second handle whose Env can change on its own: see testutil.Clone.
func clone(s *Store) Store { return Store{testutil.Clone(s.Store)} }

// yes stands in for a person answering "yes" at the terminal, for every command that needs
// a person's confirmation (wakeup trust, axiom and standard changes).
func yes(string) (bool, error) { return true, nil }

// remember stores a memory through the ordinary command, filling in the defaults the CLI
// would supply, and returns what the command printed.
func remember(t *testing.T, s *Store, a memory.RememberArgs) string {
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
	if err := memory.New(s.Store).Remember(&buf, a); err != nil {
		t.Fatalf("remember %q: %v", a.Title, err)
	}
	return buf.String()
}

// recallJSON runs recall with --json and returns the decoded envelope.
func recallJSON(t *testing.T, s *Store, a RecallArgs) map[string]interface{} {
	t.Helper()
	a.JSON = true
	var buf bytes.Buffer
	if err := s.Recall(&buf, a); err != nil {
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

// mustJSON runs a command that writes JSON and returns the decoded object, failing the test
// if the command errors or prints anything that is not a JSON object.
func mustJSON(t *testing.T, fn func(io.Writer) error) map[string]interface{} {
	t.Helper()
	return testutil.MustJSON(t, fn)
}

// markPrivate declares a project private in the shared index, the way `private` does.
func markPrivate(t *testing.T, s *Store, name string) {
	t.Helper()
	testutil.MarkPrivate(t, s.Store, name)
}

// mustConfig reads brain.json in a test, where a parse error is a failure rather than a value.
func mustConfig(t *testing.T, path string) store.Config {
	t.Helper()
	c, err := store.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

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
	if err := install.Init(io.Discard, root, install.InitArgs{Dir: "brain", Agents: "claude"}); err != nil {
		t.Fatal(err)
	}
	st, err := open(store.NewEnv(filepath.Join(root, "brain")))
	if err != nil {
		t.Fatal(err)
	}
	remember(t, st, memory.RememberArgs{Scope: "global", Type: "infra",
		Title: "nginx holds the wildcard certificate", Body: "x"})
	return root, st
}
