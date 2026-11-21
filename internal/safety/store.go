package safety

import "github.com/chka/os-brain/internal/store"

// Store is a brain opened for the safety copies. It adds them to the store underneath and
// nothing else, so a copy is taken of, and restored into, the very store the command opened.
type Store struct{ *store.Store }

// New gives the safety-copy commands of an open brain.
func New(s *store.Store) *Store { return &Store{s} }
