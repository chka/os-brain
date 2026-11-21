package recall

import "github.com/chka/os-brain/internal/store"

// Store is a brain opened for recall. It adds recall, coverage and the identity card to the
// store underneath and nothing else, so a search reads, and reinforces, the very store the
// command opened.
type Store struct{ *store.Store }

// New gives recall over an open brain.
func New(s *store.Store) *Store { return &Store{s} }
