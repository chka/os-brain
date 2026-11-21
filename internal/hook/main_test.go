package hook

import (
	"bytes"
	"io"
	"testing"

	"github.com/chka/os-brain/internal/memory"
	"github.com/chka/os-brain/internal/tasks"
	"github.com/chka/os-brain/internal/testguard"
	"github.com/chka/os-brain/internal/testutil"
)

// Every package with tests runs under the same floor: paths pinned to a sandbox, and a
// check afterwards that no real spine gained a project.
func TestMain(m *testing.M) { testguard.Run(m) }

// The tests were written inside one package, against one Store with every command on it.
// Remember and Task give them the same vocabulary here, so the test bodies did not change
// when the package was split. They exist only in the test build.

func (s *Store) Remember(w io.Writer, a memory.RememberArgs) error {
	return memory.New(s.Store).Remember(w, a)
}

func (s *Store) Task(w io.Writer, a tasks.TaskArgs) error { return tasks.New(s.Store).Task(w, a) }

// newTestStore is testutil.NewStore with the hooks on top.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	return New(testutil.NewStore(t))
}

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
	if err := s.Remember(&buf, a); err != nil {
		t.Fatalf("remember %q: %v", a.Title, err)
	}
	return buf.String()
}
