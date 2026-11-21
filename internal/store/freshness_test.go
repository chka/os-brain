package store_test

import (
	"testing"

	"github.com/chka/os-brain/internal/memory"
	"github.com/chka/os-brain/internal/store"
)

// Six results must not mean six git subprocesses. Memories written at the same commit share
// one answer, and the common case (everything at HEAD) costs none at all.
func TestOneGitCallPerDistinctCommitNotPerMemory(t *testing.T) {
	s, dir := newGitTestStore(t)
	for _, title := range []string{"alpha handler", "beta handler", "gamma handler"} {
		remember(t, s, memory.RememberArgs{Type: "gotcha", Title: title})
	}
	commit(t, dir, "two")
	tree := s.TreeState()
	ms, err := s.QueryMemories("SELECT " + store.MemoryCols + ", 0.0 AS bm FROM memories m")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		tree.For(m, s.Env.Project)
	}
	if store.MemoisedCommits(tree) != 1 {
		t.Errorf("memoised %d distances for one distinct commit", store.MemoisedCommits(tree))
	}
}
