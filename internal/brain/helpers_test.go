package brain

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// Helpers shared by many test files in this package. Their names are used across the
// package, so they are kept stable.

// newTestStore opens a brain whose every path points into a throwaway directory. A test
// suite that is not hermetic writes into the store the developer actually uses: an
// earlier suite once left dozens of phantom projects in a real store that way.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "brain")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "brain.json"),
		[]byte(`{"project":"testproj"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("OS_BRAIN_GLOBAL_DB", filepath.Join(dir, "config", "brain", "brain.db"))
	t.Setenv("OS_BRAIN_DB", filepath.Join(home, "brain.db"))
	t.Setenv("OS_BRAIN_PROJECT", "testproj")

	s, err := Open(NewEnv(home))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestARunLeavesTheRealStoreUntouched guards newTestStore itself: if it ever stopped
// pinning the paths, every test that calls it would write into a real brain. Both the
// os-brain default and the default of the team edition, which may be installed on the same
// machine, are checked.
func TestARunLeavesTheRealStoreUntouched(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "scratch", Body: "x"})
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
func remember(t *testing.T, s *Store, a RememberArgs) string {
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
	if err := s.Remember(&buf, a); err != nil {
		t.Fatalf("remember %q: %v", a.Title, err)
	}
	return buf.String()
}

// recall runs recall with --json and returns the decoded envelope.
func recall(t *testing.T, s *Store, a RecallArgs) map[string]interface{} {
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

// idOf returns the id of the memory with exactly this title.
func idOf(t *testing.T, s *Store, title string) int64 {
	t.Helper()
	var id int64
	if err := s.DB.QueryRow("SELECT id FROM memories WHERE title=?", title).Scan(&id); err != nil {
		t.Fatalf("no memory %q: %v", title, err)
	}
	return id
}

// writeDump writes an export file with this body and returns its path.
func writeDump(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "dump.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// knownProject puts a project in the shared index the way opening its brain once would
// have. A note is only sent to a project that exists, so note tests need a recipient.
func knownProject(t *testing.T, s *Store, name string) {
	t.Helper()
	g := s.globalConnect(true)
	if g == nil {
		t.Fatal("no spine")
	}
	defer s.closeGlobal(g)
	if _, err := g.Exec("INSERT INTO projects(root_id,name,last_seen) VALUES(?,?,?)",
		"test-"+name, name, Now()); err != nil {
		t.Fatal(err)
	}
}

// markPrivate declares a project private in the shared index, the way `private` does.
func markPrivate(t *testing.T, s *Store, name string) {
	t.Helper()
	g := s.globalConnect(true)
	if g == nil {
		t.Fatal("no shared index")
	}
	defer s.closeGlobal(g)
	if _, err := g.Exec(projectIDsDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Exec("INSERT OR REPLACE INTO project_ids(name, root_id, private, bound_at) "+
		"VALUES(?, 'elsewhere', 1, 1)", name); err != nil {
		t.Fatal(err)
	}
}

// ptr returns a pointer to v, for the optional fields of the command argument structs.
func ptr[T any](v T) *T { return &v }

// captureStderr returns what fn wrote to the process's stderr. Warnings and nudges go to
// the real stderr rather than the command's writer, so a test that only reads the writer
// never sees them.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}
