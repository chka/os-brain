package overview

import "github.com/chka/os-brain/internal/store"

// Store is a brain opened for the commands that show it whole: map, describe, snapshot and
// the user model. It adds them to the store underneath and nothing else, so each one reads
// the very store the command opened.
type Store struct{ *store.Store }

// New gives the overview commands of an open brain.
func New(s *store.Store) *Store { return &Store{s} }
