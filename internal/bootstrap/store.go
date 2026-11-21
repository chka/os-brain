package bootstrap

import "github.com/chka/os-brain/internal/store"

// Store is a brain opened for the bootstrap command. It adds it to the store underneath and
// nothing else, so which transcripts were read is recorded in the very store the command
// opened.
type Store struct{ *store.Store }

// New gives the bootstrap command of an open brain.
func New(s *store.Store) *Store { return &Store{s} }
