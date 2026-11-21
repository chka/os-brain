package hook

import "github.com/chka/os-brain/internal/store"

// Store is a brain opened for the per-turn hooks: prompt recall, prefer and capture. It adds
// them to the store underneath and nothing else, so a hook reads and writes the very store
// the host's hook command opened.
type Store struct{ *store.Store }

// New gives the per-turn hooks of an open brain.
func New(s *store.Store) *Store { return &Store{s} }
