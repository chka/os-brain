package spine

import "github.com/chka/os-brain/internal/store"

// Store is a brain opened for the spine commands. It adds them to the store underneath and
// nothing else, so an axiom, a standard, a note or a release is read and written through the
// spine connection, env and config of the command that opened the brain.
type Store struct{ *store.Store }

// New gives the spine commands of an open brain.
func New(s *store.Store) *Store { return &Store{s} }
