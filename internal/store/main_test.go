package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chka/os-brain/internal/testguard"
)

// Every package with tests runs under the same floor: paths pinned to a sandbox, and a
// check afterwards that no real spine gained a project.
func TestMain(m *testing.M) { testguard.Run(m) }

// newTestStore opens a brain whose every path points into a throwaway directory. It is
// testutil.NewStore, which this package's own tests cannot import: testutil imports the
// store, and a package's internal tests importing it would be an import cycle.
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
