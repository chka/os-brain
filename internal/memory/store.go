package memory

import "github.com/chka/os-brain/internal/store"

// Store is a brain opened for the commands that write and read single memories. It adds
// them to the store underneath and nothing else, so every guard here runs against the same
// connection, env and config as the command that opened the brain.
type Store struct{ *store.Store }

// New gives the memory commands of an open brain.
func New(s *store.Store) *Store { return &Store{s} }
