package memory

import (
	"bytes"
	"io"
	"testing"

	"github.com/chka/os-brain/internal/diag"
	"github.com/chka/os-brain/internal/testguard"
	"github.com/chka/os-brain/internal/testutil"
)

// Every package with tests runs under the same floor: paths pinned to a sandbox, and a
// check afterwards that no real spine gained a project.
func TestMain(m *testing.M) { testguard.Run(m) }

// newTestStore is testutil.NewStore with the memory commands on top.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	return New(testutil.NewStore(t))
}

// clone is s as a second handle whose Env can change on its own: see testutil.Clone.
func clone(s *Store) Store { return Store{testutil.Clone(s.Store)} }

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

// lastID returns the id of the most recently stored memory.
func lastID(t *testing.T, s *Store) int64 {
	t.Helper()
	return testutil.LastID(t, s.Store)
}

// captureWarnings sends package warnings to w until the returned function is called.
func captureWarnings(w io.Writer) func() {
	return diag.SetOutput(w)
}
