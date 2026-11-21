package doctor

import "github.com/chka/os-brain/internal/store"

// Store is a brain opened for doctor and vacuum. It adds them to the store underneath and
// nothing else, so doctor checks, and with --fix repairs, the very store the command opened.
type Store struct{ *store.Store }

// New gives doctor over an open brain.
func New(s *store.Store) *Store { return &Store{s} }
