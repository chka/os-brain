package dump

import "github.com/chka/os-brain/internal/store"

// Store is a brain opened for the commands that move its contents in and out: export,
// import, backup and prune, and the per-project dump and purge that wipe, share and
// uninstall build on. It adds them to the store underneath and nothing else.
type Store struct{ *store.Store }

// New gives the dump commands of an open brain.
func New(s *store.Store) *Store { return &Store{s} }
