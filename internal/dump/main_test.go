package dump_test

import (
	"bytes"
	"io"
	"testing"

	"github.com/chka/os-brain/internal/dump"
	"github.com/chka/os-brain/internal/memory"
	"github.com/chka/os-brain/internal/testguard"
	"github.com/chka/os-brain/internal/testutil"
)

// Every package with tests runs under the same floor: paths pinned to a sandbox, and a
// check afterwards that no real spine gained a project.
func TestMain(m *testing.M) { testguard.Run(m) }

// The tests are an external package because they seed memories through remember, which
// sits above this package. They were written inside one package, against one Store with
// every command on it; these thin wrappers give them the same names here, so the test
// bodies did not change when the package was split.

type (
	Store        struct{ *dump.Store }
	RememberArgs = memory.RememberArgs
	ExportArgs   = dump.ExportArgs
	ImportArgs   = dump.ImportArgs
	BackupArgs   = dump.BackupArgs
)

// newTestStore is testutil.NewStore with the dump commands on top.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	return &Store{dump.New(testutil.NewStore(t))}
}

// Remember stores a memory through the ordinary command, which lives above this package.
func (s *Store) Remember(w io.Writer, a RememberArgs) error {
	return memory.New(s.Store.Store).Remember(w, a)
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

// idOf returns the id of the memory with exactly this title.
func idOf(t *testing.T, s *Store, title string) int64 {
	t.Helper()
	return testutil.IDOf(t, s.Store.Store, title)
}

// markPrivate declares a project private in the shared index, the way `private` does.
func markPrivate(t *testing.T, s *Store, name string) {
	t.Helper()
	testutil.MarkPrivate(t, s.Store.Store, name)
}

// writeDump writes an export file with this body and returns its path.
func writeDump(t *testing.T, body string) string {
	t.Helper()
	return testutil.WriteDump(t, body)
}
