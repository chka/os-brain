package curate

import "github.com/chka/os-brain/internal/store"

// Store is a brain opened for review and promote. It adds them to the store underneath and
// nothing else, so a review reads, and a promotion writes, the very store the command opened.
type Store struct{ *store.Store }

// New gives review and promote over an open brain.
func New(s *store.Store) *Store { return &Store{s} }
