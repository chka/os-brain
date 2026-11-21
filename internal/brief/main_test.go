package brief

import (
	"bytes"
	"io"
	"testing"

	"github.com/chka/os-brain/internal/bootstrap"
	"github.com/chka/os-brain/internal/lead"
	"github.com/chka/os-brain/internal/measure"
	"github.com/chka/os-brain/internal/memory"
	"github.com/chka/os-brain/internal/project"
	"github.com/chka/os-brain/internal/recall"
	"github.com/chka/os-brain/internal/spine"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/tasks"
	"github.com/chka/os-brain/internal/testguard"
	"github.com/chka/os-brain/internal/testutil"
)

// Every package with tests runs under the same floor: paths pinned to a sandbox, and a
// check afterwards that no real spine gained a project.
func TestMain(m *testing.M) { testguard.Run(m) }

// The tests were written inside one package, against one Store with every command on it.
// The names below give them the same vocabulary here, so the test bodies did not change
// when the package was split. The methods exist only in the test build, so the tests can
// set up what the brief reads through the ordinary commands; none of those packages
// imports brief.

type (
	BootstrapArgs = bootstrap.BootstrapArgs
	ContextArgs   = project.ContextArgs
	RecallArgs    = recall.RecallArgs
)

func (s *Store) Remember(w io.Writer, a memory.RememberArgs) error {
	return memory.New(s.Store).Remember(w, a)
}

func (s *Store) Log(w io.Writer, a memory.LogArgs) error { return memory.New(s.Store).Log(w, a) }

func (s *Store) Update(w io.Writer, a memory.UpdateArgs) error {
	return memory.New(s.Store).Update(w, a)
}

func (s *Store) Verify(w io.Writer, a memory.VerifyArgs) error {
	return memory.New(s.Store).Verify(w, a)
}

func (s *Store) Recall(w io.Writer, a RecallArgs) error { return recall.New(s.Store).Recall(w, a) }

func (s *Store) Axiom(w io.Writer, a spine.AxiomArgs) error { return spine.New(s.Store).Axiom(w, a) }

func (s *Store) Axioms() []spine.Axiom { return spine.New(s.Store).Axioms() }

func (s *Store) Bootstrap(w io.Writer, a BootstrapArgs) error {
	return bootstrap.New(s.Store).Bootstrap(w, a)
}

func (s *Store) Context(w io.Writer, a ContextArgs) error { return project.New(s.Store).Context(w, a) }

func (s *Store) Lead(w io.Writer, a lead.LeadArgs) error { return lead.New(s.Store).Lead(w, a) }

func (s *Store) Measure(w io.Writer, a measure.MeasureArgs) error {
	return measure.New(s.Store).Measure(w, a)
}

func (s *Store) Task(w io.Writer, a tasks.TaskArgs) error { return tasks.New(s.Store).Task(w, a) }

// newTestStore is testutil.NewStore with the brief on top.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	return New(testutil.NewStore(t))
}

// newGitTestStore is testutil.NewGitStore with the brief on top: a throwaway brain in a real
// git repository with one commit, and the repository directory.
func newGitTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	s, dir := testutil.NewGitStore(t)
	return New(s), dir
}

// git and commit are testutil.Git and testutil.Commit, under the names this package's
// tests use.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return testutil.Git(t, dir, args...)
}

func commit(t *testing.T, dir, name string) {
	t.Helper()
	testutil.Commit(t, dir, name)
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

// ptr returns a pointer to v, for the optional fields of the command argument structs.
func ptr[T any](v T) *T { return &v }

// mustConfig reads brain.json in a test, where a parse error is a failure rather than a value.
func mustConfig(t *testing.T, path string) store.Config {
	t.Helper()
	c, err := store.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
