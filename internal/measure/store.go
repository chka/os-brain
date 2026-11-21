package measure

import "github.com/chka/os-brain/internal/store"

// Store is a brain opened for the measure commands. It adds them to the store underneath and
// nothing else, so every measure method reads and writes through the same connection, env
// and config as the command that opened the brain.
type Store struct{ *store.Store }

// New gives the measure commands of an open brain.
func New(s *store.Store) *Store { return &Store{s} }
