package safety_test

import (
	"io"
	"testing"

	"github.com/chka/os-brain/internal/memory"
	"github.com/chka/os-brain/internal/safety"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/testguard"
	"github.com/chka/os-brain/internal/testutil"
)

// Every package with tests runs under the same floor: paths pinned to a sandbox, and a
// check afterwards that no real spine gained a project.
func TestMain(m *testing.M) { testguard.Run(m) }

// The tests were written inside one package, against one Store with every command on it.
// These thin wrappers give them the same names here, so the test bodies did not change when
// the package was split.

type (
	Store        struct{ *safety.Store }
	SafetyCopy   = safety.SafetyCopy
	RememberArgs = memory.RememberArgs
)

const SafetyKeep = safety.SafetyKeep

// seedTitles is the shared fixture of memory titles distinct enough to all be stored.
var seedTitles = testutil.SeedTitles

// newTestStore is testutil.NewStore with the safety commands on top.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	return &Store{safety.New(testutil.NewStore(t))}
}

// Open reopens a store the way the next session would, after a restore replaced its file.
func Open(env store.Env) (*Store, error) {
	s, err := store.Open(env)
	if err != nil {
		return nil, err
	}
	return &Store{safety.New(s)}, nil
}

func (s *Store) listCopies() ([]SafetyCopy, error) { return s.ListCopies() }

func (s *Store) rotate(copies []SafetyCopy) []string { return s.Rotate(copies) }

// Remember stores a memory through the ordinary command, which lives above this package.
func (s *Store) Remember(w io.Writer, a RememberArgs) error {
	return memory.New(s.Store.Store).Remember(w, a)
}
