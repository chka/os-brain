package brief

import "github.com/chka/os-brain/internal/store"

// Store is a brain opened for what a session reads when it starts and when it asks what is
// open: brief, wakeup, the session report and open. It adds them to the store underneath and
// nothing else, so the brief describes the very store the command opened.
type Store struct{ *store.Store }

// New gives the brief and wakeup of an open brain.
func New(s *store.Store) *Store { return &Store{s} }
